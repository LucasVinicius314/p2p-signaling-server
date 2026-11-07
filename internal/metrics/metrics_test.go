package metrics

import (
	"testing"

	"p2p-signaling-server/internal/room"
)

func TestNilMetricsIsSafe(t *testing.T) {
	var m *Metrics
	m.Connection()
	m.RoomCreated()
	m.Join()
	m.FailedJoin()
	m.RateLimited()
	if s := m.Snapshot(nil); s.RoomsActive != 0 {
		t.Fatal("unexpected")
	}
}

func TestSnapshot(t *testing.T) {
	m := New()
	m.Join()
	m.Join()
	m.FailedJoin()
	s := m.Snapshot([]room.Info{{Bucket: "a", Peers: 2}, {Bucket: "a", Peers: 3}, {Bucket: "b", Peers: 1}})
	if s.Joins != 2 || s.FailedJoins != 1 || s.RoomsActive != 3 || s.PeersActive != 6 {
		t.Fatalf("%+v", s)
	}
	if g := s.PerBucket["a"]; g.Rooms != 2 || g.Peers != 5 {
		t.Fatalf("%+v", g)
	}
}
