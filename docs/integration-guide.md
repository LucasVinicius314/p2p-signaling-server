# Integration guide: p2p multiplayer in a Godot C# game

This is the one place that explains how to use p2p-signaling-server from a game. Other docs point here instead of
repeating it. It is written for developers and for coding agents: the steps are ordered, each has a "done when", and
the traps are listed with the symptom you will see.

Contents: [1 The picture](#1-the-picture) · [2 Checklist](#2-checklist) · [3 Add the game to the server](#3-add-the-game-to-the-server)
· [4 Add the library to the game](#4-add-the-library-to-the-game) · [5 The WebRTC native extension](#5-the-webrtc-native-extension)
· [6 Wire the session](#6-wire-the-session-the-part-that-must-be-exactly-right) · [7 Decide what to replicate](#7-decide-what-to-replicate)
· [8 Build, export, CI](#8-build-export-ci) · [9 Verify it](#9-verify-it) · [10 Troubleshooting](#10-troubleshooting)
· [11 Reference implementation](#11-reference-implementation-lightspeed)

## 1. The picture

```
Host game                p2p-signaling-server                 Client game
   |-- hello, create_room ------->|                                 |
   |<-- room_created {code} ------|        (host shows the code)    |
   |                              |<------ hello, join_room {code} -|
   |<-- peer_joined --------------|------- joined ----------------->|
   |<======= offer / answer / ICE candidates, relayed =============>|
   |<================ direct WebRTC data channels ================>  |
```

- The server only **introduces** players. After the introduction all game traffic is direct, peer to peer. If the
  server restarts, running games continue; only new joins fail for a few seconds.
- One player **hosts** (peer id 1). Everyone else is a **client** and connects to the host (a star, the default). Godot's
  normal multiplayer (RPCs, authority) runs on top of the links, with the host as the server.
- A game is a **bucket** on the server: its limits, the versions it accepts, its ICE servers. One bucket per game.

You need four things: a bucket on the server, the library's source compiled into your game, the WebRTC native
extension, and your own session code (wiring plus the decision of what to replicate).

## 2. Checklist

Do these in order. Each step says how to know it worked.

1. **Create the bucket** on the server (section 3). *Done when* `get_bucket` shows it.
2. **Add the library** as a git submodule and compile its sources into the game assembly (section 4). *Done when* the game
   builds and `new P2PSignaling.P2PLobby(...)` resolves.
3. **Add the WebRTC native extension** (section 5). *Done when* `ClassDB.ClassExists("WebRTCMultiplayerPeer")` is true on
   desktop.
4. **Wire the session** exactly as in section 6. *Done when* two local processes exchange an RPC (section 9; the shipped
   example does this and passes).
5. **Decide what to replicate** and build it on top (section 7).
6. **Set up build, export and CI** (section 8).
7. **Verify** against the local dev server, then the real one (section 9).

## 3. Add the game to the server

Each game needs a bucket before it can connect. The game passes the bucket id when it connects.

Create it with the MCP tool `create_bucket` or the REST API. Admin access needs the master key (`Authorization: Bearer
<key>`).

```
curl -X POST https://<server>/admin/v1/buckets \
  -H "Authorization: Bearer $MASTER_API_KEY" -H "Content-Type: application/json" -H "User-Agent: admin-script/1.0" \
  -d '{"id":"my-game","name":"My Game","max_players":8}'
```

Send a `User-Agent`: the live server sits behind Cloudflare, which rejects the default Python and some library user agents
with `error code: 1010`. MCP clients are not affected.

| Field | Meaning | Notes |
|---|---|---|
| `id` | the bucket id the game passes | lowercase letters, digits and `-`, 3 to 40 characters; cannot be changed later |
| `name` | display name | required |
| `accepted_versions` | deprecated, ignored | the server never checks the game version |
| `max_players` | players per room | 2 to 16 (mesh: 2 to 8) |
| `topology` | `star` (everyone connects to the host) or `mesh` (everyone to everyone) | use `star` unless you need peer to peer between clients |
| `code_length` | characters in a room code | 4 to 8; default 6 |
| `host_approval` | the host must accept each joiner | the game handles `JoinRequest` and calls `Approve` |
| `relay_only` | force TURN, hiding players' IP addresses from each other | needs a `turn:` entry in `ice_servers` |
| `ice_servers` | STUN and TURN servers sent to players | default: Google's public STUN. Add TURN for players whose NAT blocks direct links |
| `allowed_origins` | browser origins allowed to connect | only for Web exports, for example `https://play.example.com` |
| `limits` | abuse limits (joins per address per minute, wrong-code lockouts, message size and rate, ...) | defaults are sensible; see [protocol.md](protocol.md#error-codes) |

Everything is validated as a whole on every change. Useful follow-ups:

- `update_bucket` changes fields (the new config applies to new connections at once).
- `set_bucket_enabled` is a kill switch for a game.
- `get_stats` and `list_rooms` show what is happening live.

**The version string.** Decide how the game produces it and keep it consistent with the bucket. Lightspeed reports the
release tag without its leading `v` (read from a `version.txt` next to the executable), and `dev` when run from source.
It is informational only: the server accepts any string and never rejects a player over it.

For local development you do not need the real server: see section 9 (`go run ./cmd/devserver`).

## 4. Add the library to the game

> **The rule: compile the library's source files into your game's own assembly.**
> Do **not** reference `P2PSignaling.csproj` (or any compiled copy) as a separate project or package.

Why: Godot only runs script classes, and their `_Process` and other callbacks, from the game's own assembly. A
`P2PLobby` compiled into a different assembly is created without error and then does nothing at all: no connection, no
events, no failure. There is no error to search for. (The library's own example behaved exactly this way until it was
changed to compile the sources in.)

Steps:

1. Add the repository as a submodule:
   ```
   git submodule add https://github.com/LucasVinicius314/p2p-signaling-server libs/p2p-signaling-server
   ```
2. Compile its sources into the game, and keep everything else of the submodule out of the build. In the game's `.csproj`:
   ```xml
   <ItemGroup>
     <Compile Remove="libs/**" />
     <Compile Include="libs/p2p-signaling-server/clients/godot-csharp/src/**/*.cs" />
   </ItemGroup>
   ```
   The `Remove` matters: without it the SDK's default globbing also compiles the library's example project and tests,
   and the library twice.
3. Create an empty file `libs/.gdignore` so Godot does not scan the submodule as game resources.
4. Anyone cloning the game needs `git clone --recurse-submodules` (or `git submodule update --init` afterwards). CI must
   check out submodules too (`actions/checkout` with `submodules: true`).
5. Update the library with `git submodule update --remote libs/p2p-signaling-server`, then commit the new pointer.
   **Never edit files under `libs/` from the game repository**: changes go to the server repository and come back through
   the submodule.

Requirements: Godot 4.x with .NET (tested with 4.7.2), `net8.0`. The library files declare every `using` they need, so the
game does not have to enable implicit usings.

Alternative without a submodule: copy the three files in `clients/godot-csharp/src/` into the game. That works the same way
but you update them by hand.

## 5. The WebRTC native extension

Godot ships WebRTC as a native extension that is not part of the engine's desktop builds.

- **Desktop (Windows, Linux, macOS):** the game needs `addons/webrtc_native` (the `webrtc-native` extension; 1.2.2-stable
  was tested). Without it the lobby raises `Failed` with `webrtc_unavailable`.
- **Web exports** have WebRTC built in and need nothing.
- The extension is about 90 MB of binaries, so do not commit it. Download it with a script that pins the release and
  verifies its SHA-256 (a working pair, `scripts/fetch-webrtc.sh` and `.ps1`, is in the Lightspeed repository, see
  section 11), git-ignore `addons/webrtc_native/`, run the script once after cloning, and run it in CI before exporting.
- An exported desktop build places the native library next to the executable (for example
  `libwebrtc_native.windows.template_release.x86_64.dll`). If a build lacks it, multiplayer fails with
  `webrtc_unavailable`. Make the build script refuse to run when the extension is missing.

## 6. Wire the session (the part that must be exactly right)

The complete, tested example is [`clients/godot-csharp/example/MinimalSession.cs`](../clients/godot-csharp/example/MinimalSession.cs).
It is compiled by CI and was run for real (a host process and a client process exchange an RPC). Copy its wiring. This
section explains it.

### The order of events

| Step | Host | Client |
|---|---|---|
| 1 | `lobby.CreateRoom()` | `lobby.JoinRoom(code)` |
| 2 | `PeerCreated(peer)`: **assign it to `Multiplayer.MultiplayerPeer` now** | `PeerCreated(peer)`: **assign it now** |
| 3 | `CodeReady(code)`: show the code to the player | |
| 4 | `Multiplayer.PeerConnected(id)`: a client's link is up | `Multiplayer.ConnectedToServer`: the host link is up |
| 5 | wait for the client's first RPC | send the first RPC to the host (`RpcId(1, ...)`) |
| 6 | `Multiplayer.PeerDisconnected(id)` when a client leaves | `Multiplayer.ServerDisconnected` if the host goes away |

The six rules behind it:

1. **Assign the multiplayer peer in `PeerCreated`, never later.** The event fires as soon as the peer exists, before any
   link is up. Godot's multiplayer layer must be attached before a peer connects; one attached afterwards is never
   registered and its packets are dropped (`!connected_peers.has(sender)` errors; RPCs never arrive on a client). The
   `Connected` event fires too late for this. It is fine for raw packets, not for RPCs.
2. **A client may send RPCs only after `Multiplayer.ConnectedToServer`;** a host only to peers it has seen in
   `Multiplayer.PeerConnected`. Sending earlier fails.
3. **After the link is up, use the multiplayer layer** (RPCs, `PeerConnected`, `PeerDisconnected`), not the lobby events. The
   lobby's events (`CodeReady`, `Failed`, `RoomClosed`, `SignalingLost`, `JoinRequest`) are about the room.
4. **The node that declares the RPCs must have the same path on every peer.** The simplest way is a single autoload (for
   example `/root/NetSession`). A different path means RPCs find no target.
5. **Client to host RPCs use `RpcMode.AnyPeer`;** host to client RPCs use the default `Authority` mode (only the host,
   peer 1, may call them). On the host, check `Multiplayer.IsServer()` and validate `Multiplayer.GetRemoteSenderId()`; on
   a client, check that the sender is 1. Never trust a client's values: bounds-check and rate-limit them.
6. **The lobby node must be in the scene tree.** It does its work in `_Process`.

Peer ids: the host is always 1; the others are random positive numbers (not consecutive). Do not assume an order.

### What each lobby event means

| Event | Meaning and what to do |
|---|---|
| `PeerCreated(peer)` | attach it to `Multiplayer` right now (rule 1) |
| `CodeReady(code)` | host: the room exists; show the code (out-of-band sharing is up to the game) |
| `Failed(code, message)` | the attempt failed; show `message`, tear down and allow a retry. Codes are in the troubleshooting table |
| `RoomClosed(reason)` | the room is gone (`host_left`, `idle_timeout`, `closed_by_admin`, `bucket_deleted`); end the session |
| `SignalingLost(reason)` | the signaling connection ended after you were linked (`server_restarting`, `connection_closed`). Linked peers keep playing, but a host's code is dead and nobody new can join: tell the host and offer to create a new room |
| `JoinPending` / `JoinRequest(id, name)` / `JoinCancelled(id)` | only with `host_approval`: the host answers with `Approve(id, true/false)` |
| `PeerConnected(id)` / `PeerDisconnected(id)` | data channels opened or closed (the multiplayer layer's equivalents are preferred) |

### Leaving

Call `lobby.Leave()`, set `Multiplayer.MultiplayerPeer = null`, free the lobby node, and clear any state that belonged to
the session. A client whose host disappears should end the session and carry on offline.

### Things the library does for you

- Picks the Godot setup from the bucket's topology; applies the "higher peer id creates the offer" rule; trickle ICE.
- Sends a keepalive every 30 s so the room is not closed as idle while a host is connected.
- Times out a link that does not come up (20 s) with `Failed` / `connect_timeout`.

### Friends: see your friends' rooms without a code

Optional. Every machine gets an id the first time any game using the library asks for it, kept in
`~/.p2p-signaling-server/id` (shared by all games, so a friend added once is seen in every game). Players give their id to
friends and put friends' ids, one per line, in `~/.p2p-signaling-server/friends` (blank lines and `# notes` are ignored;
the library creates the file with instructions). Then:

```csharp
lobby.ListForFriends = true;      // host: make my rooms visible to everyone who has my id (before CreateRoom)
lobby.FriendRoomsReceived += rooms => { /* FriendRoom: Code, FriendLabel, HostName, Players, MaxPlayers, AgeSeconds */ };
lobby.ListFriendRooms();          // friend: ask which of my friends are hosting; call again to refresh
// then: lobby.JoinRoom(room.Code)
```

Show the player their own id with `P2PIdentity.OwnId` (a "copy my id" button), and warn about
`lobby.FriendFileProblems` (a mistyped id is reported with its line number). Ids carry a checksum, so a typo is caught
instead of silently following nobody. The id is a secret you share with friends only: anyone who has it can list your rooms,
and the only way to take it back is to delete the `id` file (a new id) and tell your friends. The server only ever sees a
hash of it. The folder can be moved with the environment variable `P2P_SIGNALING_HOME` (handy to run two players on one
machine). Details and limits are in [protocol.md](protocol.md#friends-listing-rooms-to-people-who-know-you). Working
code: [`example/FriendsSession.cs`](../clients/godot-csharp/example/FriendsSession.cs).

### Which server: the `server` file

The library has no built-in address. The first time a game connects with `lobby.Url` set, the library saves it to
`~/.p2p-signaling-server/server` (one line; `#` comments allowed) unless that file already exists, so a choice the user made
is never overwritten. Later, any game that leaves `Url` empty uses it, so players and developers point every game at another
server (for example `http://127.0.0.1:8099` for `cmd/devserver`) by editing one file. Order: `lobby.Url` if set, else the
environment variable `P2P_SIGNALING_URL`, else the file. With none of them, `Failed` fires with `no_server_url`. The file is
read at connect time.

## 7. Decide what to replicate

The library gives you a connected Godot multiplayer peer; what the game sends over it is yours. A pattern that worked
well (it is what Lightspeed does, section 11):

- **The host is authoritative.** It runs the simulation and owns shared settings. Clients send their **input or pose**
  and **requests** (ask the host to change a shared setting, to spawn something); the host validates, applies and tells
  everyone.
- **Per-frame data goes unreliable, events go reliable.** Poses and snapshots: about 30 per second with
  `TransferMode.Unreliable` (a lost one is replaced by the next). Spawn, despawn, roster, settings changes: reliable.
  Keep an unreliable message under about 16 KB; split big snapshots.
- **Clients follow the host's bodies as puppets:** freeze the client's local physics for networked bodies and move them
  from snapshots, easing toward the newest pose.
- **Spawned objects are announced.** The host assigns an id and tells clients to create a puppet; despawns are announced
  too. Objects that are part of the scene get stable ids (for example sorted by node name) on both sides.
- **Deterministic motion needs a shared clock,** not replication: if scripted objects are a function of time, give every
  peer the host's time (a world-time offset that clients adopt and nudge) instead of sending their positions.
- **Measure latency** with a small ping RPC (stamp, bounce, read the clock). It is the number players care about.
- **A client's inputs that change the world go through requests,** so the host stays the single source of truth.

## 8. Build, export, CI

- **Submodules in CI:** `actions/checkout` with `submodules: true` (both repositories are public, no token needed).
- **The native extension:** run your fetch script before the export step; fail the build if `addons/webrtc_native` is
  missing.
- **Version string:** produce it the same way in every build (for example from the release tag).
- **Server restarts and redeploys:** handle `SignalingLost` and `Failed` / `server_restarting` (`RetryAfterSeconds` says
  how long to wait).

## 9. Verify it

Always run the real thing. Compiling proves nothing here: the separate-assembly trap compiles perfectly and does nothing.

**Local server.** The dev server keeps buckets in memory, needs no database and runs the same WebSocket handler:

```
go run ./cmd/devserver -bucket my-game        # listens on 127.0.0.1:8099
```

**The shipped example** (host in one terminal, client in another; add `addons/webrtc_native` to the example project first):

```
godot --headless --path clients/godot-csharp/example --build-solutions --quit
godot --headless --path clients/godot-csharp/example res://MinimalSession.tscn -- --host --name=Hosty         # prints CODE ...
godot --headless --path clients/godot-csharp/example res://MinimalSession.tscn -- --join=<CODE> --name=Joey
```

Expected: the host prints `CODE`, `hello from Joey`, and `MINIMAL OK`; the client prints `RPC OK - client received: welcome,
Joey` and `MINIMAL OK`; both exit 0. `LOOPBACK OK` from the other example (`res://Loopback.tscn` run with `P2P_URL` and
`P2P_BUCKET` set) checks the raw link.

**Your own game.** Add command-line hooks to start hosting or joining and to print a state report, so two headless
processes can be compared automatically. Lightspeed's flags are a template (`--net-host`, `--net-join=CODE`,
`--net-report-paused`, ...; see its `CLAUDE.md`). A good check is to freeze the host's simulation, let the client settle,
and compare both reports line by line.

**The real server.** Point the game at it and use the MCP tools `list_rooms` and `get_stats` to watch the room and the
counters while you test. A wrong code, a wrong version and a missing server are worth trying: each should show a clear
message.

## 10. Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| Nothing happens: no events, no `Failed`, nothing on the server | the library was compiled as a separate assembly (`ProjectReference` or a package), so Godot never runs `P2PLobby._Process` | compile the sources into the game assembly (section 4) |
| Client's RPCs never arrive; engine errors `!connected_peers.has(sender)` | the multiplayer peer was assigned after it connected | assign it in `PeerCreated` (rule 1) |
| RPC fails with "node not found" or reaches nobody | the RPC node has a different path on the two peers | use one autoload at a fixed path (rule 4) |
| An RPC sent right after `PeerCreated` or `JoinRoom` fails | the link is not up yet | send after `ConnectedToServer` (client) or `PeerConnected` (host) (rule 2) |
| `Failed: webrtc_unavailable` | the WebRTC native extension is missing from the project or the export | section 5 |
| `Failed: bucket_not_found` / `bucket_disabled` | wrong bucket id or server, or the bucket is switched off | check the id; `get_bucket` |
| `Failed: room_not_found` | wrong code, or the room closed (the server answers the same for both) | check the code; the host may have left |
| `Failed: room_full` / `room_locked` | the room is at `max_players`, or the host locked it | |
| A friend's room is not in the list | the friend's id is not on a line of your `friends` file (or has a typo: see `FriendFileProblems`), they did not set `ListForFriends` before `CreateRoom`, the room is locked or full, or it is in another bucket | check the file and the flag; the list only shows joinable rooms of the bucket you are connected to |
| `Failed: bad_message` after `CreateRoom` with `ListForFriends`, or after `ListFriendRooms` | the server is older than the friends feature | redeploy the server |
| `Failed: rate_limited` / `locked_out` | too many joins or wrong codes from this address, or from everyone on this bucket | wait; if every player is limited together, the server is not seeing real client addresses (server operator: `TRUSTED_PROXY_CIDRS`, see [deploy-caprover.md](deploy-caprover.md)) |
| `Failed: connect_timeout` | the direct link did not come up (a strict NAT or a firewall; only STUN is configured) | test on one network; add a TURN server to the bucket's `ice_servers` |
| `Failed: signaling_closed` before linking | the server is unreachable, or restarted in the middle | retry |
| `Failed: server_restarting` | the server is being redeployed | retry after `RetryAfterSeconds` |
| `Failed: origin_rejected` (Web) | the page's origin is not in `allowed_origins` | add it |
| `Failed: no_server_url` | `lobby.Url` is empty and neither `P2P_SIGNALING_URL` nor `~/.p2p-signaling-server/server` holds an address | set `Url` in the game, or write the address into that file |
| `Failed: connect_failed` | the server address is malformed or unreachable | check the URL (`https://` becomes `wss://`) |
| Engine error in `remove_peer` when a player leaves | an old library version | update the submodule (fixed) |
| Everything builds twice, or duplicate-type errors | the submodule's own projects are being compiled | add `<Compile Remove="libs/**" />` (section 4) |
| `403`, `error code: 1010` from the admin API | Cloudflare rejected the client's user agent | send a `User-Agent` header (section 3) |

## 11. Reference implementation: Lightspeed

[Lightspeed](https://github.com/LucasVinicius314/lightspeed) is a full game built on this, with a host-authoritative world.
It is the best example of sections 6 and 7 in real use. All paths are in that repository.

| Concept | Where |
|---|---|
| The session: lobby, roles, every RPC, snapshots, puppets, roster | `src/Net/NetSession.cs` (an autoload) |
| Compact binary encoding for the unreliable messages | `src/Net/NetCodec.cs` |
| Other players drawn from network poses | `src/Net/RemoteAvatar.cs` |
| Shared settings applied on the host, requested by clients | `src/Core/SimClock.cs` (the setters turn into requests on a client) |
| Shared world clock so scripted motion matches | `SimClock.WorldTime` and `WorldOffset`; movers use `WorldTime` |
| Host-side spawning announced to clients; a client's throw asked of the host | `src/Demo/Spawner.cs`, `src/Demo/Thrower.cs` |
| The native extension fetch (pinned and hash-checked) | `scripts/fetch-webrtc.sh` and `.ps1` |
| Submodule setup, compile include, `.gdignore` | `Lightspeed.csproj`, `libs/` |
| Scripted two-process verification | the `--net-*` flags, described in its `CLAUDE.md` |

How Lightspeed answers the design questions of section 7: the host runs the physics; clients send their camera pose and
requests; the host sends 30 snapshots a second (players and bodies) and reliable spawn, despawn, roster and settings
messages; scene bodies get ids from their sorted names and spawned bodies get ids from a counter; a client's physics
bodies are frozen puppets; time-based motion follows a world time that clients adopt from the host; and a ping RPC every
250 ms measures latency.
