// Package memory is an in-memory bucket.Store for tests and local runs.
package memory

import (
	"context"
	"sync"

	"p2p-signaling-server/internal/bucket"
)

type Store struct {
	mu    sync.Mutex
	items map[string]bucket.Bucket
	audit []bucket.AuditEntry
}

func New() *Store { return &Store{items: map[string]bucket.Bucket{}} }

func (s *Store) List(context.Context) ([]bucket.Bucket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]bucket.Bucket, 0, len(s.items))
	for _, b := range s.items {
		out = append(out, b)
	}
	return out, nil
}

func (s *Store) Create(_ context.Context, b bucket.Bucket) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.items[b.ID]; ok {
		return bucket.ErrExists
	}
	s.items[b.ID] = b
	return nil
}

func (s *Store) Update(_ context.Context, b bucket.Bucket) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.items[b.ID]; !ok {
		return bucket.ErrNotFound
	}
	s.items[b.ID] = b
	return nil
}

func (s *Store) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.items[id]; !ok {
		return bucket.ErrNotFound
	}
	delete(s.items, id)
	return nil
}

func (s *Store) AppendAudit(_ context.Context, e bucket.AuditEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audit = append(s.audit, e)
	return nil
}

func (s *Store) RecentAudit(_ context.Context, limit int) ([]bucket.AuditEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []bucket.AuditEntry{}
	for i := len(s.audit) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, s.audit[i])
	}
	return out, nil
}
