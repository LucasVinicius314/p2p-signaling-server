// Package room holds the in-memory room state machine. It knows nothing about networking:
// callers get back the peers they need to notify.
package room

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
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
	ErrNotFound   = errors.New("room not found")
	ErrFull       = errors.New("room full")
	ErrBusy       = errors.New("too many rooms")
	ErrInRoom     = errors.New("already in a room")
	ErrNoPeer     = errors.New("no such peer")
	ErrNotAllowed = errors.New("signal not allowed between these peers")
	ErrNotInRoom  = errors.New("not in a room")
)

// Peer is one connected player. Send must never block.
type Peer struct {
	ID   int32
	Name string
	Send func(msg any)

	room *Room // guarded by Manager.mu
}

type Params struct {
	Topology   string // "star" or "mesh"
	MaxPlayers int
	MaxRooms   int // per bucket
	CodeLength int
}

type Room struct {
	Bucket   string
	Code     string
	Topology string
	Max      int
	Created  time.Time

	host  *Peer
	peers map[int32]*Peer
}

type Manager struct {
	mu    sync.Mutex
	rooms map[string]map[string]*Room // bucket -> code -> room
}

func NewManager() *Manager { return &Manager{rooms: map[string]map[string]*Room{}} }

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
	var code string
	for {
		code = NewCode(p.CodeLength)
		if _, taken := codes[code]; !taken {
			break
		}
	}
	host.ID = HostID
	r := &Room{
		Bucket: bucketID, Code: code, Topology: p.Topology, Max: p.MaxPlayers,
		Created: time.Now(), host: host, peers: map[int32]*Peer{HostID: host},
	}
	host.room = r
	codes[code] = r
	return r, nil
}

// Join adds p to the room and returns the peers that were already there.
func (m *Manager) Join(bucketID, code string, p *Peer) (*Room, []*Peer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p.room != nil {
		return nil, nil, ErrInRoom
	}
	r := m.rooms[bucketID][code]
	if r == nil {
		return nil, nil, ErrNotFound
	}
	if len(r.peers) >= r.Max {
		return nil, nil, ErrFull
	}
	existing := r.snapshot()
	for {
		id := randomID()
		if _, taken := r.peers[id]; !taken {
			p.ID = id
			break
		}
	}
	r.peers[p.ID] = p
	p.room = r
	return r, existing, nil
}

// LeaveResult says who must be told. If Closed, the room is gone and Others are all its
// former members; otherwise Others are the remaining members.
type LeaveResult struct {
	Room   *Room
	Closed bool
	Left   int32
	Others []*Peer
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
	delete(r.peers, p.ID)
	res := LeaveResult{Room: r, Left: p.ID}
	if p == r.host {
		res.Closed = true
		for _, o := range r.peers {
			o.room = nil
		}
		delete(m.rooms[r.Bucket], r.Code)
	}
	res.Others = r.snapshot()
	if res.Closed {
		r.peers = map[int32]*Peer{}
	}
	return res, true
}

// Route finds the target of a signal from p, enforcing topology rules.
func (m *Manager) Route(from *Peer, to int32) (*Peer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := from.room
	if r == nil {
		return nil, ErrNotInRoom
	}
	t := r.peers[to]
	if t == nil || t == from {
		return nil, ErrNoPeer
	}
	if r.Topology == "star" && from != r.host && t != r.host {
		return nil, ErrNotAllowed
	}
	return t, nil
}

// Members returns a snapshot of p's room (nil if none).
func (m *Manager) Members(p *Peer) []*Peer {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p.room == nil {
		return nil
	}
	return p.room.snapshot()
}

// CloseRoom removes a room and returns its members (all detached).
func (m *Manager) CloseRoom(bucketID, code string) ([]*Peer, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.rooms[bucketID][code]
	if r == nil {
		return nil, false
	}
	return m.detach(r), true
}

// CloseBucket removes every room of a bucket and returns all members.
func (m *Manager) CloseBucket(bucketID string) []*Peer {
	m.mu.Lock()
	defer m.mu.Unlock()
	var all []*Peer
	for _, r := range m.rooms[bucketID] {
		all = append(all, m.detach(r)...)
	}
	return all
}

func (m *Manager) detach(r *Room) []*Peer {
	ps := r.snapshot()
	for _, p := range ps {
		p.room = nil
	}
	r.peers = map[int32]*Peer{}
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
			out = append(out, Info{b, r.Code, len(r.peers), time.Since(r.Created).Seconds()})
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
