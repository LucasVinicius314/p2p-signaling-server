# Wire protocol (version 1)

WebSocket at `GET /ws` (use `wss://`). Text frames, each a JSON object with a `t` field naming the
message. Unknown message types and unknown fields are rejected with `bad_message`. The first message
must be `hello` within 10 seconds. Maximum frame size and message rate come from the bucket
(defaults 16 KiB, 30 per second); exceeding the frame size drops the connection, exceeding the rate
sends `rate_limited` and drops it.

The server sends WebSocket pings about every 20 seconds.

## Connecting

The upgrade itself can be refused before any message is exchanged:

| HTTP status | Meaning |
|---|---|
| `429` | too many new connections from this address (120 per minute), or too many open ones (32 per address, 10000 in total). An IPv6 `/64` counts as one address. |
| `503` | the server is shutting down; retry shortly |

## Client to server

| `t` | Fields | Meaning |
|---|---|---|
| `hello` | `bucket`, `game_version`, `protocol` (1), `name?` | Must be first. Selects the bucket, checks version and origin. `name` is shortened to 32 bytes and stripped of control characters. |
| `create_room` | `max_players?`, `host_tag?` | Become host (peer id 1). `host_tag` (64 lowercase hex characters, see [friends](#friends-listing-rooms-to-people-who-know-you)) makes the room visible to people who know that tag; without it the room is reachable only by its code. |
| `list_rooms` | `tags` (up to 100, each 64 lowercase hex characters) | Ask for the joinable rooms whose host tag is one of `tags`. Answered with `room_list`. |
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
| `room_list` | `rooms`: a list of `{code, tag, host?, players, max_players, age_seconds}` | Answer to `list_rooms`: oldest first, only rooms that are not locked and not full. `host` is the name the host connected with. |
| `join_pending` | | Waiting for host approval. |
| `join_request` | `peer`, `name?` | To the host in approval mode. No address information. |
| `join_cancelled` | `peer` | To the host: that request was withdrawn (joiner left, or it timed out after 60 s). |
| `joined` | `peer_id`, `host_id`, `peers` | Admitted. Star: `peers` is just the host. Mesh: all existing peers. |
| `peer_joined` / `peer_left` | `peer_id` | Membership changes (star: sent to the host only; mesh: to everyone). |
| `signal` | `from`, `kind`, `data` | A relayed message. |
| `room_closed` | `reason` | `host_left`, `idle_timeout`, `closed_by_admin` or `bucket_deleted`. |
| `server_restarting` | `retry_after_ms` | The server is shutting down (a redeploy). The socket is closed right after with close code 1012. Reconnect after the given delay. Peer-to-peer links that already exist are not affected, but the room is gone, so the host has to create a new one. |
| `error` | `code`, `message` | See below. |
| `pong` | | |

Peer ids: the host is `1`; joiners get random unique ids in `2..2^31-1` (Godot's range).

## Close codes

The server closes with a WebSocket close code that says why:

| Code | Reason text | Meaning |
|---|---|---|
| `1008` | an error code (`bad_message`, ...) | the handshake was rejected; the same code was sent as an `error` message first |
| `1008` | `too_many_failed_joins` | this connection tried too many wrong join codes (see below) |
| `1012` | `server restarting` | graceful shutdown; see `server_restarting` |

## Error codes

`bad_message`, `unsupported_protocol`, `bucket_not_found`, `bucket_disabled`,
`origin_rejected`, `room_not_found`, `room_full`, `room_locked`, `join_denied`, `not_host`,
`rate_limited`, `locked_out`, `server_busy`.

- `room_not_found` is returned for both a wrong code and a nonexistent room, so codes cannot be probed.
- `join_room` is limited in layers, all checked **before** the code is looked at, so a correct code
  does not get around them:
  1. `joins_per_ip_per_minute` attempts per address (`rate_limited`).
  2. A lockout per address: after `failed_joins_before_lockout` wrong codes within `lockout_seconds`,
     that address gets `locked_out` until the lockout ends.
  3. A budget for the whole bucket: more than `failed_joins_per_minute` wrong codes in a minute, from all
     addresses together, makes further joins fail with `rate_limited` until it refills. This stops
     guessing spread over many addresses.
- Independently of those, a connection that sends `failed_joins_per_connection` wrong codes is answered
  and then dropped (close code 1008, `too_many_failed_joins`). The per-connection figure is deliberately
  much lower than the per-address lockout, so one persistent guesser is cut off early while other players
  behind the same shared address are not locked out by a few typos.
- An "address" is an IPv4 address or a whole IPv6 `/64`.
- `create_room` is refused with `rate_limited` when the address already hosts `rooms_per_ip` rooms
  (0 means no limit), and with `server_busy` when the bucket is at `max_rooms`.
- `join_denied`: the host declined, or did not answer within 60 seconds.

## Friends: listing rooms to people who know you

A room is normally reachable only by its code. A host can also tag its room so that friends find it without being sent
the code:

- Every machine has a random id (see the client library's `P2PIdentity`) that the player gives to friends. The **tag** is
  the lowercase hex of `SHA256("p2p-signaling-server/friends/v1:" + canonical id)`, so the server never receives the id
  itself.
- A host sends its tag as `host_tag` in `create_room`. A friend sends the tags of the ids on their friends list in
  `list_rooms` and gets back the matching rooms with their join codes. The code is then used with `join_room` as always, so
  every limit on joining (rate limits, lockouts, host approval, locked or full rooms) still applies.
- A tag is a capability: it is a 256-bit hash of a secret id, so it cannot be guessed, and whoever holds it can list that
  host's rooms. Rooms without a tag are never listed. Rooms that are locked or full are left out of the answer.
- The server never logs tags, ids or codes. `list_rooms` is limited to 60 requests per minute per address
  (`rate_limited`, the connection stays open) and 100 tags per request (`bad_message`). It works in any bucket and needs
  no setting.
- Old servers do not know these messages and answer `bad_message`; the field and message are additive, so protocol 1 is
  unchanged and old clients are not affected.

## Idle rooms

Any message from a member (including `ping`) counts as activity. A room with no activity for
`room_idle_timeout_seconds` is closed with `room_closed` / `idle_timeout`. A host that wants its room
to stay joinable while a game runs should send a `ping` now and then (for example every minute); the
Godot client library does this every 30 seconds.
