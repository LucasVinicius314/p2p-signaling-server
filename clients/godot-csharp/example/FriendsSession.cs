using System;
using Godot;
using P2PSignaling;

namespace P2PSignalingExample;

/// <summary>
/// Shows the friends feature: a host makes its room visible to friends, and a friend finds it without being told the
/// code. Run it for real against `go run ./cmd/devserver -bucket game`, with a separate home folder per player
/// (P2P_SIGNALING_HOME) so one machine can play several people:
///
///   host:    P2P_SIGNALING_HOME=/tmp/host  godot --headless --path clients/godot-csharp/example res://FriendsSession.tscn -- --host
///   (read the host's id from /tmp/host/id and put it on a line of /tmp/friend/friends)
///   friend:  P2P_SIGNALING_HOME=/tmp/friend godot --headless --path clients/godot-csharp/example res://FriendsSession.tscn
///   stranger (nobody's id in the friends file): add --expect-none
///
/// Options: --url=, --bucket=, --version=, --name=. Prints FRIENDS OK and exits 0 on success, 1 on failure.
/// The friend asks for rooms again every second until it finds one (the host may not be up yet).
/// </summary>
public partial class FriendsSession : Node
{
    private P2PLobby _lobby = null!;
    private bool _isHost;
    private bool _expectNone;
    private double _elapsed;
    private double _sinceAsk = 99;
    private int _asked;
    private bool _joining;
    private bool _done;

    public override void _Ready()
    {
        string url = Arg("url", "http://127.0.0.1:8099");
        string bucket = Arg("bucket", "game");
        _isHost = Arg("host", "") == "true";
        _expectNone = Arg("expect-none", "") == "true";

        _lobby = new P2PLobby(url, bucket, Arg("version", "1.0.0")) { PlayerName = Arg("name", _isHost ? "Hosty" : "Friendly") };
        AddChild(_lobby);
        _lobby.PeerCreated += peer => Multiplayer.MultiplayerPeer = peer; // rule 1 of docs/integration-guide.md
        _lobby.Failed += (c, m) => Finish(false, $"{c}: {m}");
        _lobby.RoomClosed += reason => Finish(false, $"room closed: {reason}");

        if (_isHost)
        {
            GD.Print($"HOST ID {P2PIdentity.OwnId}"); // what the host gives to friends
            Multiplayer.PeerConnected += id => Finish(true, $"a friend (peer {id}) found the room and joined");
            _lobby.ListForFriends = true; // the line that makes the room visible to friends
            _lobby.CodeReady += code => GD.Print($"CODE {code} (listed to friends)");
            _lobby.CreateRoom();
        }
        else
        {
            Multiplayer.ConnectedToServer += () => Finish(true, "joined the host by the code from the friends list");
            _lobby.FriendRoomsReceived += OnRooms;
        }
    }

    private void OnRooms(System.Collections.Generic.IReadOnlyList<FriendRoom> rooms)
    {
        foreach (var problem in _lobby.FriendFileProblems)
            GD.Print($"friends file: {problem}");
        GD.Print($"list {_asked}: {rooms.Count} room(s)");
        if (rooms.Count == 0 || _joining)
            return;
        var room = rooms[0];
        GD.Print($"friend room: code {room.Code}, host '{room.HostName}', note '{room.FriendLabel}', {room.Players}/{room.MaxPlayers}");
        if (_expectNone)
        {
            Finish(false, "a stranger was shown a room");
            return;
        }
        _joining = true;
        _lobby.JoinRoom(room.Code);
    }

    public override void _Process(double delta)
    {
        _elapsed += delta;
        if (!_isHost && !_joining)
        {
            _sinceAsk += delta;
            if (_sinceAsk >= 1)
            {
                _sinceAsk = 0;
                _asked++;
                if (_expectNone && _asked > 4)
                    Finish(true, "no rooms were shown to someone nobody listed");
                else
                    _lobby.ListFriendRooms();
            }
        }
        if (!_done && _elapsed > (_isHost ? 120 : 30)) // a host may wait a while for a friend
            Finish(false, "timed out");
    }

    private void Finish(bool ok, string message)
    {
        if (_done)
            return;
        _done = true;
        GD.Print(ok ? $"FRIENDS OK - {message}" : $"FRIENDS FAILED - {message}");
        _lobby.Leave();
        GetTree().CreateTimer(0.3).Timeout += () => GetTree().Quit(ok ? 0 : 1);
    }

    private static string Arg(string name, string fallback)
    {
        foreach (var arg in OS.GetCmdlineUserArgs())
        {
            if (arg == $"--{name}")
                return "true";
            if (arg.StartsWith($"--{name}=", StringComparison.Ordinal))
                return arg[(name.Length + 3)..];
        }
        return fallback;
    }
}
