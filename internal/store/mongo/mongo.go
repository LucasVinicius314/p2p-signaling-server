// Package mongo is the MongoDB Atlas bucket.Store.
package mongo

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	driver "go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"p2p-signaling-server/internal/bucket"
)

type Store struct {
	client  *driver.Client
	buckets *driver.Collection
	audit   *driver.Collection
}

const auditTTL = 90 * 24 * time.Hour

// Connect opens the client, pings, and ensures the audit TTL index.
func Connect(ctx context.Context, uri, db string) (*Store, error) {
	c, err := driver.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return nil, err
	}
	pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := c.Ping(pctx, nil); err != nil {
		_ = c.Disconnect(ctx)
		return nil, err
	}
	d := c.Database(db)
	s := &Store{client: c, buckets: d.Collection("buckets"), audit: d.Collection("audit_log")}
	_, err = s.audit.Indexes().CreateOne(pctx, driver.IndexModel{
		Keys:    bson.D{{Key: "at", Value: 1}},
		Options: options.Index().SetExpireAfterSeconds(int32(auditTTL.Seconds())),
	})
	if err != nil {
		_ = c.Disconnect(ctx)
		return nil, err
	}
	return s, nil
}

func (s *Store) Close(ctx context.Context) error { return s.client.Disconnect(ctx) }

func (s *Store) List(ctx context.Context) ([]bucket.Bucket, error) {
	cur, err := s.buckets.Find(ctx, bson.D{})
	if err != nil {
		return nil, err
	}
	var out []bucket.Bucket
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) Create(ctx context.Context, b bucket.Bucket) error {
	_, err := s.buckets.InsertOne(ctx, b)
	if driver.IsDuplicateKeyError(err) {
		return bucket.ErrExists
	}
	return err
}

func (s *Store) Update(ctx context.Context, b bucket.Bucket) error {
	r, err := s.buckets.ReplaceOne(ctx, bson.D{{Key: "_id", Value: b.ID}}, b)
	if err != nil {
		return err
	}
	if r.MatchedCount == 0 {
		return bucket.ErrNotFound
	}
	return nil
}

func (s *Store) Delete(ctx context.Context, id string) error {
	r, err := s.buckets.DeleteOne(ctx, bson.D{{Key: "_id", Value: id}})
	if err != nil {
		return err
	}
	if r.DeletedCount == 0 {
		return bucket.ErrNotFound
	}
	return nil
}

func (s *Store) AppendAudit(ctx context.Context, e bucket.AuditEntry) error {
	_, err := s.audit.InsertOne(ctx, e)
	return err
}

func (s *Store) RecentAudit(ctx context.Context, limit int) ([]bucket.AuditEntry, error) {
	cur, err := s.audit.Find(ctx, bson.D{}, options.Find().SetSort(bson.D{{Key: "at", Value: -1}}).SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	out := []bucket.AuditEntry{}
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}
