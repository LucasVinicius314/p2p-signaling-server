using System.Text;
using Godot;
using P2PSignaling;

namespace P2PSignalingExample;

/// <summary>
/// Loopback test: one process plays host and client. The host creates a room, the client joins by
/// code, a real WebRTC link comes up, and a packet crosses it. Exits 0 on success, 1 on failure.
/// Configure with env vars P2P_URL (default http://localhost:8080), P2P_BUCKET (default loopback),
/// P2P_VERSION (default 1.0.0).
/// </summary>
public partial class Loopback : Node
{
    private P2PLobby _host = null!;
    private P2PLobby _client = null!;
    private double _elapsed;
    private bool _sent, _done;
    private int _clientId;

    public override void _Ready()
    {
        var url = Env("P2P_URL", "http://localhost:8080");
        var bucket = Env("P2P_BUCKET", "loopback");
        var version = Env("P2P_VERSION", "1.0.0");

        _host = new P2PLobby(url, bucket, version) { PlayerName = "host" };
        _client = new P2PLobby(url, bucket, version) { PlayerName = "client" };
        AddChild(_host);
        AddChild(_client);

        _host.Failed += (c, m) => Finish(false, $"host failed: {c} {m}");
        _client.Failed += (c, m) => Finish(false, $"client failed: {c} {m}");
        _host.PeerConnected += id => { _clientId = id; GD.Print($"host: peer {id} connected"); };
        _host.Connected += _ => GD.Print("host: ready");
        _client.Connected += _ => GD.Print("client: connected to host");
        _host.CodeReady += code =>
        {
            GD.Print($"host: room code {code}");
            _client.JoinRoom(code);
        };

        GD.Print($"connecting to {url} bucket={bucket}");
        _host.CreateRoom();
    }

    public override void _Process(double delta)
    {
        if (_done) return;
        _elapsed += delta;
        if (_elapsed > 30) { Finish(false, "timed out"); return; }

        // Peers are not assigned to Multiplayer here, so the lobbies poll them; use the raw packet API.
        if (!_sent && _client.Peer != null && _host.Peer != null && _clientId != 0 && _client.IsLinked)
        {
            _sent = true;
            _client.Peer.SetTargetPeer(1);
            _client.Peer.PutPacket(Encoding.UTF8.GetBytes("hello over webrtc"));
            GD.Print("client: sent packet");
        }
        if (_sent && _host.Peer != null && _host.Peer.GetAvailablePacketCount() > 0)
        {
            var text = Encoding.UTF8.GetString(_host.Peer.GetPacket());
            Finish(text == "hello over webrtc", $"host received: {text}");
        }
    }

    private void Finish(bool ok, string message)
    {
        if (_done) return;
        _done = true;
        GD.Print(ok ? $"LOOPBACK OK - {message}" : $"LOOPBACK FAILED - {message}");
        GetTree().Quit(ok ? 0 : 1);
    }

    private static string Env(string name, string fallback)
    {
        var v = OS.GetEnvironment(name);
        return string.IsNullOrEmpty(v) ? fallback : v;
    }
}
