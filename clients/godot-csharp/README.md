# P2PSignaling.Godot

C# client for [p2p-signaling-server](../../README.md), for Godot 4 (.NET, tested with 4.7.2). It connects to the signaling
server, creates or joins a room by code, sets up the WebRTC links and gives you a ready `WebRTCMultiplayerPeer` for
Godot's normal multiplayer (RPCs, authority).

**How to add it to a game, wire it up and avoid the traps is in the
[integration guide](../../docs/integration-guide.md). Start there.** This page is the API reference only.

The two rules that matter most, because breaking either one fails silently:

1. **Compile these sources into the game's own assembly** (a git submodule plus a `<Compile Include=...>`; never a
   `ProjectReference` or package), or the lobby does nothing at all.
2. **Assign the multiplayer peer in `PeerCreated`**, before any link is up, or a client's RPCs are dropped.

Working code that does it right: [`example/MinimalSession.cs`](example/MinimalSession.cs).

## API

`P2PLobby` is a `Node`. Create it, `AddChild` it, subscribe to the events, then call `CreateRoom()` or `JoinRoom(code)`.

```csharp
var lobby = new P2PLobby("https://p2p.example.com", "my-game", "1.0.0") { PlayerName = "Player" };
AddChild(lobby);
```

| Constructor argument / property | Meaning |
|---|---|
| `Url` | server base address; `http(s)://` becomes `ws(s)://`. A url set here is saved to `~/.p2p-signaling-server/server` if none exists; empty uses `P2PIdentity.ServerUrl()` (`P2P_SIGNALING_URL`, else that file, else `Failed(no_server_url)`; see the guide's [server file](../../docs/integration-guide.md#which-server-the-server-file)) |
| `Bucket` | the game's bucket id on the server |
| `GameVersion` | informational; the server never rejects a version |
| `PlayerName` | optional display name (the host sees it in `JoinRequest`, friends see it in room lists). Empty uses `P2PIdentity.PlayerName()` (`P2P_SIGNALING_NAME`, else `~/.p2p-signaling-server/name`); a name set here is saved there if none exists. When the player types a name, call `P2PIdentity.SavePlayerName(name)` so every game shows it (see the guide's [name file](../../docs/integration-guide.md#who-you-are-the-name-file)). Cut to 32 bytes, as the server does |
| `MaxPlayers` | requested room size for `CreateRoom`; 0 uses the bucket's maximum |
| `ConnectTimeoutSeconds` | how long a link may take to come up before `Failed` (default 20) |

### Methods

`CreateRoom()`, `JoinRoom(code)`, `Approve(peerId, accept)` (host, approval mode), `SetLocked(bool)` (host), `Leave()`,
`ListFriendRooms()` (see [Friends](#friends)).

### Properties

`IsHost`, `PeerId` (1 for the host, 0 until in a room), `RoomCode`, `Peer` (the `MultiplayerPeer`, really a
`WebRTCMultiplayerPeer`), `IsLinked`, `HasSignaling`, `RetryAfterSeconds` (how long a restarting server asked clients to wait).

### Events

| Event | When |
|---|---|
| `PeerCreated(peer)` | **assign `peer` to `Multiplayer.MultiplayerPeer` here** if you use Godot's multiplayer (RPCs). It fires before any link is up, which is what the multiplayer layer needs: a peer assigned after it connected is never registered and its packets are dropped |
| `CodeReady(code)` | host: the room exists, show this code |
| `Connected(peer)` | the peer is linked. Host: right after the room exists. Client: when the link to the host is up. Fine for raw packets (`PutPacket`/`GetPacket`); for RPCs use `PeerCreated` and the `Multiplayer` events |
| `PeerConnected(id)` / `PeerDisconnected(id)` | a peer's data channels opened or closed |
| `JoinPending()` | client: waiting for the host's approval |
| `JoinRequest(id, name)` | host, in approval mode: answer with `Approve(id, true/false)` |
| `JoinCancelled(id)` | host: a pending request was withdrawn or timed out |
| `RoomClosed(reason)` | `host_left`, `idle_timeout`, `closed_by_admin`, `bucket_deleted` |
| `SignalingLost(reason)` | the signaling connection ended after you were linked (`server_restarting` after a redeploy, or `connection_closed`). Existing links keep working, but the server forgot the room: a host should create a new one, since the old code is dead and nobody new can join |
| `FriendRoomsReceived(rooms)` | the answer to `ListFriendRooms()`: a list of `FriendRoom` (`Code`, `FriendLabel`, `HostName`, `Players`, `MaxPlayers`, `AgeSeconds`) |
| `Failed(code, message)` | a server error code (`room_not_found`, `locked_out`, ...) or a local one (`connect_timeout`, `webrtc_unavailable`, `signaling_closed`, `server_restarting`, `connect_failed`). What each means and how to fix it is in the guide's [troubleshooting table](../../docs/integration-guide.md#10-troubleshooting) |

### Friends

Optional: see friends' rooms without being sent a code. `P2PIdentity` (a static class, no Godot types) keeps this machine's id
in `~/.p2p-signaling-server/id` and reads friends' ids, one per line, from `~/.p2p-signaling-server/friends`
(`P2P_SIGNALING_HOME` moves the folder). What the guide's [section 6](../../docs/integration-guide.md#friends-see-your-friends-rooms-without-a-code)
explains, as API:

| Member | Meaning |
|---|---|
| `P2PLobby.ListForFriends` | host: also tag the room so friends can list it (set before `CreateRoom`); off by default |
| `P2PLobby.ListFriendRooms()` | asks for the joinable rooms your friends host; answered through `FriendRoomsReceived` (an empty friends file answers at once, without a request) |
| `P2PLobby.FriendFileProblems` | bad lines found in the friends file the last time it was read |
| `P2PIdentity.OwnId` | this machine's id to give to friends, for example `7H3K-9QWE-...` (created on first use) |
| `P2PIdentity.OwnTag` | its hash, which is what the server receives |
| `P2PIdentity.LoadFriends()` | the parsed friends file (`Friend`: `Id`, `Label`, `Tag`) and its problems |
| `P2PIdentity.TryParseId(text, out canonical)` | validates an id as typed (any case, dashes or spaces optional; the checksum catches typos) |
| `P2PIdentity.IsPersistent` / `OwnProblem` | false and why when the id could not be saved (it then lasts for this run only) |

Working code: [`example/FriendsSession.cs`](example/FriendsSession.cs).

### Behaviour worth knowing

- The signaling socket stays open after linking and sends a keepalive every 30 s, which keeps the room from closing as idle
  (the server closes rooms with no activity for the bucket's `room_idle_timeout_seconds`). Call `Leave()` when the session ends.
- Topology (star or mesh) comes from the bucket; the library picks the right Godot setup.
- Everything runs on the main thread through `_Process`. There are no threads.
- Godot has no C# bindings for `WebRTCPeerConnection` / `WebRTCMultiplayerPeer`, so the library drives them by name.

## Examples

`example/` is a Godot project that compiles these sources into its own assembly, exactly like a game. It needs
`addons/webrtc_native` and a running server (`go run ./cmd/devserver`, see the guide's section 9).

- `Loopback` (the default scene): one process hosts and joins its own room and sends a raw packet. Prints `LOOPBACK OK`.
- `MinimalSession`: host and client as two processes, with an RPC each way. Prints `MINIMAL OK`.
- `FriendsSession`: a host lists its room to friends, a friend finds and joins it, a stranger sees nothing. Prints `FRIENDS OK`. How to run it (it needs a `P2P_SIGNALING_HOME` per player) is at the top of the file.
