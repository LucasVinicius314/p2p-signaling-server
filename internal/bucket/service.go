package bucket

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"
)

// maxPatchAttempts bounds how often a patch is re-applied on top of a newer revision.
const maxPatchAttempts = 8

// Service is the single entry point for reading and managing buckets; REST and MCP both use it.
// Reads come from an in-memory cache; writes go to the store, then refresh the cache.
// Buckets returned here carry real ICE credentials; use Bucket.Redacted before showing them to an admin.
type Service struct {
	store Store
	now   func() time.Time

	// refreshMu serializes Refresh: two overlapping reloads could finish out of order and leave the cache
	// holding the older snapshot.
	refreshMu sync.Mutex

	mu     sync.RWMutex
	cache  map[string]Bucket
	loaded bool

	lastRefresh time.Time
	lastErr     string

	// OnAuditError, if set, is told when an audit entry could not be written (the change itself stands).
	OnAuditError func(error)
}

// Status describes the health of the config cache.
type Status struct {
	Loaded      bool      `json:"loaded"`
	LastRefresh time.Time `json:"last_refresh"`
	LastError   string    `json:"last_error,omitempty"`
}

// Status reports whether the cache has loaded and whether the last refresh succeeded.
func (s *Service) Status() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Status{s.loaded, s.lastRefresh, s.lastErr}
}

func NewService(store Store) *Service {
	return &Service{store: store, now: time.Now, cache: map[string]Bucket{}}
}

// Refresh reloads all buckets. On error the previous cache is kept.
func (s *Service) Refresh(ctx context.Context) error {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	list, err := s.store.List(ctx)
	if err != nil {
		s.mu.Lock()
		s.lastErr = err.Error()
		s.mu.Unlock()
		return err
	}
	m := make(map[string]Bucket, len(list))
	for _, b := range list {
		m[b.ID] = b.Normalize()
	}
	s.mu.Lock()
	s.cache, s.loaded = m, true
	s.lastRefresh, s.lastErr = s.now().UTC(), ""
	s.mu.Unlock()
	return nil
}

// Loaded reports whether the first successful load has happened.
func (s *Service) Loaded() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.loaded
}

// Poll refreshes every interval until ctx ends.
func (s *Service) Poll(ctx context.Context, interval time.Duration, onErr func(error)) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.Refresh(ctx); err != nil && onErr != nil {
				onErr(err)
			}
		}
	}
}

// Get returns a bucket from the cache. Treat the result as read-only: its slices are shared.
func (s *Service) Get(id string) (Bucket, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.cache[id]
	return b, ok
}

func (s *Service) List() []Bucket {
	s.mu.RLock()
	out := make([]Bucket, 0, len(s.cache))
	for _, b := range s.cache {
		out = append(out, b)
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Create applies overlay (a JSON object of fields) onto the defaults, validates and stores.
func (s *Service) Create(ctx context.Context, actor, id string, overlay json.RawMessage) (Bucket, error) {
	b := Default(id, "")
	if len(overlay) > 0 {
		if err := decodeStrict(overlay, &b); err != nil {
			return Bucket{}, &ValidationError{err}
		}
	}
	b.ID = id
	b.Revision = 1
	b.CreatedAt = s.now().UTC()
	b.UpdatedAt = b.CreatedAt
	if err := b.Validate(); err != nil {
		return Bucket{}, &ValidationError{err}
	}
	if err := s.store.Create(ctx, b); err != nil {
		return Bucket{}, err
	}
	s.audit(ctx, actor, "create_bucket", id, nil, &b)
	_ = s.Refresh(ctx)
	return b, nil
}

// Preview validates what Create (if id is new) or Patch (if it exists) would produce,
// without saving anything. The result is redacted.
func (s *Service) Preview(id string, fields json.RawMessage) (Bucket, error) {
	b, exists := s.Get(id)
	if exists {
		b = b.Clone()
	} else {
		b = Default(id, "")
	}
	old := b.Clone()
	if len(fields) > 0 {
		if err := decodeStrict(fields, &b); err != nil {
			return Bucket{}, &ValidationError{err}
		}
	}
	b.ID = id
	if err := restoreCredentials(old.IceServers, b.IceServers); err != nil {
		return Bucket{}, &ValidationError{err}
	}
	if err := b.Validate(); err != nil {
		return Bucket{}, &ValidationError{err}
	}
	return b.Redacted(), nil
}

// Patch merges a partial JSON update onto the current bucket and validates the whole. If the bucket
// changes underneath (another admin action at the same moment), the patch is re-applied on the newer
// revision, so concurrent edits of different fields both land.
func (s *Service) Patch(ctx context.Context, actor, id string, patch json.RawMessage) (Bucket, error) {
	for attempt := 1; ; attempt++ {
		old, ok := s.Get(id)
		if !ok {
			return Bucket{}, ErrNotFound
		}
		b := old.Clone() // never decode into the cached copy: decoding reuses slice storage
		if err := decodeStrict(patch, &b); err != nil {
			return Bucket{}, &ValidationError{err}
		}
		b.ID, b.CreatedAt, b.UpdatedAt = old.ID, old.CreatedAt, s.now().UTC()
		b.Revision = old.Revision + 1
		if err := restoreCredentials(old.IceServers, b.IceServers); err != nil {
			return Bucket{}, &ValidationError{err}
		}
		if err := b.Validate(); err != nil {
			return Bucket{}, &ValidationError{err}
		}
		err := s.store.Update(ctx, b, old.Revision)
		if errors.Is(err, ErrConflict) && attempt < maxPatchAttempts {
			if rerr := s.Refresh(ctx); rerr != nil {
				return Bucket{}, rerr
			}
			continue
		}
		if err != nil {
			return Bucket{}, err
		}
		s.audit(ctx, actor, "update_bucket", id, &old, &b)
		_ = s.Refresh(ctx)
		return b, nil
	}
}

func (s *Service) Delete(ctx context.Context, actor, id string) error {
	old, ok := s.Get(id)
	if !ok {
		return ErrNotFound
	}
	if err := s.store.Delete(ctx, id); err != nil {
		return err
	}
	s.audit(ctx, actor, "delete_bucket", id, &old, nil)
	_ = s.Refresh(ctx)
	return nil
}

func (s *Service) RecentAudit(ctx context.Context, limit int) ([]AuditEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return s.store.RecentAudit(ctx, limit)
}

// audit records a change with credentials masked. A failure to write it is reported, never silent.
func (s *Service) audit(ctx context.Context, actor, action, id string, before, after *Bucket) {
	redact := func(b *Bucket) *Bucket {
		if b == nil {
			return nil
		}
		r := b.Redacted()
		return &r
	}
	err := s.store.AppendAudit(ctx, AuditEntry{
		At: s.now().UTC(), Actor: actor, Action: action, BucketID: id, Before: redact(before), After: redact(after),
	})
	if err != nil && s.OnAuditError != nil {
		s.OnAuditError(err)
	}
}

// ValidationError marks client-fixable input problems.
type ValidationError struct{ Err error }

func (e *ValidationError) Error() string { return e.Err.Error() }
func (e *ValidationError) Unwrap() error { return e.Err }

func decodeStrict(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
