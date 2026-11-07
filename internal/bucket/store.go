package bucket

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("bucket not found")
	ErrExists   = errors.New("bucket already exists")
	// ErrConflict: the bucket changed since the revision an update was based on.
	ErrConflict = errors.New("bucket was changed by someone else; retry")
)

// AuditEntry records one admin change. Buckets are stored redacted (no ICE credentials).
type AuditEntry struct {
	At       time.Time `json:"at" bson:"at"`
	Actor    string    `json:"actor" bson:"actor"`
	Action   string    `json:"action" bson:"action"`
	BucketID string    `json:"bucket_id" bson:"bucket_id"`
	Before   *Bucket   `json:"before,omitempty" bson:"before,omitempty"`
	After    *Bucket   `json:"after,omitempty" bson:"after,omitempty"`
}

// Clone returns a copy that shares no slices with b, so it can be edited without touching the original.
func (b Bucket) Clone() Bucket {
	b.AcceptedVersions = append([]string{}, b.AcceptedVersions...)
	b.AllowedOrigins = append([]string{}, b.AllowedOrigins...)
	servers := make([]IceServer, len(b.IceServers))
	for i, s := range b.IceServers {
		s.URLs = append([]string{}, s.URLs...)
		servers[i] = s
	}
	b.IceServers = servers
	return b
}

// Store persists buckets and the audit log.
type Store interface {
	List(ctx context.Context) ([]Bucket, error)
	Create(ctx context.Context, b Bucket) error // ErrExists
	// Update replaces the stored bucket only if its stored revision equals expectedRevision
	// (ErrConflict otherwise, ErrNotFound if it does not exist). A stored bucket without a revision counts as 0.
	Update(ctx context.Context, b Bucket, expectedRevision int64) error
	Delete(ctx context.Context, id string) error // ErrNotFound
	AppendAudit(ctx context.Context, e AuditEntry) error
	RecentAudit(ctx context.Context, limit int) ([]AuditEntry, error)
}
