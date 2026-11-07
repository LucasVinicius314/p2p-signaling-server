# P2PSignaling.Godot

C# client for [p2p-signaling-server](../../README.md), for Godot 4.7 (.NET). It connects to the
signaling server, creates or joins a room by code, sets up the WebRTC links and gives you a ready
`WebRTCMultiplayerPeer` for Godot's normal multiplayer (RPCs, authority, spawners).

## Install

The package is published to GitHub Packages. GitHub's NuGet registry needs a login even to
download, so each machine (and CI) needs a token with `read:packages` once.

1. Add a source in your game's `nuget.config` (next to the `.csproj`/`.sln`):

   ```xml
   <?xml version="1.0" encoding="utf-8"?>
   <configuration>
     <packageSources>
       <add key="github-p2p" value="https://nuget.pkg.github.com/<OWNER>/index.json" />
     </packageSources>
   </configuration>
   ```

2. Store your credentials in your **user-level** NuGet config (never commit a token):

   ```
   dotnet nuget update source github-p2p --username <GITHUB_USERNAME> --password <TOKEN> --store-password-in-clear-text --configfile "%APPDATA%\NuGet\NuGet.Config"
   ```

   `<OWNER>` is the GitHub user or organization that publishes the package (the owner of this
   repository). `<TOKEN>` is a GitHub personal access token (classic) with the `read:packages` scope. With the
   GitHub CLI: `gh auth refresh -s read:packages` and use `gh auth token`.

3. Reference the package in the game's `.csproj`:

   ```xml
   <PackageReference Include="P2PSignaling.Godot" Version="0.1.0" />
   ```

In GitHub Actions, add a step before restore:
`dotnet nuget add source https://nuget.pkg.github.com/<OWNER>/index.json -n github-p2p -u <GITHUB_USERNAME> -p ${{ secrets.PACKAGES_TOKEN }} --store-password-in-clear-text`
(the default `GITHUB_TOKEN` can read packages from the same repo, but a game in another repo needs a PAT secret).

The library depends on `GodotSharp` 4.7.2, which your game already has through `Godot.NET.Sdk`.

## WebRTC on desktop

Godot ships WebRTC as a native extension, and it is **not** part of the NuGet package. Desktop
exports (Windows, Linux, macOS) need the `webrtc-native` extension in the game project:

1. Download `godot-extension-webrtc_native.zip` from
   <https://github.com/godotengine/webrtc-native/releases> (1.2.2-stable was tested for this library).
2. Extract it so you have `res://addons/webrtc_native/webrtc_native.gdextension`.
3. Commit it, or have your CI download it before the export step.

Web exports have WebRTC built in and need nothing.

If the extension is missing, `Failed` fires with the code `webrtc_unavailable`.

## Use

```csharp
using P2PSignaling;

public partial class Lobby : Node
{
    private P2PLobby _lobby = null!;

    public override void _Ready()
    {
        _lobby = new P2PLobby("https://p2p.example.com", "my-game", "1.0.0") { PlayerName = "Player" };
        AddChild(_lobby);                       // it must be in the scene tree

        _lobby.Connected += peer => Multiplayer.MultiplayerPeer = peer;   // do this right away
        _lobby.CodeReady += code => GD.Print($"Share this code: {code}");
        _lobby.Failed += (code, message) => GD.PrintErr($"{code}: {message}");
        _lobby.RoomClosed += reason => GD.Print($"Room closed: {reason}");
    }

    public void Host() => _lobby.CreateRoom();
    public void Join(string code) => _lobby.JoinRoom(code);
}
```

After `Connected` the game is ordinary Godot multiplayer. The host is peer id 1.

### Events

| Event | When |
|---|---|
| `CodeReady(code)` | host: the room exists, show this code |
| `Connected(peer)` | assign `peer` to `Multiplayer.MultiplayerPeer`. Host: right after the room exists. Client: when the link to the host is up |
| `PeerConnected(id)` / `PeerDisconnected(id)` | a peer's data channels opened or closed |
| `JoinPending()` | client: waiting for the host's approval |
| `JoinRequest(id, name)` | host, in approval mode: answer with `Approve(id, true/false)` |
| `JoinCancelled(id)` | host: a pending request was withdrawn or timed out |
| `RoomClosed(reason)` | `host_left`, `idle_timeout`, `closed_by_admin`, `bucket_deleted` |
| `Failed(code, message)` | a server error code (`room_not_found`, `locked_out`, ...) or a local one (`connect_timeout`, `webrtc_unavailable`, `signaling_closed`) |

Other methods: `SetLocked(bool)` (host), `Leave()`.

### Notes

- **Assign the peer in the `Connected` handler.** Until a peer is assigned to `Multiplayer`, the
  lobby polls it itself; after that Godot does.
- The signaling socket stays open after connecting and sends a keepalive every 30 s, which keeps the
  room joinable for latecomers (the server closes rooms with no activity for the bucket's
  `room_idle_timeout_seconds`). Call `Leave()` when the game session ends.
- Topology (star or mesh) comes from the bucket; the library picks the right Godot setup.
- Everything runs on the main thread through `_Process`; there are no threads to manage.
- Godot has no C# bindings for `WebRTCPeerConnection`/`WebRTCMultiplayerPeer`, so the library drives
  them by name. `lobby.Peer` is typed as `MultiplayerPeer` (the real object is a `WebRTCMultiplayerPeer`).

## Loopback example

`example/` is a Godot project where one process hosts and joins its own room through a running
server and sends a packet across the WebRTC link. Set `P2P_URL`, `P2P_BUCKET` (and `P2P_VERSION`),
add `addons/webrtc_native`, then:

```
Godot_v4.7.2-stable_mono_win64_console.exe --headless --path clients/godot-csharp/example --build-solutions --quit
Godot_v4.7.2-stable_mono_win64_console.exe --headless --path clients/godot-csharp/example
```

It prints `LOOPBACK OK` and exits 0 on success.

## Publishing (maintainers)

Push a tag named `godot-csharp-v<version>` (for example `godot-csharp-v0.1.0`); the
`publish-godot-csharp` workflow packs and pushes the package to GitHub Packages using the
repository's `GITHUB_TOKEN`. The version comes from the tag.
