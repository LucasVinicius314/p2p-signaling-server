// Package ops is the service layer shared by the REST admin API and the MCP tools,
// so the two cannot drift apart.
package ops

import (
	"context"
	"time"

	"p2p-signaling-server/internal/bucket"
	"p2p-signaling-server/internal/metrics"
	"p2p-signaling-server/internal/room"
)

// Closer closes live rooms and tells their peers why (implemented by signaling.Handler).
type Closer interface {
	CloseRoom(bucketID, code, reason string) bool
	CloseBucket(bucketID, reason string)
}

type Ops struct {
	Buckets *bucket.Service
	Rooms   *room.Manager
	Closer  Closer
	Metrics *metrics.Metrics
}

// DeleteBucket deletes the bucket and closes its live rooms.
func (o *Ops) DeleteBucket(ctx context.Context, actor, id string) error {
	if err := o.Buckets.Delete(ctx, actor, id); err != nil {
		return err
	}
	o.Closer.CloseBucket(id, "bucket_deleted")
	return nil
}

// ListRooms returns live rooms (no player-supplied text), optionally for one bucket.
func (o *Ops) ListRooms(bucketID string) []room.Info { return o.Rooms.List(bucketID) }

// CloseRoom closes a room; it reports whether the room existed.
func (o *Ops) CloseRoom(bucketID, code string) bool {
	return o.Closer.CloseRoom(bucketID, code, "closed_by_admin")
}

func (o *Ops) Stats() metrics.Snapshot { return o.Metrics.Snapshot(o.Rooms.List("")) }

type Health struct {
	Status        string        `json:"status"` // "ok" or "degraded"
	UptimeSeconds int64         `json:"uptime_seconds"`
	Buckets       int           `json:"buckets"`
	Cache         bucket.Status `json:"cache"`
}

// Health reports cache state; "degraded" means the last store refresh failed (joins keep working
// from the cached config).
func (o *Ops) Health() Health {
	st := o.Buckets.Status()
	h := Health{
		Status: "ok", Buckets: len(o.Buckets.List()), Cache: st,
		UptimeSeconds: int64(o.Metrics.Uptime() / time.Second),
	}
	if !st.Loaded || st.LastError != "" {
		h.Status = "degraded"
	}
	return h
}
