# Deploying on CapRover

The repo root has a `captain-definition` pointing at `deployments/Dockerfile`; CapRover builds the
image from the repo.

## App settings

- **WebSocket Support**: on. Required for `/ws`.
- **HTTPS** and **Force HTTPS**: on.
- **Instance count**: 1. Rooms live in memory; a second instance would split them.
- **Container HTTP Port**: `8080` (the app's default `PORT`), or set `PORT=80`.
- **Atlas**: the server's IP must be on the Atlas network allowlist.

## Environment variables

| Variable | Required | Meaning |
|---|---|---|
| `MASTER_API_KEY` | yes | at least 32 characters; used as `Authorization: Bearer <key>` by REST and MCP |
| `MONGODB_URI` | yes | Atlas connection string (the database is always `main`) |
| `PORT` | no | default 8080 |
| `TRUSTED_PROXY_CIDRS` | **yes in practice** | comma-separated networks whose `X-Forwarded-For` is believed (see below) |
| `LOG_LEVEL` | no | `debug`, `info` (default), `warn`, `error` |

## Real client addresses (rate limits and lockouts depend on this)

Rate limits, lockouts, the connection caps and the per-address room cap are keyed by client IP
(an IPv6 address counts as its whole `/64`). Behind CapRover (and
Cloudflare) the TCP peer is the proxy, so without configuration **every player looks like one
address** and one abuser can lock out everybody. The server logs a warning at startup when
`TRUSTED_PROXY_CIDRS` is empty.

The server believes `X-Forwarded-For` only when the direct peer is inside `TRUSTED_PROXY_CIDRS`,
and reads the header right to left, skipping trusted networks: the first untrusted address is the
client. So the list must contain every proxy hop in front of the app:

1. **CapRover's network** (its nginx talks to the container from the Docker network), typically
   `10.0.0.0/8` and `172.16.0.0/12`.
2. **Cloudflare**, if the domain is proxied (orange cloud). Otherwise the Cloudflare edge address
   would be taken for the client.

A starting value for a Cloudflare-proxied app (Cloudflare's published ranges; refresh from <https://www.cloudflare.com/ips-v4> and <https://www.cloudflare.com/ips-v6>):

```
10.0.0.0/8,172.16.0.0/12,173.245.48.0/20,103.21.244.0/22,103.22.200.0/22,103.31.4.0/22,141.101.64.0/18,108.162.192.0/18,190.93.240.0/20,188.114.96.0/20,197.234.240.0/22,198.41.128.0/17,162.158.0.0/15,104.16.0.0/13,104.24.0.0/14,172.64.0.0/13,131.0.72.0/22,2400:cb00::/32,2606:4700::/32,2803:f800::/32,2405:b500::/32,2405:8100::/32,2a06:98c0::/29,2c0f:f248::/32
```

### Verify it

`GET /admin/v1/client-ip` (with the bearer key) shows what the server sees:

```
curl -H "Authorization: Bearer <key>" https://<host>/admin/v1/client-ip
{"client_ip":"...","remote_addr":"...","x_forwarded_for":"..."}
```

`client_ip` must be **the public address of the machine you called from**. If it is a proxy address instead, that proxy is
missing from `TRUSTED_PROXY_CIDRS` (add the network shown in `remote_addr` or the last entries of
`x_forwarded_for`). If it is your own address, you are set.

Only the TCP peer is trusted to vouch for the header, so a client that reaches the server directly
cannot spoof its address by sending its own `X-Forwarded-For`.

## Logs

Logs are JSON on stdout (`LOG_LEVEL=debug` adds per-connection detail). Every HTTP request is logged with
its route pattern (never the path, which can contain a room code), status and duration, and answered with
an `X-Request-Id` header that appears in the matching log line. Signaling events (room created and closed,
hello rejected, lockouts, capacity) are logged with the bucket id. Join codes, client addresses, player
names and signaling payloads are never logged. A panic is logged with its stack trace.

## Restarts

On SIGTERM the server stops accepting connections (new ones get `503`), sends every connected client
`server_restarting` with a retry delay, closes their sockets with code 1012, and then exits (within 8
seconds). A redeploy closes the rooms (they live in memory), but games that already have their
peer-to-peer connections keep running; only new joins fail for a few seconds, and a host has to create a
new room afterwards.
