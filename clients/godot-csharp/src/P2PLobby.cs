using System.Text;
using Godot;

namespace P2PSignaling;

/// <summary>
/// Creates or joins a room through p2p-signaling-server and sets up the WebRTC links.
/// Add it to the scene tree, call <see cref="CreateRoom"/> or <see cref="JoinRoom"/>, and when
/// <see cref="Connected"/> fires assign the peer: <c>Multiplayer.MultiplayerPeer = peer;</c>.
/// </summary>
public partial class P2PLobby : Node
{
    /// <summary>Server base URL, e.g. <c>https://p2p.example.com</c> (http/https become ws/wss).</summary>
    public string Url { get; set; } = "";
    public string Bucket { get; set; } = "";
    public string GameVersion { get; set; } = "";
    /// <summary>Optional display name; the host sees it in <see cref="JoinRequest"/>.</summary>
    public string PlayerName { get; set; } = "";
    /// <summary>Requested room size for <see cref="CreateRoom"/>; 0 uses the bucket's maximum.</summary>
    public int MaxPlayers { get; set; }
    /// <summary>How long a WebRTC link may take to come up before <see cref="Failed"/> fires.</summary>
    public double ConnectTimeoutSeconds { get; set; } = 20;

    /// <summary>True once <see cref="Connected"/> has fired.</summary>
    public bool IsLinked => _connectedRaised;

    public bool IsHost { get; private set; }
    /// <summary>Our peer id (1 for the host); 0 until in a room.</summary>
    public int PeerId { get; private set; }
    public string? RoomCode { get; private set; }
    /// <summary>The WebRTCMultiplayerPeer (typed as its bound base class, see <see cref="Rtc"/>).</summary>
    public MultiplayerPeer? Peer { get; private set; }

    /// <summary>Host: the room exists and this is its join code.</summary>
    public event Action<string>? CodeReady;
    /// <summary>The multiplayer peer is ready to assign. Host: right after the room exists. Client: once the host link is up.</summary>
    public event Action<MultiplayerPeer>? Connected;
    /// <summary>Client: waiting for the host to approve.</summary>
    public event Action? JoinPending;
    /// <summary>Host (approval mode): someone wants in; answer with <see cref="Approve"/>.</summary>
    public event Action<int, string>? JoinRequest;
    /// <summary>Host: a pending request was withdrawn or timed out.</summary>
    public event Action<int>? JoinCancelled;
    public event Action<int>? PeerConnected;
    public event Action<int>? PeerDisconnected;
    /// <summary>The room is gone (host left, idle, closed by an admin). Arguments: reason.</summary>
    public event Action<string>? RoomClosed;
    /// <summary>Something went wrong: a server error code (see docs/protocol.md) or a local one such as connect_timeout.</summary>
    public event Action<string, string>? Failed;

    private enum Phase { Idle, Connecting, AwaitWelcome, Ready }

    private sealed class Link
    {
        public required GodotObject Conn;
        public double Age;
        public bool Up;
    }

    private const double KeepaliveSeconds = 30;
    private const double SocketTimeoutSeconds = 10;

    private WebSocketPeer? _ws;
    private Phase _phase = Phase.Idle;
    private Action? _onReady;
    private bool _helloSent;
    private double _phaseAge;
    private double _pingAge;
    private bool _connectedRaised;
    private string _topology = "star";
    private Godot.Collections.Dictionary _ice = new();
    private readonly Dictionary<int, Link> _links = new();

    public P2PLobby() { }

    public P2PLobby(string url, string bucket, string gameVersion)
    {
        Url = url;
        Bucket = bucket;
        GameVersion = gameVersion;
    }

    /// <summary>Become the host. Raises <see cref="CodeReady"/> then <see cref="Connected"/>.</summary>
    public void CreateRoom() => WhenReady(() => Send(Outgoing.CreateRoom(MaxPlayers)));

    /// <summary>Join a room by its code. Raises <see cref="Connected"/> when the host link is up.</summary>
    public void JoinRoom(string code) => WhenReady(() => Send(Outgoing.JoinRoom(code)));

    /// <summary>Host with approval mode: admit or refuse a <see cref="JoinRequest"/>.</summary>
    public void Approve(int peerId, bool accept) => Send(Outgoing.Approve(peerId, accept));

    /// <summary>Host: refuse (or allow again) new joins.</summary>
    public void SetLocked(bool locked) => Send(Outgoing.Lock(locked));

    /// <summary>Leave the room and drop all connections.</summary>
    public void Leave()
    {
        if (_phase == Phase.Ready) Send(Outgoing.Leave());
        Teardown(closeSocket: true);
    }

    public override void _ExitTree() => Teardown(closeSocket: true);

    public override void _Process(double delta)
    {
        if (_ws != null) PumpSocket(delta);
        PollRtc();
        TickLinks(delta);
    }

    // ---- signaling socket ----

    private void WhenReady(Action action)
    {
        if (_phase == Phase.Ready) { action(); return; }
        _onReady = action;
        if (_phase == Phase.Idle) Connect();
    }

    private void Connect()
    {
        _ws = new WebSocketPeer();
        var err = _ws.ConnectToUrl(WsUrl());
        if (err != Error.Ok)
        {
            _ws = null;
            Failed?.Invoke("connect_failed", $"could not start the connection ({err})");
            return;
        }
        _phase = Phase.Connecting;
        _helloSent = false;
        _phaseAge = 0;
    }

    private string WsUrl()
    {
        var u = Url.TrimEnd('/');
        if (u.StartsWith("https://")) u = "wss://" + u[8..];
        else if (u.StartsWith("http://")) u = "ws://" + u[7..];
        return u.EndsWith("/ws") ? u : u + "/ws";
    }

    private void Send(string json)
    {
        if (_ws == null || _ws.GetReadyState() != WebSocketPeer.State.Open) return;
        _ws.SendText(json);
        _pingAge = 0;
    }

    private void PumpSocket(double delta)
    {
        var ws = _ws!;
        ws.Poll();
        switch (ws.GetReadyState())
        {
            case WebSocketPeer.State.Connecting:
                _phaseAge += delta;
                if (_phaseAge > SocketTimeoutSeconds) Fatal("connect_timeout", "the signaling server did not answer");
                break;

            case WebSocketPeer.State.Open:
                if (!_helloSent)
                {
                    _helloSent = true;
                    _phase = Phase.AwaitWelcome;
                    ws.SendText(Outgoing.Hello(Bucket, GameVersion, PlayerName));
                }
                while (_ws != null && ws.GetAvailablePacketCount() > 0)
                    HandleText(Encoding.UTF8.GetString(ws.GetPacket()));
                if (_ws != null && _phase == Phase.Ready && RoomCode != null)
                {
                    _pingAge += delta; // any message keeps the room from idling out
                    if (_pingAge >= KeepaliveSeconds) Send(Outgoing.Ping());
                }
                break;

            case WebSocketPeer.State.Closed:
                _ws = null;
                _phase = Phase.Idle;
                // After the host link is up the game no longer needs signaling.
                if (!_connectedRaised) Fatal("signaling_closed", "the signaling connection closed");
                break;
        }
    }

    private void HandleText(string text)
    {
        if (!ServerMessage.TryParse(text, out var m)) return;
        switch (m.Type)
        {
            case "welcome":
                _ice = IceConfig.Build(m);
                _topology = m.Str("topology") ?? "star";
                _phase = Phase.Ready;
                var run = _onReady;
                _onReady = null;
                run?.Invoke();
                break;

            case "room_created":
                IsHost = true;
                PeerId = 1;
                RoomCode = m.Str("code");
                StartPeer(host: true);
                CodeReady?.Invoke(RoomCode ?? "");
                RaiseConnected();
                break;

            case "join_pending":
                JoinPending?.Invoke();
                break;

            case "join_request":
                JoinRequest?.Invoke(m.Int("peer"), m.Str("name") ?? "");
                break;

            case "join_cancelled":
                JoinCancelled?.Invoke(m.Int("peer"));
                break;

            case "joined":
                PeerId = m.Int("peer_id");
                StartPeer(host: false);
                foreach (var id in m.IntArray("peers")) EnsureLink(id);
                break;

            case "peer_joined":
                EnsureLink(m.Int("peer_id"));
                break;

            case "peer_left":
                DropLink(m.Int("peer_id"));
                break;

            case "signal":
                HandleSignal(m);
                break;

            case "room_closed":
                var reason = m.Str("reason") ?? "";
                Teardown(closeSocket: true);
                RoomClosed?.Invoke(reason);
                break;

            case "error":
                var code = m.Str("code") ?? "error";
                var message = m.Str("message") ?? "";
                if (_phase == Phase.Ready) Failed?.Invoke(code, message); // e.g. room_not_found: caller may retry
                else Fatal(code, message);                                // hello was rejected
                break;
        }
    }

    // ---- WebRTC ----

    private void StartPeer(bool host)
    {
        var peer = Rtc.NewMultiplayerPeer();
        if (peer == null)
        {
            Fatal("webrtc_unavailable", "WebRTC is not available; desktop exports need the webrtc-native extension");
            return;
        }
        Peer = peer;
        Error err;
        if (_topology == "mesh") err = Rtc.CreateMesh(peer, PeerId);
        else err = host ? Rtc.CreateServer(peer) : Rtc.CreateClient(peer, PeerId);
        if (err != Error.Ok)
        {
            Fatal("webrtc_unavailable", $"could not set up the multiplayer peer ({err})");
            return;
        }
        peer.PeerConnected += id =>
        {
            var pid = (int)id;
            if (_links.TryGetValue(pid, out var link)) link.Up = true;
            PeerConnected?.Invoke(pid);
            if (!IsHost && pid == 1) RaiseConnected();
        };
        peer.PeerDisconnected += id => PeerDisconnected?.Invoke((int)id);
    }

    private void RaiseConnected()
    {
        if (_connectedRaised || Peer == null) return;
        _connectedRaised = true;
        Connected?.Invoke(Peer);
    }

    private Link? EnsureLink(int id)
    {
        if (Peer == null || id == PeerId) return null;
        if (_links.TryGetValue(id, out var existing)) return existing;

        var conn = Rtc.NewConnection();
        if (conn == null || Rtc.Initialize(conn, _ice) != Error.Ok)
        {
            Fatal("webrtc_unavailable", "WebRTC is not available; desktop exports need the webrtc-native extension");
            return null;
        }
        conn.Connect("session_description_created", Callable.From<string, string>((type, sdp) =>
        {
            Rtc.SetLocalDescription(conn, type, sdp);
            Send(Outgoing.Description(id, type, sdp));
        }));
        conn.Connect("ice_candidate_created", Callable.From<string, long, string>((media, index, name) =>
            Send(Outgoing.Candidate(id, media, index, name))));

        var link = new Link { Conn = conn };
        _links[id] = link;
        Rtc.AddPeer(Peer, conn, id); // must happen before CreateOffer: it creates the data channels
        if (PeerId > id) Rtc.CreateOffer(conn); // the higher id offers, so both sides never offer at once
        return link;
    }

    private void HandleSignal(ServerMessage m)
    {
        var from = m.Int("from");
        var link = EnsureLink(from);
        if (link == null || m.Raw("data") is not { } data) return;
        switch (m.Str("kind"))
        {
            case "offer":
            case "answer":
                if (data.TryGetProperty("sdp", out var sdp))
                    Rtc.SetRemoteDescription(link.Conn, m.Str("kind")!, sdp.GetString() ?? "");
                break;
            case "candidate":
                if (data.TryGetProperty("media", out var media) && data.TryGetProperty("index", out var index) && data.TryGetProperty("name", out var name))
                    Rtc.AddIceCandidate(link.Conn, media.GetString() ?? "", index.GetInt64(), name.GetString() ?? "");
                break;
        }
    }

    private void DropLink(int id)
    {
        if (!_links.Remove(id, out var link)) return;
        if (Peer != null) Rtc.RemovePeer(Peer, id);
        Rtc.Close(link.Conn);
    }

    private void PollRtc()
    {
        // Once the game assigns the peer to Multiplayer, SceneMultiplayer polls it; until then we must.
        if (Peer != null && IsInsideTree() && GetTree().GetMultiplayer().MultiplayerPeer != Peer) Peer.Poll();
    }

    private void TickLinks(double delta)
    {
        if (_links.Count == 0) return;
        foreach (var (id, link) in _links.ToArray())
        {
            if (link.Up) continue;
            link.Age += delta;
            if (link.Age > ConnectTimeoutSeconds)
            {
                if (!IsHost && id == 1) Fatal("connect_timeout", "could not establish a direct connection to the host");
                else DropLink(id);
            }
        }
    }

    // ---- failure and cleanup ----

    private void Fatal(string code, string message)
    {
        Teardown(closeSocket: true);
        Failed?.Invoke(code, message);
    }

    private void Teardown(bool closeSocket)
    {
        foreach (var link in _links.Values) Rtc.Close(link.Conn);
        _links.Clear();
        Peer?.Close();
        Peer = null;
        RoomCode = null;
        PeerId = 0;
        IsHost = false;
        _connectedRaised = false;
        _onReady = null;
        if (closeSocket)
        {
            _ws?.Close();
            _ws = null;
            _phase = Phase.Idle;
        }
    }
}
