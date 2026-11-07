# p2p-signaling-server

A small WebRTC **signaling and lobby server** in Go, shared by any number of games. A game's players use
it to find each other by a short room code and to set up direct peer-to-peer connections. After that the
game's traffic goes straight between the players; the server carries none of it, so a restart or a redeploy
does not interrupt a running game, it only blocks new joins for a few seconds.

Each game is a **bucket**: one config document in MongoDB Atlas (player limit, accepted game versions,
ICE servers, abuse limits, ...). Adding p2p to another game means creating a bucket, not changing code.
Buckets are managed over a REST admin API and over **MCP**, so an AI assistant can set them up and
inspect the server.

## How it works

```
Host game                p2p-signaling-server                 Client game
   |-- hello, create_room ------->|                                 |
   |<-- room_created {code} ------|        (host shows the code)    |
   |                              |<------ hello, join_room {code} -|
   |<-- peer_joined --------------|------- joined ----------------->|
   |<======= offer / answer / ICE candidates, relayed =============>|
   |<================ direct WebRTC data channels ================>  |
```

- The host is peer 1. Joiners connect to the host (star topology, the default) or to everyone (mesh).
- The server only relays the connection set-up messages. It never reads them and never carries game data.
- The host is revealed only to someone who supplies the code. A wrong code and a missing room look the
  same, and join attempts are rate limited and locked out (see [docs/protocol.md](docs/protocol.md)).
- An optional per-bucket **host approval** mode makes the host accept each joiner before any connection
  details are exchanged, and an optional **relay-only** mode hides players' IP addresses from each other
  (it needs a TURN server in the bucket's `ice_servers`).

Full message reference: [docs/protocol.md](docs/protocol.md). The client library for Godot (C#) is in
[clients/godot-csharp](clients/godot-csharp/README.md).

## Using it in a game

**[docs/integration-guide.md](docs/integration-guide.md)** is the one complete guide: how to add a game to the server, add
the library to a Godot C# game, wire the session correctly, decide what to replicate, set up export and CI, verify it, and
fix the problems you are likely to hit. A working example is in
[clients/godot-csharp/example](clients/godot-csharp/example/MinimalSession.cs). For coding agents working in this repository,
see [CLAUDE.md](CLAUDE.md).

## Running it

You need Go (a current 1.26.x patch release) and a MongoDB database (Atlas free tier is fine).

```
export MASTER_API_KEY="$(openssl rand -hex 32)"      # at least 32 characters
export MONGODB_URI="mongodb+srv://user:password@cluster.example.mongodb.net/"
go run ./cmd/p2p-signaling-server
```

| Variable | Required | Meaning |
|---|---|---|
| `MASTER_API_KEY` | yes | the one admin secret, at least 32 characters; the server refuses to start with a shorter one |
| `MONGODB_URI` | yes | MongoDB connection string. The database is always named `main` |
| `PORT` | no | default `8080` |
| `TRUSTED_PROXY_CIDRS` | behind a proxy | networks whose `X-Forwarded-For` is believed; see [docs/deploy-caprover.md](docs/deploy-caprover.md) |
| `LOG_LEVEL` | no | `debug`, `info` (default), `warn`, `error` |

Endpoints: `GET /ws` (players), `GET /health`, `GET /ready`, the admin API under `/admin/v1`, and MCP at `/mcp`.

Deploying on CapRover (WebSocket support, one instance, real client addresses behind the proxy, restarts)
is covered in [docs/deploy-caprover.md](docs/deploy-caprover.md).

## Administering it

Everything admin uses the same key: `Authorization: Bearer <MASTER_API_KEY>`.

```
# create a bucket for a game (unspecified fields get defaults: star, 8 players, public STUN, any version)
curl -X POST https://signal.example.com/admin/v1/buckets \
  -H "Authorization: Bearer $MASTER_API_KEY" -H "Content-Type: application/json" \
  -d '{"id":"my-game","name":"My Game","accepted_versions":["1.0.0"]}'

curl -H "Authorization: Bearer $MASTER_API_KEY" https://signal.example.com/admin/v1/buckets
curl -H "Authorization: Bearer $MASTER_API_KEY" https://signal.example.com/admin/v1/stats
```

Bucket fields are validated as a whole on every change. ICE server credentials are masked in everything the
admin surface returns (REST, MCP and the audit log) and shown to players only in their `welcome` message.
Two edits at the same moment cannot overwrite each other: every bucket has a `revision`, and a conflicting
update is re-applied on the newer one.

### MCP

The server speaks MCP (streamable HTTP) at `/mcp` with the same bearer key. With Claude Code:

```
claude mcp add --transport http p2p-signaling https://signal.example.com/mcp \
  --header "Authorization: Bearer <MASTER_API_KEY>"
```

Tools: `list_buckets`, `get_bucket`, `create_bucket`, `update_bucket`, `validate_bucket` (dry run),
`set_bucket_enabled`, `delete_bucket` (needs `confirm` equal to the id), `list_rooms`, `close_room`,
`get_stats`, `get_health`, `get_audit_log`. No tool returns player-supplied text. The key gives full read and
write access, MCP included, so keep it secret.

## Abuse limits (per bucket)

Joining by code is the one thing an attacker can brute force, so it is limited in layers, all applied before
the code is looked at: a rate per address, a lockout per address after repeated wrong codes, a drop after a few
wrong codes on one connection, and a budget of wrong codes for the whole bucket per minute. IPv6 addresses
are grouped by `/64` so rotating addresses does not help. Open connections are capped per address and in total.
Defaults and ranges are in [docs/protocol.md](docs/protocol.md) and enforced by bucket validation.

## Development

```
go vet ./...
go test -race ./...                                     # unit and end-to-end tests, no network needed
go run ./cmd/devserver -bucket game                     # a local server with no database, for testing a game
MONGODB_URI=mongodb://localhost:27017 go test -tags mongo ./internal/store/mongo   # needs a real MongoDB
```

CI also runs `gofmt`, a `go mod tidy` check, `staticcheck`, `govulncheck` (on the latest Go patch release),
the Mongo store tests against a MongoDB container, and a Docker build.

```
cmd/p2p-signaling-server/   entry point: load config, run, shut down
cmd/devserver/              in-memory local server for developing games (no database, no admin API)
internal/
  config/      environment parsing and validation
  app/         wiring and lifecycle (graceful shutdown)
  bucket/      Bucket type, validation, the Store interface, the cached Service
  store/       memory (tests) and mongo (Atlas) implementations
  room/        the room state machine (no networking)
  signaling/   the WebSocket handler and wire protocol
  admin/       REST admin API
  mcpserver/   MCP tools, thin layer over the same services
  ops/         the service layer shared by REST and MCP
  auth/ ratelimit/ netutil/ httpserver/ metrics/   building blocks
clients/godot-csharp/       Godot 4 (C#) client library and example
docs/                       integration guide, wire protocol, CapRover deployment
CLAUDE.md                   guide for coding agents working in this repository
test/e2e/                   end-to-end tests with real WebSocket clients
```

## License

[MIT](LICENSE)
