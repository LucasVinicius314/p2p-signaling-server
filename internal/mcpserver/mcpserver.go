// Package mcpserver exposes the admin operations as MCP tools (streamable HTTP).
// Player-supplied text (room meta, peer names) is never returned by any tool.
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"p2p-signaling-server/internal/bucket"
	"p2p-signaling-server/internal/metrics"
	"p2p-signaling-server/internal/ops"
	"p2p-signaling-server/internal/room"
)

const (
	actor = "mcp"
	// serverInfoVersion is required by the MCP handshake; it is not a build version.
	serverInfoVersion = "1"
)

// LimitsFields are optional per-field limit overrides.
type LimitsFields struct {
	JoinsPerIPPerMinute      *int `json:"joins_per_ip_per_minute,omitempty"`
	FailedJoinsPerConnection *int `json:"failed_joins_per_connection,omitempty" jsonschema:"wrong codes one connection may try before it is dropped"`
	FailedJoinsBeforeLockout *int `json:"failed_joins_before_lockout,omitempty" jsonschema:"wrong codes from one address before it is locked out"`
	LockoutSeconds           *int `json:"lockout_seconds,omitempty"`
	FailedJoinsPerMinute     *int `json:"failed_joins_per_minute,omitempty" jsonschema:"wrong codes the whole bucket tolerates per minute"`
	RoomsPerIP               *int `json:"rooms_per_ip,omitempty" jsonschema:"0 = unlimited"`
	MessageBytes             *int `json:"message_bytes,omitempty"`
	MessagesPerSecond        *int `json:"messages_per_second,omitempty"`
}

// Fields are the optional bucket config fields; anything left out keeps its default
// (create) or current value (update).
type Fields struct {
	Name                   *string             `json:"name,omitempty"`
	Enabled                *bool               `json:"enabled,omitempty"`
	Topology               *string             `json:"topology,omitempty" jsonschema:"star or mesh"`
	MaxPlayers             *int                `json:"max_players,omitempty" jsonschema:"2 to 16 (mesh: at most 8)"`
	MaxRooms               *int                `json:"max_rooms,omitempty"`
	CodeLength             *int                `json:"code_length,omitempty" jsonschema:"4 to 8"`
	AcceptedVersions       *[]string           `json:"accepted_versions,omitempty" jsonschema:"empty list accepts any game version"`
	HostApproval           *bool               `json:"host_approval,omitempty"`
	RelayOnly              *bool               `json:"relay_only,omitempty" jsonschema:"requires a turn: or turns: ice server"`
	AllowedOrigins         *[]string           `json:"allowed_origins,omitempty" jsonschema:"browser origins like https://example.com (no path); empty allows any"`
	IceServers             *[]bucket.IceServer `json:"ice_servers,omitempty" jsonschema:"replaces the whole list; a credential shown as ******** is kept if the server and username are unchanged"`
	Limits                 *LimitsFields       `json:"limits,omitempty"`
	RoomIdleTimeoutSeconds *int                `json:"room_idle_timeout_seconds,omitempty"`
	Notes                  *string             `json:"notes,omitempty"`
}

func (f Fields) raw() (json.RawMessage, error) { return json.Marshal(f) }

type idArg struct {
	ID string `json:"id" jsonschema:"bucket id, lowercase [a-z0-9-], 3 to 40 characters"`
}

type writeArgs struct {
	ID string `json:"id" jsonschema:"bucket id, lowercase [a-z0-9-], 3 to 40 characters"`
	Fields
}

type BucketSummary struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Enabled    bool   `json:"enabled"`
	Topology   string `json:"topology"`
	MaxPlayers int    `json:"max_players"`
	LiveRooms  int    `json:"live_rooms"`
}

type bucketsOut struct {
	Buckets []BucketSummary `json:"buckets"`
}
type bucketOut struct {
	Bucket bucket.Bucket `json:"bucket"`
}
type validateOut struct {
	Valid  bool           `json:"valid"`
	Error  string         `json:"error,omitempty"`
	Result *bucket.Bucket `json:"result,omitempty"`
}
type enabledArgs struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
}
type deleteArgs struct {
	ID      string `json:"id"`
	Confirm string `json:"confirm" jsonschema:"must equal id"`
}
type deletedOut struct {
	Deleted string `json:"deleted"`
}
type roomsArgs struct {
	Bucket string `json:"bucket,omitempty" jsonschema:"only rooms of this bucket"`
}
type roomsOut struct {
	Rooms []room.Info `json:"rooms"`
}
type closeArgs struct {
	Bucket string `json:"bucket"`
	Code   string `json:"code"`
}
type closedOut struct {
	Closed bool `json:"closed"`
}
type none struct{}
type statsOut struct {
	Stats metrics.Snapshot `json:"stats"`
}
type healthOut struct {
	Health ops.Health `json:"health"`
}
type auditArgs struct {
	Limit int `json:"limit,omitempty" jsonschema:"default 50, max 200"`
}
type auditOut struct {
	Entries []bucket.AuditEntry `json:"entries"`
}

func ptr[T any](v T) *T { return &v }

// New builds the MCP server with all tools.
func New(o *ops.Ops) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "p2p-signaling-server", Version: serverInfoVersion}, nil)
	ro := &mcp.ToolAnnotations{ReadOnlyHint: true}

	mcp.AddTool(s, &mcp.Tool{Name: "list_buckets", Description: "Summary of every game bucket, with live room counts.", Annotations: ro},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ none) (*mcp.CallToolResult, bucketsOut, error) {
			live := o.Stats().PerBucket
			out := bucketsOut{Buckets: []BucketSummary{}}
			for _, b := range o.Buckets.List() {
				out.Buckets = append(out.Buckets, BucketSummary{b.ID, b.Name, b.Enabled, b.Topology, b.MaxPlayers, live[b.ID].Rooms})
			}
			return nil, out, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "get_bucket", Description: "Full config of one bucket.", Annotations: ro},
		func(ctx context.Context, _ *mcp.CallToolRequest, in idArg) (*mcp.CallToolResult, bucketOut, error) {
			b, ok := o.Buckets.Get(in.ID)
			if !ok {
				return nil, bucketOut{}, bucket.ErrNotFound
			}
			return nil, bucketOut{b.Redacted()}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "create_bucket", Description: "Create a game bucket. Unspecified fields get defaults (star, 8 players, public STUN, any version). Returns the created config."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in writeArgs) (*mcp.CallToolResult, bucketOut, error) {
			if in.Name == nil {
				return nil, bucketOut{}, errors.New("name is required")
			}
			raw, err := in.Fields.raw()
			if err != nil {
				return nil, bucketOut{}, err
			}
			b, err := o.Buckets.Create(ctx, actor, in.ID, raw)
			return nil, bucketOut{b.Redacted()}, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "update_bucket", Description: "Change fields of a bucket; the resulting whole config is validated."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in writeArgs) (*mcp.CallToolResult, bucketOut, error) {
			raw, err := in.Fields.raw()
			if err != nil {
				return nil, bucketOut{}, err
			}
			b, err := o.Buckets.Patch(ctx, actor, in.ID, raw)
			return nil, bucketOut{b.Redacted()}, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "validate_bucket", Description: "Dry run: validate what create_bucket (new id) or update_bucket (existing id) would produce, without saving.", Annotations: ro},
		func(ctx context.Context, _ *mcp.CallToolRequest, in writeArgs) (*mcp.CallToolResult, validateOut, error) {
			raw, err := in.Fields.raw()
			if err != nil {
				return nil, validateOut{}, err
			}
			b, err := o.Buckets.Preview(in.ID, raw)
			if err != nil {
				return nil, validateOut{Error: err.Error()}, nil
			}
			if in.Name == nil && b.Name == "" {
				return nil, validateOut{Error: "name is required"}, nil
			}
			return nil, validateOut{Valid: true, Result: &b}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "set_bucket_enabled", Description: "Kill switch: a disabled bucket rejects new connections. Rooms already running are not closed."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in enabledArgs) (*mcp.CallToolResult, bucketOut, error) {
			raw, _ := Fields{Enabled: ptr(in.Enabled)}.raw()
			b, err := o.Buckets.Patch(ctx, actor, in.ID, raw)
			return nil, bucketOut{b.Redacted()}, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "delete_bucket", Description: "Permanently delete a bucket and close its live rooms. confirm must equal id.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: ptr(true)}},
		func(ctx context.Context, _ *mcp.CallToolRequest, in deleteArgs) (*mcp.CallToolResult, deletedOut, error) {
			if in.Confirm != in.ID {
				return nil, deletedOut{}, errors.New("confirm must equal id")
			}
			if err := o.DeleteBucket(ctx, actor, in.ID); err != nil {
				return nil, deletedOut{}, err
			}
			return nil, deletedOut{in.ID}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "list_rooms", Description: "Live rooms: bucket, code, peer count, age. Contains no player-supplied text.", Annotations: ro},
		func(ctx context.Context, _ *mcp.CallToolRequest, in roomsArgs) (*mcp.CallToolResult, roomsOut, error) {
			return nil, roomsOut{o.ListRooms(in.Bucket)}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "close_room", Description: "Close one live room; its peers are told the room was closed."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in closeArgs) (*mcp.CallToolResult, closedOut, error) {
			if !o.CloseRoom(in.Bucket, in.Code) {
				return nil, closedOut{}, errors.New("room not found")
			}
			return nil, closedOut{true}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "get_stats", Description: "Counters (connections, rooms created, joins, failures, rate-limit hits) and live gauges per bucket.", Annotations: ro},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ none) (*mcp.CallToolResult, statsOut, error) {
			return nil, statsOut{o.Stats()}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "get_health", Description: "Config cache state, store connectivity and uptime.", Annotations: ro},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ none) (*mcp.CallToolResult, healthOut, error) {
			return nil, healthOut{o.Health()}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "get_audit_log", Description: "Recent admin actions (newest first), from both REST and MCP.", Annotations: ro},
		func(ctx context.Context, _ *mcp.CallToolRequest, in auditArgs) (*mcp.CallToolResult, auditOut, error) {
			e, err := o.Buckets.RecentAudit(ctx, in.Limit)
			return nil, auditOut{e}, err
		})
	return s
}

// Handler serves the MCP streamable HTTP transport. Wrap it with the auth middleware.
func Handler(s *mcp.Server) http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
}
