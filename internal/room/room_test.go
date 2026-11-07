package room

import (
	"net/netip"
	"strings"
	"testing"
	"time"
)

func peer() *Peer { return &Peer{Send: func(any) {}} }

func peerAt(ip string) *Peer {
	p := peer()
	p.IP = netip.MustParseAddr(ip)
	return p
}

var star = Params{Topology: "star", MaxPlayers: 3, MaxRooms: 2, CodeLength: 5}

func join(m *Manager, r *Room, p *Peer) (JoinResult, error) {
	return m.Join(r.Bucket, r.Code, p, false)
}

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
	if _, err := m.Join("g", "NOPE1", a, false); err != ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if _, err := m.Join("other", r.Code, a, false); err != ErrNotFound {
		t.Fatalf("codes must be per bucket, got %v", err)
	}
	if res, err := join(m, r, a); err != nil || len(res.Existing) != 1 || a.ID < 2 {
		t.Fatalf("join: %v %v id=%d", err, res.Existing, a.ID)
	}
	if _, err := join(m, r, b); err != nil {
		t.Fatal(err)
	}
	if _, err := join(m, r, c); err != ErrFull {
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
	if _, err := join(m, r, c); err != ErrNotFound {
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
	join(m, r, a)
	join(m, r, b)
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

func TestRoomsPerIP(t *testing.T) {
	m := NewManager()
	p := Params{Topology: "star", MaxPlayers: 4, MaxRooms: 50, CodeLength: 5, RoomsPerIP: 2}
	for i := 0; i < 2; i++ {
		if _, err := m.Create("g", p, peerAt("203.0.113.1")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.Create("g", p, peerAt("203.0.113.1")); err != ErrTooManyRooms {
		t.Fatalf("want ErrTooManyRooms, got %v", err)
	}
	if _, err := m.Create("g", p, peerAt("203.0.113.2")); err != nil {
		t.Fatalf("other address is fine: %v", err)
	}
	if _, err := m.Create("h", p, peerAt("203.0.113.1")); err != nil {
		t.Fatalf("limit is per bucket: %v", err)
	}
}

func TestCloseBucket(t *testing.T) {
	m := NewManager()
	host, a := peer(), peer()
	r, _ := m.Create("g", star, host)
	join(m, r, a)
	if ps := m.CloseBucket("g"); len(ps) != 2 {
		t.Fatalf("got %d peers", len(ps))
	}
	if len(m.List("")) != 0 {
		t.Fatal("rooms remain")
	}
}

func TestApproval(t *testing.T) {
	m := NewManager()
	host, a, b, stranger := peer(), peer(), peer(), peer()
	r, _ := m.Create("g", star, host)

	res, err := m.Join("g", r.Code, a, true)
	if err != nil || !res.Pending {
		t.Fatalf("want pending join, got %+v %v", res, err)
	}
	// a pending joiner is not a member: it cannot signal and does not count as a peer
	if _, err := m.Route(a, HostID); err != ErrNotInRoom {
		t.Fatalf("pending joiner must not route, got %v", err)
	}
	if len(m.List("g")) != 1 || m.List("g")[0].Peers != 1 {
		t.Fatalf("pending joiner must not count as a peer: %+v", m.List("g"))
	}
	// only the host decides
	if _, err := m.Approve(stranger, a.ID, true); err != ErrNotInRoom {
		t.Fatalf("outsider: got %v", err)
	}
	join(m, r, b) // b is admitted directly (a non-host member) while a is still pending
	if _, err := m.Approve(b, a.ID, true); err != ErrNotHost {
		t.Fatalf("non-host: got %v", err)
	}
	if _, err := m.Approve(host, 12345, true); err != ErrNoPeer {
		t.Fatalf("unknown peer: got %v", err)
	}
	ar, err := m.Approve(host, a.ID, true)
	if err != nil || !ar.Accepted || len(ar.Existing) != 2 {
		t.Fatalf("approve: %+v %v", ar, err)
	}
	if _, err := m.Route(a, HostID); err != nil {
		t.Fatalf("approved peer should route: %v", err)
	}
}

func TestApprovalDeniedFreesSlot(t *testing.T) {
	m := NewManager()
	p := star
	p.MaxPlayers = 2
	host, a, b := peer(), peer(), peer()
	r, _ := m.Create("g", p, host)
	if _, err := m.Join("g", r.Code, a, true); err != nil {
		t.Fatal(err)
	}
	// the pending joiner holds the only free slot
	if _, err := m.Join("g", r.Code, b, true); err != ErrFull {
		t.Fatalf("pending joiners must reserve capacity, got %v", err)
	}
	ar, err := m.Approve(host, a.ID, false)
	if err != nil || ar.Accepted {
		t.Fatalf("deny: %+v %v", ar, err)
	}
	if _, err := m.Join("g", r.Code, b, true); err != nil {
		t.Fatalf("slot should be free after denial: %v", err)
	}
	if _, err := m.Join("g", r.Code, a, false); err != ErrFull {
		t.Fatalf("slot is held by b now: %v", err)
	}
}

func TestPendingLeaveAndHostClose(t *testing.T) {
	m := NewManager()
	host, a, b := peer(), peer(), peer()
	r, _ := m.Create("g", star, host)
	m.Join("g", r.Code, a, true)
	m.Join("g", r.Code, b, true)

	res, ok := m.Leave(a)
	if !ok || !res.Pending || res.Left != a.ID || len(res.Others) != 1 || res.Others[0] != host {
		t.Fatalf("pending leave: %+v", res)
	}
	// host leaving also releases pending joiners
	res, _ = m.Leave(host)
	if !res.Closed || len(res.Others) != 1 || res.Others[0] != b {
		t.Fatalf("host leave should hand back pending joiners: %+v", res)
	}
	if _, err := m.Join("g", r.Code, a, false); err != ErrNotFound {
		t.Fatalf("room should be gone: %v", err)
	}
}

func TestLock(t *testing.T) {
	m := NewManager()
	host, a, b := peer(), peer(), peer()
	r, _ := m.Create("g", star, host)
	join(m, r, a)
	if err := m.SetLocked(a, true); err != ErrNotHost {
		t.Fatalf("non-host lock: got %v", err)
	}
	if err := m.SetLocked(host, true); err != nil {
		t.Fatal(err)
	}
	if _, err := join(m, r, b); err != ErrLocked {
		t.Fatalf("want ErrLocked, got %v", err)
	}
	m.SetLocked(host, false)
	if _, err := join(m, r, b); err != nil {
		t.Fatalf("unlocked room should admit: %v", err)
	}
}

func TestSweepIdleAndPending(t *testing.T) {
	now := time.Unix(1000, 0)
	m := NewManager()
	m.now = func() time.Time { return now }
	idle := func(string) time.Duration { return 10 * time.Minute }

	host, a, other := peer(), peer(), peer()
	r, _ := m.Create("g", star, host)
	m.Join("g", r.Code, a, true)
	quiet, _ := m.Create("g", star, other)

	now = now.Add(61 * time.Second)
	res := m.Sweep(idle, 60*time.Second)
	if len(res.Closed) != 0 || len(res.Denied) != 1 || res.Denied[0].Peer != a || res.Denied[0].Host != host {
		t.Fatalf("pending should time out and nothing else: %+v", res)
	}
	if _, err := m.Join("g", r.Code, a, false); err != nil {
		t.Fatalf("denied joiner is free to try again: %v", err)
	}

	// activity keeps a room alive; silence closes it
	now = now.Add(9 * time.Minute)
	m.Touch(host)
	now = now.Add(2 * time.Minute) // quiet room: 11 min since last activity; host's room: 2 min
	res = m.Sweep(idle, 60*time.Second)
	if len(res.Closed) != 1 || res.Closed[0][0] != other {
		t.Fatalf("only the quiet room should close: %+v", res.Closed)
	}
	infos := m.List("g")
	if len(infos) != 1 || infos[0].Code != r.Code {
		t.Fatalf("active room must survive: %+v (quiet was %s)", infos, quiet.Code)
	}
}

func TestRoomsPerIPCountsAWholeIPv6Prefix(t *testing.T) {
	m := NewManager()
	p := Params{Topology: "star", MaxPlayers: 4, MaxRooms: 50, CodeLength: 5, RoomsPerIP: 2}
	// Different addresses inside one /64 are one subscriber.
	for _, a := range []string{"2001:db8:1:2::1", "2001:db8:1:2:dead:beef::7"} {
		if _, err := m.Create("g", p, peerAt(a)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.Create("g", p, peerAt("2001:db8:1:2:cafe::9")); err != ErrTooManyRooms {
		t.Fatalf("a third address in the same /64 must be refused, got %v", err)
	}
	if _, err := m.Create("g", p, peerAt("2001:db8:1:3::1")); err != nil {
		t.Fatalf("another /64 is fine: %v", err)
	}
}

func TestCreateGivesUpWhenCodesRunOut(t *testing.T) {
	m := NewManager()
	// One-character codes: 32 possible. The search for a free code is bounded, so a nearly full space
	// ends in ErrBusy instead of spinning.
	p := Params{Topology: "star", MaxPlayers: 2, MaxRooms: 1000, CodeLength: 1}
	var busy bool
	for i := 0; i < 200 && !busy; i++ {
		if _, err := m.Create("g", p, peer()); err == ErrBusy {
			busy = true
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if !busy {
		t.Fatal("expected ErrBusy once the code space is exhausted")
	}
}
