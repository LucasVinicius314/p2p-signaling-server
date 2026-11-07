package room

import (
	"strings"
	"testing"
)

func peer() *Peer { return &Peer{Send: func(any) {}} }

var star = Params{Topology: "star", MaxPlayers: 3, MaxRooms: 2, CodeLength: 5}

func TestCodes(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		c := NewCode(5)
		if len(c) != 5 || strings.Trim(c, Alphabet) != "" {
			t.Fatalf("bad code %q", c)
		}
		seen[c] = true
	}
	if len(seen) < 990 {
		t.Fatalf("suspiciously many collisions: %d unique", len(seen))
	}
}

func TestLifecycle(t *testing.T) {
	m := NewManager()
	host, a, b, c := peer(), peer(), peer(), peer()
	r, err := m.Create("g", star, host)
	if err != nil || host.ID != HostID {
		t.Fatal(err)
	}
	if _, err := m.Create("g", star, host); err != ErrInRoom {
		t.Fatalf("want ErrInRoom, got %v", err)
	}
	if _, _, err := m.Join("g", "NOPE1", a); err != ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if _, _, err := m.Join("other", r.Code, a); err != ErrNotFound {
		t.Fatalf("codes must be per bucket, got %v", err)
	}
	if _, ex, err := m.Join("g", r.Code, a); err != nil || len(ex) != 1 || a.ID < 2 {
		t.Fatalf("join: %v %v id=%d", err, ex, a.ID)
	}
	if _, _, err := m.Join("g", r.Code, b); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Join("g", r.Code, c); err != ErrFull {
		t.Fatalf("want ErrFull, got %v", err)
	}
	// star: clients cannot signal each other, but can signal the host
	if _, err := m.Route(a, b.ID); err != ErrNotAllowed {
		t.Fatalf("want ErrNotAllowed, got %v", err)
	}
	if to, err := m.Route(a, HostID); err != nil || to != host {
		t.Fatalf("route to host: %v", err)
	}
	if _, err := m.Route(a, 999); err != ErrNoPeer {
		t.Fatalf("want ErrNoPeer, got %v", err)
	}
	// client leaves: room stays
	if res, ok := m.Leave(a); !ok || res.Closed || len(res.Others) != 2 {
		t.Fatalf("client leave: %+v", res)
	}
	// host leaves: room closes, others detached
	res, _ := m.Leave(host)
	if !res.Closed || len(res.Others) != 1 {
		t.Fatalf("host leave: %+v", res)
	}
	if _, _, err := m.Join("g", r.Code, c); err != ErrNotFound {
		t.Fatalf("closed room must be gone, got %v", err)
	}
	if _, err := m.Route(b, HostID); err != ErrNotInRoom {
		t.Fatalf("want ErrNotInRoom, got %v", err)
	}
}

func TestMeshRoutesAnyPair(t *testing.T) {
	m := NewManager()
	host, a, b := peer(), peer(), peer()
	p := star
	p.Topology = "mesh"
	r, _ := m.Create("g", p, host)
	m.Join("g", r.Code, a)
	m.Join("g", r.Code, b)
	if _, err := m.Route(a, b.ID); err != nil {
		t.Fatalf("mesh should allow client-client: %v", err)
	}
}

func TestMaxRooms(t *testing.T) {
	m := NewManager()
	for i := 0; i < 2; i++ {
		if _, err := m.Create("g", star, peer()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.Create("g", star, peer()); err != ErrBusy {
		t.Fatalf("want ErrBusy, got %v", err)
	}
	if _, err := m.Create("h", star, peer()); err != nil {
		t.Fatalf("limit is per bucket: %v", err)
	}
}

func TestCloseBucket(t *testing.T) {
	m := NewManager()
	host, a := peer(), peer()
	r, _ := m.Create("g", star, host)
	m.Join("g", r.Code, a)
	if ps := m.CloseBucket("g"); len(ps) != 2 {
		t.Fatalf("got %d peers", len(ps))
	}
	if len(m.List("")) != 0 {
		t.Fatal("rooms remain")
	}
}
