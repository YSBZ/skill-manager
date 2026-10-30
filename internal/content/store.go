package content

import (
	"context"
	"time"
)

type SourceCheckpoint struct {
	Name         string       `json:"name"`
	ETag         string       `json:"etag"`
	LastModified string       `json:"lastModified"`
	CacheControl string       `json:"cacheControl"`
	NextFetchAt  time.Time    `json:"nextFetchAt"`
	FailureCount int          `json:"failureCount"`
	Status       SourceStatus `json:"status"`
}

type Store interface {
	Upsert(context.Context, Entry) error
	Delete(context.Context, string) error
	Get(context.Context, string) (Entry, error)
	All(context.Context, string) ([]Entry, error)
	SaveSnapshot(context.Context, int64, time.Time, int) error
	Snapshots(context.Context, int64) ([]Snapshot, error)
	SaveCoverage(context.Context, Coverage) error
	LoadCoverage(context.Context) (Coverage, error)
}

type CatalogStore interface {
	Store
	ReplaceCollection(context.Context, string, []Entry) error
	Collection(context.Context, string) ([]Entry, error)
	HasMembership(context.Context, string) (bool, error)
	ExpireUnreferenced(context.Context, time.Time) error
	IsWithdrawn(context.Context, string) (bool, error)
	Version(context.Context) (int64, error)
	SaveCheckpoint(context.Context, string, SourceCheckpoint) error
	Checkpoints(context.Context) ([]SourceCheckpoint, error)
	SaveNotices(context.Context, []map[string]any) error
	LoadNotices(context.Context) ([]map[string]any, error)
}
