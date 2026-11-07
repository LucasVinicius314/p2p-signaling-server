using System;
using Godot;
using P2PSignaling;

namespace P2PSignalingExample;

/// <summary>
/// The smallest correct way to use the library with Godot's multiplayer (RPCs). Copy the wiring, not the game logic.
/// It is compiled by CI and was run for real: one process hosts, another joins by code, the client sends an RPC to the
/// host and the host answers with one.
///
/// Run (two terminals, against `go run ./cmd/devserver -bucket game`):
///   host:   godot --headless --path clients/godot-csharp/example res://MinimalSession.tscn -- --host
///   client: godot --headless --path clients/godot-csharp/example res://MinimalSession.tscn -- --join=CODE
/// Options: --url=, --bucket=, --version=, --name=. It prints CODE, RPC OK and exits 0 on success, 1 on failure.
///
/// The rules this file demonstrates (see docs/integration-guide.md):
///  1. Assign the multiplayer peer in PeerCreated, the moment it exists, before any link is up.
///  2. A client may call RPCs only after Multiplayer.ConnectedToServer; a host after Multiplayer.PeerConnected.
///  3. After that, use the multiplayer layer (RPCs, PeerConnected/PeerDisconnected), not the lobby events.
///  4. The node must have the same path on every peer, or RPCs find no target.
/// </summary>
public partial class MinimalSession : Node
{
    private P2PLobby _lobby = null!;
    private bool _isHost;
    private string _name = "player";
    private double _elapsed;
    private bool _done;

    public override void _Ready()
    {
        string url = Arg("url", "http://127.0.0.1:8099");
        string bucket = Arg("bucket", "game");
        string version = Arg("version", "1.0.0");
        _name = Arg("name", "player");
        string? code = Arg("join", "");
        _isHost = string.IsNullOrEmpty(code);

        _lobby = new P2PLobby(url, bucket, version) { PlayerName = _name };
        AddChild(_lobby); // it must be in the scene tree: it does its work in _Process

        // Rule 1: attach the peer right away.
        _lobby.PeerCreated += peer => Multiplayer.MultiplayerPeer = peer;

        // Rules 2 and 3: from here on the multiplayer layer tells you when a link is usable.
        Multiplayer.ConnectedToServer += () =>
        {
            GD.Print("client: linked to the host");
            RpcId(1, MethodName.RpcHello, _name); // a client talks to the host, which is always peer 1
        };
        Multiplayer.PeerConnected += id => GD.Print($"peer {id} connected");
        Multiplayer.PeerDisconnected += id =>
        {
            GD.Print($"peer {id} disconnected");
            if (_isHost)
                Finish(true, "host saw the client leave");
        };
        Multiplayer.ServerDisconnected += () => Finish(false, "the host went away");

        // The lobby's own events are for the room, not for the link.
        _lobby.CodeReady += c => GD.Print($"CODE {c}"); // show this to the other players
        _lobby.Failed += (c, m) => Finish(false, $"{c}: {m}");
        _lobby.RoomClosed += reason => { if (!_done) Finish(false, $"room closed: {reason}"); };
        _lobby.SignalingLost += reason => GD.Print($"signaling lost ({reason}); linked peers keep playing");

        if (_isHost)
            _lobby.CreateRoom();
        else
            _lobby.JoinRoom(code!);
    }

    public override void _Process(double delta)
    {
        _elapsed += delta;
        if (!_done && _elapsed > 30)
            Finish(false, "timed out");
    }

    // The host sends nothing before a client has said hello, so there is no race with the client's own setup.
    [Rpc(MultiplayerApi.RpcMode.AnyPeer)]
    private void RpcHello(string playerName)
    {
        if (!Multiplayer.IsServer())
            return;
        int sender = Multiplayer.GetRemoteSenderId();
        GD.Print($"host: hello from {playerName} (peer {sender})");
        RpcId(sender, MethodName.RpcWelcome, $"welcome, {playerName}");
    }

    // Authority is the default RPC mode: only the host (peer 1) may call this.
    [Rpc]
    private void RpcWelcome(string text)
    {
        GD.Print($"RPC OK - client received: {text}");
        Finish(true, "round trip complete");
    }

    private void Finish(bool ok, string message)
    {
        if (_done)
            return;
        _done = true;
        GD.Print(ok ? $"MINIMAL OK - {message}" : $"MINIMAL FAILED - {message}");
        _lobby.Leave();
        // Quit on the next frame so the leave message goes out first.
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
