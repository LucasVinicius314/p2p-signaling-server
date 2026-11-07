// Package room holds the in-memory room state machine. It knows nothing about networking:
// callers get back the peers they need to notify.
package room

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net/netip"
	"sync"
	"time"
)

const (
	// Alphabet has 32 symbols and no look-alikes (no 0/O, 1/I).
	Alphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"
	HostID   = 1
	maxID    = 1<<31 - 1
)

var (
	ErrNotFound     = errors.New("room not found")
	ErrFull         = errors.New("room full")
	ErrLocked       = errors.New("room locked")
	ErrBusy         = errors.New("too many rooms")
	ErrTooManyRooms = errors.New("too many rooms from this address")
	ErrInRoom       = errors.New("already in a room")
	ErrNotInRoom    = errors.New("not in a room")
	ErrNotHost      = errors.New("only the host can do that")
	ErrNoPeer       = errors.New("no such peer")
	ErrNotAllowed   = errors.New("signal not allowed between these peers")
)

// Peer is one connected player. Send must never block.
type Peer struct {
	ID   int32
	Name string
	IP   netip.Addr
	Send func(msg any)

	// guarded by Manager.mu
	room         *Room
	pending      bool
	pendingSince time.Time
}

type Params struct {
	Topology   string // "star" or "mesh"
	MaxPlayers int
	MaxRooms   int // per bucket
	CodeLength int
	RoomsPerIP int // live rooms one address may host in this bucket; 0 = unlimited
}

type Room struct {
	Bucket   string
	Code     string
	Topology string
	Max      int
	Created  time.Time

	host       *Peer
	peers      map[int32]*Peer // admitted members, including the host
	pending    map[int32]*Peer // joiners waiting for host approval
	locked     bool
	lastActive time.Time
}

type Manager struct {
	mu    sync.Mutex
	now   func() time.Time
	rooms map[string]map[string]*Room // bucket -> code -> room
}

func NewManager() *Manager {
	return &Manager{now: time.Now, rooms: map[string]map[string]*Room{}}
}

// Create makes a room with host as peer 1.
func (m *Manager) Create(bucketID string, p Params, host *Peer) (*Room, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if host.room != nil {
		return nil, ErrInRoom
	}
	codes := m.rooms[bucketID]
	if codes == nil {
		codes = map[string]*Room{}
		m.rooms[bucketID] = codes
	}
	if len(codes) >= p.MaxRooms {
		return nil, ErrBusy
	}
	if p.RoomsPerIP > 0 && host.IP.IsValid() {
		n := 0
		for _, r := range codes {
			if r.host.IP == host.IP {
				n++
			}
		}
		if n >= p.RoomsPerIP {
			return nil, ErrTooManyRooms
		}
	}
	var code string
	for {
		code = NewCode(p.CodeLength)
		if _, taken := codes[code]; !taken {
			break
		}
	}
	host.ID = HostID
	now := m.now()
	r := &Room{
		Bucket: bucketID, Code: code, Topology: p.Topology, Max: p.MaxPlayers, Created: now,
		host: host, peers: map[int32]*Peer{HostID: host}, pending: map[int32]*Peer{}, lastActive: now,
	}
	host.room = r
	codes[code] = r
	return r, nil
}

// JoinResult describes a successful Join. If Pending, the joiner waits for the host's decision
// and Existing is empty.
type JoinResult struct {
	Room     *Room
	Existing []*Peer
	Pending  bool
}

// Join adds p to the room, or, when approval is true, parks it until the host decides.
// Pending joiners count against capacity so a flood of requests cannot overwhelm the host.
func (m *Manager) Join(bucketID, code string, p *Peer, approval bool) (JoinResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p.room != nil {
		return JoinResult{}, ErrInRoom
	}
	r := m.rooms[bucketID][code]
	if r == nil {
		return JoinResult{}, ErrNotFound
	}
	if r.locked {
		return JoinResult{}, ErrLocked
	}
	if len(r.peers)+len(r.pending) >= r.Max {
		return JoinResult{}, ErrFull
	}
	for {
		id := randomID()
		if r.peers[id] == nil && r.pending[id] == nil {
			p.ID = id
			break
		}
	}
	r.lastActive = m.now()
	p.room = r
	if approval {
		p.pending, p.pendingSince = true, m.now()
		r.pending[p.ID] = p
		return JoinResult{Room: r, Pending: true}, nil
	}
	existing := r.snapshot()
	r.peers[p.ID] = p
	return JoinResult{Room: r, Existing: existing}, nil
}

// ApproveResult describes the host's decision on a pending joiner.
type ApproveResult struct {
	Room     *Room
	Peer     *Peer
	Accepted bool
	Existing []*Peer // members before the joiner was admitted
}

// Approve lets the host admit or deny a pending joiner.
func (m *Manager) Approve(host *Peer, id int32, accept bool) (ApproveResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := host.room
	if r == nil || r.peers[host.ID] != host {
		return ApproveResult{}, ErrNotInRoom
	}
	if r.host != host {
		return ApproveResult{}, ErrNotHost
	}
	p := r.pending[id]
	if p == nil {
		return ApproveResult{}, ErrNoPeer
	}
	delete(r.pending, id)
	p.pending = false
	r.lastActive = m.now()
	if !accept {
		p.room = nil
		return ApproveResult{Room: r, Peer: p}, nil
	}
	existing := r.snapshot()
	r.peers[id] = p
	return ApproveResult{Room: r, Peer: p, Accepted: true, Existing: existing}, nil
}

// SetLocked lets the host refuse (or allow again) new joins.
func (m *Manager) SetLocked(host *Peer, locked bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := host.room
	if r == nil || r.peers[host.ID] != host {
		return ErrNotInRoom
	}
	if r.host != host {
		return ErrNotHost
	}
	r.locked = locked
	r.lastActive = m.now()
	return nil
}

// LeaveResult says who must be told. If Closed, the room is gone and Others are all its former
// members and pending joiners. If Pending, the leaver was only waiting for approval and Others
// is the host.
type LeaveResult struct {
	Room    *Room
	Closed  bool
	Pending bool
	Left    int32
	Others  []*Peer
}

// Leave removes p. The host leaving closes the room.
func (m *Manager) Leave(p *Peer) (LeaveResult, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := p.room
	if r == nil {
		return LeaveResult{}, false
	}
	p.room = nil
	res := LeaveResult{Room: r, Left: p.ID}
	if p.pending {
		p.pending = false
		delete(r.pending, p.ID)
		res.Pending = true
		res.Others = []*Peer{r.host}
		return res, true
	}
	delete(r.peers, p.ID)
	r.lastActive = m.now()
	if p == r.host {
		res.Closed = true
		res.Others = m.detach(r)
		return res, true
	}
	res.Others = r.snapshot()
	return res, true
}

// Touch records activity from p; idle rooms are closed by Sweep.
func (m *Manager) Touch(p *Peer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p.room != nil {
		p.room.lastActive = m.now()
	}
}

// Route finds the target of a signal from p, enforcing topology rules.
func (m *Manager) Route(from *Peer, to int32) (*Peer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := from.room
	if r == nil || r.peers[from.ID] != from {
		return nil, ErrNotInRoom
	}
	t := r.peers[to]
	if t == nil || t == from {
		return nil, ErrNoPeer
	}
	if r.Topology == "star" && from != r.host && t != r.host {
		return nil, ErrNotAllowed
	}
	r.lastActive = m.now()
	return t, nil
}

// Denied is a pending joiner whose approval timed out.
type Denied struct{ Peer, Host *Peer }

type SweepResult struct {
	Closed [][]*Peer // members of each room closed for idleness
	Denied []Denied
}

// Sweep closes rooms idle longer than idle(bucket) and denies pending joiners older than pendingTTL.
func (m *Manager) Sweep(idle func(bucketID string) time.Duration, pendingTTL time.Duration) SweepResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	var res SweepResult
	for bucketID, codes := range m.rooms {
		for _, r := range codes {
			if now.Sub(r.lastActive) > idle(bucketID) {
				res.Closed = append(res.Closed, m.detach(r))
				continue
			}
			for id, p := range r.pending {
				if now.Sub(p.pendingSince) > pendingTTL {
					delete(r.pending, id)
					p.pending, p.room = false, nil
					res.Denied = append(res.Denied, Denied{p, r.host})
				}
			}
		}
	}
	return res
}

// CloseRoom removes a room and returns everyone in it (all detached).
func (m *Manager) CloseRoom(bucketID, code string) ([]*Peer, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.rooms[bucketID][code]
	if r == nil {
		return nil, false
	}
	return m.detach(r), true
}

// CloseBucket removes every room of a bucket and returns everyone in them.
func (m *Manager) CloseBucket(bucketID string) []*Peer {
	m.mu.Lock()
	defer m.mu.Unlock()
	var all []*Peer
	for _, r := range m.rooms[bucketID] {
		all = append(all, m.detach(r)...)
	}
	return all
}

// detach removes r and returns its remaining members and pending joiners.
func (m *Manager) detach(r *Room) []*Peer {
	ps := r.snapshot()
	for _, p := range r.pending {
		p.pending = false
		ps = append(ps, p)
	}
	for _, p := range ps {
		p.room = nil
	}
	r.peers, r.pending = map[int32]*Peer{}, map[int32]*Peer{}
	delete(m.rooms[r.Bucket], r.Code)
	return ps
}

// Info is a read-only description of a room containing no player-supplied text.
type Info struct {
	Bucket string  `json:"bucket"`
	Code   string  `json:"code"`
	Peers  int     `json:"peers"`
	Age    float64 `json:"age_seconds"`
}

// List returns live rooms, optionally filtered by bucket.
func (m *Manager) List(bucketID string) []Info {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Info{}
	for b, codes := range m.rooms {
		if bucketID != "" && b != bucketID {
			continue
		}
		for _, r := range codes {
			out = append(out, Info{b, r.Code, len(r.peers), m.now().Sub(r.Created).Seconds()})
		}
	}
	return out
}

// Host returns the room's host peer.
func (r *Room) Host() *Peer { return r.host }

func (r *Room) snapshot() []*Peer {
	out := make([]*Peer, 0, len(r.peers))
	for _, p := range r.peers {
		out = append(out, p)
	}
	return out
}

// NewCode returns a random join code of length n from Alphabet using crypto/rand.
func NewCode(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = Alphabet[b[i]&31] // 32 symbols: no modulo bias
	}
	return string(b)
}

func randomID() int32 {
	var b [4]byte
	for {
		if _, err := rand.Read(b[:]); err != nil {
			panic(err)
		}
		v := binary.BigEndian.Uint32(b[:]) & maxID
		if v >= 2 {
			return int32(v)
		}
	}
}
