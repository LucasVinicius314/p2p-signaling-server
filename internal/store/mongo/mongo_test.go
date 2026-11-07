//go:build mongo

package mongo

import (
	"os"
	"strings"
	"testing"
	"time"

	"p2p-signaling-server/internal/bucket"
)

// Run with: MONGODB_URI=... go test -tags mongo ./internal/store/mongo
func TestRoundTrip(t *testing.T) {
	uri := os.Getenv("MONGODB_URI")
	if uri == "" {
		t.Skip("MONGODB_URI not set")
	}
	ctx := t.Context()
	s, err := Connect(ctx, uri, "p2p_signaling_test")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)
	defer s.buckets.Drop(ctx)
	defer s.audit.Drop(ctx)

	b := bucket.Default("mongo-test", "Mongo Test")
	b.CreatedAt, b.UpdatedAt = time.Now().UTC().Truncate(time.Millisecond), time.Now().UTC().Truncate(time.Millisecond)
	if err := s.Create(ctx, b); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(ctx, b); err != bucket.ErrExists {
		t.Fatalf("want ErrExists, got %v", err)
	}
	b.Notes = "hi"
	if err := s.Update(ctx, b); err != nil {
		t.Fatal(err)
	}
	list, err := s.List(ctx)
	if err != nil || len(list) != 1 || list[0].Notes != "hi" || list[0].ID != "mongo-test" {
		t.Fatalf("list %v %v", list, err)
	}
	if err := s.AppendAudit(ctx, bucket.AuditEntry{At: time.Now().UTC(), Actor: "rest", Action: "x", BucketID: b.ID, After: &b}); err != nil {
		t.Fatal(err)
	}
	a, err := s.RecentAudit(ctx, 5)
	if err != nil || len(a) != 1 || !strings.Contains(a[0].Action, "x") {
		t.Fatalf("audit %v %v", a, err)
	}
	if err := s.Delete(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, b.ID); err != bucket.ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}
