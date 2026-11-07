package bucket

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("bucket not found")
	ErrExists   = errors.New("bucket already exists")
)

type AuditEntry struct {
	At       time.Time `json:"at" bson:"at"`
	Actor    string    `json:"actor" bson:"actor"`
	Action   string    `json:"action" bson:"action"`
	BucketID string    `json:"bucket_id" bson:"bucket_id"`
	Before   *Bucket   `json:"before,omitempty" bson:"before,omitempty"`
	After    *Bucket   `json:"after,omitempty" bson:"after,omitempty"`
}

// Store persists buckets and the audit log.
type Store interface {
	List(ctx context.Context) ([]Bucket, error)
	Create(ctx context.Context, b Bucket) error  // ErrExists
	Update(ctx context.Context, b Bucket) error  // ErrNotFound
	Delete(ctx context.Context, id string) error // ErrNotFound
	AppendAudit(ctx context.Context, e AuditEntry) error
	RecentAudit(ctx context.Context, limit int) ([]AuditEntry, error)
}
