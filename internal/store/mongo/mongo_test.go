//go:build mongo

package mongo

import (
	"os"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

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
	b.Revision = 1
	if err := s.Update(ctx, b, 0); err != nil { // stored at revision 0
		t.Fatal(err)
	}
	b.Revision = 2
	if err := s.Update(ctx, b, 0); err != bucket.ErrConflict { // stale: the stored revision is 1 now
		t.Fatalf("want ErrConflict, got %v", err)
	}
	if err := s.Update(ctx, b, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(ctx, bucket.Default("no-such-bucket", "x"), 0); err != bucket.ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
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

	// A document saved before revisions existed has no revision field and must still be updatable.
	if _, err := s.buckets.InsertOne(ctx, bson.D{{Key: "_id", Value: "legacy-doc"}, {Key: "name", Value: "Legacy"}}); err != nil {
		t.Fatal(err)
	}
	legacy := bucket.Default("legacy-doc", "Legacy")
	legacy.Revision = 1
	if err := s.Update(ctx, legacy, 0); err != nil {
		t.Fatalf("legacy document without a revision: %v", err)
	}
}
