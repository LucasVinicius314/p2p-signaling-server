# Wire protocol (version 1)

WebSocket at `GET /ws` (use `wss://`). Text frames, each a JSON object with a `t` field naming the
message. Unknown message types and unknown fields are rejected with `bad_message`. The first message
must be `hello` within 10 seconds. Maximum frame size and message rate come from the bucket
(defaults 16 KiB, 30 per second); exceeding the frame size drops the connection, exceeding the rate
sends `rate_limited` and drops it.

The server sends WebSocket pings about every 20 seconds.

## Client to server

| `t` | Fields | Meaning |
|---|---|---|
| `hello` | `bucket`, `game_version`, `protocol` (1), `name?` | Must be first. Selects the bucket, checks version and origin. `name` is shortened to 32 bytes and stripped of control characters. |
| `create_room` | `max_players?`, `meta?` | Become host (peer id 1). `meta` (at most 1 KiB) is validated and ignored for now. |
| `join_room` | `code` | Join by code (case-insensitive). |
| `approve` | `peer`, `accept` | Host only, when the bucket has `host_approval`. Both fields are required. |
| `lock_room` | `locked` | Host only: refuse (or allow again) new joins. Required field. |
| `signal` | `to`, `kind` (`offer`/`answer`/`candidate`), `data` | Relay to another admitted peer. Star topology: only between a client and the host. |
| `leave` | | Leave the room; the socket stays open. If the host leaves, the room closes. |
| `ping` | | Application keepalive; answered with `pong`. |

## Server to client

| `t` | Fields | Meaning |
|---|---|---|
| `welcome` | `ice_servers`, `ice_policy` (`all`/`relay`), `topology` (`star`/`mesh`), `limits`, `protocol` | Reply to `hello`. |
| `room_created` | `code`, `peer_id` (always 1) | |
| `join_pending` | | Waiting for host approval. |
| `join_request` | `peer`, `name?` | To the host in approval mode. No address information. |
| `join_cancelled` | `peer` | To the host: that request was withdrawn (joiner left, or it timed out after 60 s). |
| `joined` | `peer_id`, `host_id`, `peers` | Admitted. Star: `peers` is just the host. Mesh: all existing peers. |
| `peer_joined` / `peer_left` | `peer_id` | Membership changes (star: sent to the host only; mesh: to everyone). |
| `signal` | `from`, `kind`, `data` | A relayed message. |
| `room_closed` | `reason` | `host_left`, `idle_timeout`, `closed_by_admin` or `bucket_deleted`. |
| `error` | `code`, `message` | See below. |
| `pong` | | |

Peer ids: the host is `1`; joiners get random unique ids in `2..2^31-1` (Godot's range).

## Error codes

`bad_message`, `unsupported_protocol`, `bucket_not_found`, `bucket_disabled`, `version_rejected`,
`origin_rejected`, `room_not_found`, `room_full`, `room_locked`, `join_denied`, `not_host`,
`rate_limited`, `locked_out`, `server_busy`.

- `room_not_found` is returned for both a wrong code and a nonexistent room, so codes cannot be probed.
- Failed joins count against the caller's address. After `failed_joins_before_lockout` failures
  within `lockout_seconds`, further `join_room` attempts from that address (for that bucket) get
  `locked_out` until the lockout ends, even with a correct code.
- `join_room` is also limited to `joins_per_ip_per_minute` attempts per address (`rate_limited`).
- `create_room` is refused with `rate_limited` when the address already hosts `rooms_per_ip` rooms,
  and with `server_busy` when the bucket is at `max_rooms`.
- `join_denied`: the host declined, or did not answer within 60 seconds.

## Idle rooms

Any message from a member (including `ping`) counts as activity. A room with no activity for
`room_idle_timeout_seconds` is closed with `room_closed` / `idle_timeout`. A host that wants its room
to stay joinable while a game runs should send a `ping` now and then (for example every minute).
