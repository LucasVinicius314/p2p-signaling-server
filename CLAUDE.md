# p2p-signaling-server

A Go WebRTC **signaling and lobby server** shared by many Godot games, plus the C# client library games use, plus the
docs for both. Players find each other by a short room code and then play peer to peer; the server never carries game
traffic. Each game is a **bucket** (a config document in MongoDB Atlas). The first game is Lightspeed
(<https://github.com/LucasVinicius314/lightspeed>); the live server is `https://p2p.suresure.me` (CapRover behind Cloudflare).

## Where things are

| You want to | Read |
|---|---|
| Add multiplayer to a game, add a game to the server, fix a connection problem | [docs/integration-guide.md](docs/integration-guide.md) (the single full guide) |
| Know exactly what goes over the WebSocket | [docs/protocol.md](docs/protocol.md) |
| Deploy or operate the server (CapRover, proxies, logs, restarts) | [docs/deploy-caprover.md](docs/deploy-caprover.md) |
| Use the client library's API (events, methods) | [clients/godot-csharp/README.md](clients/godot-csharp/README.md) |
| Run, administer and understand the server | [README.md](README.md) |

**Do not repeat integration instructions elsewhere; link to the guide.** Copies drift apart and then mislead.

## Layout

`cmd/p2p-signaling-server` the server. `cmd/devserver` an in-memory local server for testing games. `internal/` the server
packages (`bucket`, `room`, `signaling`, `admin`, `mcpserver`, `store`, ...; see the README). `clients/godot-csharp/src` the
client library (three files). `clients/godot-csharp/example` a Godot project that exercises it (`Loopback`, `MinimalSession`).
`test/e2e` end-to-end tests with real WebSocket clients.

## Commands

- `go vet ./...` and `go test -race ./...` (no network needed) and `go run honnef.co/go/tools/cmd/staticcheck@latest ./...`.
- `gofmt -l .` must print nothing. `go mod tidy` must leave `go.mod`/`go.sum` unchanged. CI also runs `govulncheck` (on the
  latest Go patch release), a Docker build, and the Mongo store tests with `-tags mongo` against a MongoDB container.
- Local server for development: `go run ./cmd/devserver -bucket game -versions 1.0.0,dev` (127.0.0.1:8099).
- Client library: `dotnet build clients/godot-csharp/P2PSignaling.csproj -c Release` (a type check only, see below).
- Example project: `<godot> --headless --path clients/godot-csharp/example --build-solutions --quit`, then run it as described
  at the top of `example/MinimalSession.cs`. It needs `addons/webrtc_native` in the example project (git-ignored; copy it from
  a game or download `godot-extension-webrtc_native.zip`, 1.2.2-stable, from the godotengine/webrtc-native releases).

## Rules

- **The client library must work when compiled into a game's own assembly, and only then.** Godot runs script classes and their
  `_Process` only from the game's assembly, so a separately compiled `P2PLobby` silently does nothing. `P2PSignaling.csproj`
  exists to type-check the sources in CI; it is not how games consume them, and nothing may recommend a `ProjectReference` or a
  package. The sources must declare every `using` they need (consumers may not enable implicit usings).
- **A client library change is not verified by compiling.** Run the example for real (host and client, two processes) and see
  `LOOPBACK OK` and `MINIMAL OK`. That is how the "assign the peer in `PeerCreated`" bug and the separate-assembly trap were
  found; both compiled fine.
- **Keep three things in step when the protocol changes:** `docs/protocol.md`, the server (`internal/signaling`), and the
  client (`clients/godot-csharp/src`), plus an e2e test. The protocol is versioned (`protocol` in `hello`); do not break old
  clients without bumping it.
- **Docs have one home each** (table above). If you change behaviour that games must know about, update the integration guide
  (including its troubleshooting table) rather than adding text elsewhere.
- **Security rules that the tests enforce; keep them:** a wrong code looks like a missing room; join limits are applied before
  the code is looked at; client addresses are limited by IPv4 address or IPv6 `/64`; ICE credentials are masked in everything an
  admin sees (REST, MCP, audit log); player-supplied text is never returned through MCP; join codes, addresses, names and
  payloads are never logged; request logs use the route pattern, not the path (paths can hold codes).
- **Concurrency:** buckets carry a `revision`; updates are applied to the revision they were based on. `Service.Refresh` is
  serialized. Never decode into a cached bucket (use `Clone`).
- **Secrets:** `master-api-key`, `deploy-key`, `deploy-key.pub` and `mongo-uri` sit in the repository folder and are git-ignored.
  Never commit, print or paste them. The server refuses to start with a `MASTER_API_KEY` shorter than 32 characters.
- **The live server:** it is behind Cloudflare, which rejects default library user agents on the admin API (`error code: 1010`);
  send a `User-Agent`. It needs `TRUSTED_PROXY_CIDRS` (CapRover's network plus Cloudflare's ranges) or every player looks like
  one address. Admin access, MCP included, is one master key with full write power.
- Both repositories are public, so a game can take this one as a submodule and CI needs no token to check it out.
