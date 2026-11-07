package bucket_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"p2p-signaling-server/internal/bucket"
	"p2p-signaling-server/internal/store/memory"
)

func newService(t *testing.T) (*bucket.Service, *memory.Store) {
	t.Helper()
	st := memory.New()
	svc := bucket.NewService(st)
	if _, err := svc.Create(t.Context(), "test", "game", json.RawMessage(`{"name":"Game"}`)); err != nil {
		t.Fatal(err)
	}
	return svc, st
}

func TestConcurrentPatchesOfDifferentFieldsAllLand(t *testing.T) {
	svc, _ := newService(t)
	patches := []string{
		`{"notes":"one"}`,
		`{"max_players":5}`,
		`{"host_approval":true}`,
		`{"name":"Renamed"}`,
		`{"room_idle_timeout_seconds":120}`,
	}
	var wg sync.WaitGroup
	errs := make(chan error, len(patches))
	for _, p := range patches {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.Patch(t.Context(), "test", "game", json.RawMessage(p))
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("a concurrent patch failed: %v", err)
		}
	}
	b, _ := svc.Get("game")
	if b.Notes != "one" || b.MaxPlayers != 5 || !b.HostApproval || b.Name != "Renamed" || b.RoomIdleTimeoutSeconds != 120 {
		t.Fatalf("an edit was lost: %+v", b)
	}
	if b.Revision != int64(1+len(patches)) {
		t.Fatalf("every saved change must bump the revision once, got %d", b.Revision)
	}
}

// flaky fails the first Updates with a conflict, as if someone else saved in between.
type flaky struct {
	*memory.Store
	conflicts atomic.Int32
}

func (f *flaky) Update(ctx context.Context, b bucket.Bucket, rev int64) error {
	if f.conflicts.Add(-1) >= 0 {
		return bucket.ErrConflict
	}
	return f.Store.Update(ctx, b, rev)
}

func TestPatchRetriesOnAConflictAndGivesUpEventually(t *testing.T) {
	f := &flaky{Store: memory.New()}
	svc := bucket.NewService(f)
	if _, err := svc.Create(t.Context(), "test", "game", json.RawMessage(`{"name":"Game"}`)); err != nil {
		t.Fatal(err)
	}

	f.conflicts.Store(2)
	if _, err := svc.Patch(t.Context(), "test", "game", json.RawMessage(`{"notes":"x"}`)); err != nil {
		t.Fatalf("two conflicts are retried: %v", err)
	}
	f.conflicts.Store(1000)
	_, err := svc.Patch(t.Context(), "test", "game", json.RawMessage(`{"notes":"y"}`))
	if !errors.Is(err, bucket.ErrConflict) {
		t.Fatalf("endless conflicts must surface as ErrConflict, got %v", err)
	}
}

func TestStoreRefusesAStaleRevision(t *testing.T) {
	st := memory.New()
	b := bucket.Default("game", "Game")
	b.Revision = 3
	if err := st.Create(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	b.Revision = 4
	if err := st.Update(t.Context(), b, 2); !errors.Is(err, bucket.ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
	if err := st.Update(t.Context(), b, 3); err != nil {
		t.Fatal(err)
	}
}

func TestPatchNeverMutatesWhatReadersHold(t *testing.T) {
	svc, _ := newService(t)
	if _, err := svc.Patch(t.Context(), "test", "game", json.RawMessage(`{"accepted_versions":["1","2","3"]}`)); err != nil {
		t.Fatal(err)
	}
	held, _ := svc.Get("game") // what a live connection might be reading
	if _, err := svc.Patch(t.Context(), "test", "game", json.RawMessage(`{"accepted_versions":["9"]}`)); err != nil {
		t.Fatal(err)
	}
	if strings.Join(held.AcceptedVersions, ",") != "1,2,3" {
		t.Fatalf("a previously returned bucket changed under the reader: %v", held.AcceptedVersions)
	}
}

func TestCredentialsAreKeptWhenTheMaskIsEchoedBack(t *testing.T) {
	svc, st := newService(t)
	ctx := t.Context()
	_, err := svc.Patch(ctx, "test", "game", json.RawMessage(
		`{"ice_servers":[{"urls":["turn:t.example.com:3478"],"username":"u","credential":"s3cret"}]}`))
	if err != nil {
		t.Fatal(err)
	}

	// An admin reads the redacted bucket and writes it back unchanged.
	shown, _ := svc.Get("game")
	echo, _ := json.Marshal(map[string]any{"ice_servers": shown.Redacted().IceServers, "notes": "edited"})
	if _, err := svc.Patch(ctx, "test", "game", echo); err != nil {
		t.Fatalf("echoing the redacted servers back must work: %v", err)
	}
	got, _ := svc.Get("game")
	if got.IceServers[0].Credential != "s3cret" || got.Notes != "edited" {
		t.Fatalf("the real credential must survive: %+v", got.IceServers)
	}

	// The mask for a server that has no stored credential cannot be restored.
	bad := `{"ice_servers":[{"urls":["turn:other.example.com:3478"],"username":"u","credential":"` + bucket.RedactedMark + `"}]}`
	_, err = svc.Patch(ctx, "test", "game", json.RawMessage(bad))
	var ve *bucket.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want a validation error, got %v", err)
	}

	// Neither the audit log nor a preview ever holds the real credential.
	entries, _ := st.RecentAudit(ctx, 50)
	for _, e := range entries {
		for _, side := range []*bucket.Bucket{e.Before, e.After} {
			if side == nil {
				continue
			}
			for _, s := range side.IceServers {
				if s.Credential == "s3cret" {
					t.Fatalf("audit entry %q contains a real credential", e.Action)
				}
			}
		}
	}
	prev, err := svc.Preview("game", json.RawMessage(`{"notes":"p"}`))
	if err != nil {
		t.Fatal(err)
	}
	if prev.IceServers[0].Credential != bucket.RedactedMark {
		t.Fatalf("a preview must be redacted: %+v", prev.IceServers)
	}
}

func TestOlderStoredBucketsKeepWorking(t *testing.T) {
	st := memory.New()
	legacy := bucket.Default("old-game", "Old")
	legacy.Limits.FailedJoinsPerConnection = 0 // fields that did not exist when it was saved
	legacy.Limits.FailedJoinsPerMinute = 0
	legacy.Revision = 0
	if err := st.Create(t.Context(), legacy); err != nil {
		t.Fatal(err)
	}
	svc := bucket.NewService(st)
	if err := svc.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	b, _ := svc.Get("old-game")
	if b.Limits.FailedJoinsPerConnection == 0 || b.Limits.FailedJoinsPerMinute == 0 {
		t.Fatalf("limits not normalized on load: %+v", b.Limits)
	}
	if _, err := svc.Patch(t.Context(), "test", "old-game", json.RawMessage(`{"notes":"hi"}`)); err != nil {
		t.Fatalf("a bucket saved before revisions existed must still be editable: %v", err)
	}
}

type failingAudit struct{ *memory.Store }

func (failingAudit) AppendAudit(context.Context, bucket.AuditEntry) error {
	return errors.New("disk full")
}

func TestAuditFailureIsReportedNotSwallowed(t *testing.T) {
	svc := bucket.NewService(failingAudit{memory.New()})
	var reported atomic.Int32
	svc.OnAuditError = func(error) { reported.Add(1) }
	if _, err := svc.Create(t.Context(), "test", "game", json.RawMessage(`{"name":"Game"}`)); err != nil {
		t.Fatalf("the change itself must stand: %v", err)
	}
	if reported.Load() != 1 {
		t.Fatal("a failed audit write must be reported")
	}
}
