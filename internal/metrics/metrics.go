// Package metrics holds process-wide counters. All methods are safe on a nil *Metrics.
package metrics

import (
	"sync/atomic"
	"time"

	"p2p-signaling-server/internal/room"
)

type Metrics struct {
	start        time.Time
	connections  atomic.Int64
	roomsCreated atomic.Int64
	joins        atomic.Int64
	failedJoins  atomic.Int64
	rateLimited  atomic.Int64
	lockouts     atomic.Int64
}

func New() *Metrics { return &Metrics{start: time.Now()} }

func (m *Metrics) Connection() {
	if m != nil {
		m.connections.Add(1)
	}
}

func (m *Metrics) RoomCreated() {
	if m != nil {
		m.roomsCreated.Add(1)
	}
}

func (m *Metrics) Join() {
	if m != nil {
		m.joins.Add(1)
	}
}

func (m *Metrics) FailedJoin() {
	if m != nil {
		m.failedJoins.Add(1)
	}
}

func (m *Metrics) RateLimited() {
	if m != nil {
		m.rateLimited.Add(1)
	}
}

func (m *Metrics) Lockout() {
	if m != nil {
		m.lockouts.Add(1)
	}
}

// Uptime since the process started.
func (m *Metrics) Uptime() time.Duration {
	if m == nil {
		return 0
	}
	return time.Since(m.start)
}

type BucketGauges struct {
	Rooms int `json:"rooms"`
	Peers int `json:"peers"`
}

type Snapshot struct {
	Connections   int64                   `json:"connections_total"`
	RoomsCreated  int64                   `json:"rooms_created_total"`
	Joins         int64                   `json:"joins_total"`
	FailedJoins   int64                   `json:"failed_joins_total"`
	RateLimitHits int64                   `json:"rate_limit_hits_total"`
	Lockouts      int64                   `json:"lockouts_total"`
	RoomsActive   int                     `json:"rooms_active"`
	PeersActive   int                     `json:"peers_active"`
	PerBucket     map[string]BucketGauges `json:"per_bucket"`
}

// Snapshot combines the counters with gauges derived from the live rooms.
func (m *Metrics) Snapshot(rooms []room.Info) Snapshot {
	s := Snapshot{PerBucket: map[string]BucketGauges{}}
	if m != nil {
		s.Connections, s.RoomsCreated, s.Joins = m.connections.Load(), m.roomsCreated.Load(), m.joins.Load()
		s.FailedJoins, s.RateLimitHits, s.Lockouts = m.failedJoins.Load(), m.rateLimited.Load(), m.lockouts.Load()
	}
	for _, r := range rooms {
		g := s.PerBucket[r.Bucket]
		g.Rooms++
		g.Peers += r.Peers
		s.PerBucket[r.Bucket] = g
		s.RoomsActive++
		s.PeersActive += r.Peers
	}
	return s
}
