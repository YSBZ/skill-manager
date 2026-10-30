package contentgithub

import (
	"context"
	"net/http"
	"sync"
	"time"

	"skillmanage/internal/content"
)

type Storage interface {
	Upsert(context.Context, content.Entry) error
	Delete(context.Context, string) error
	Get(context.Context, string) (content.Entry, error)
	All(context.Context, string) ([]content.Entry, error)
	SaveSnapshot(context.Context, int64, time.Time, int) error
	Snapshots(context.Context, int64) ([]content.Snapshot, error)
	SaveCoverage(context.Context, content.Coverage) error
	SaveCheckpoint(context.Context, string, content.SourceCheckpoint) error
	Checkpoints(context.Context) ([]content.SourceCheckpoint, error)
}

type Config struct {
	QueryVersion                    string
	Queries                         []string
	MaxCandidates, MaxPagesPerQuery int
	PageSize                        int
	CoreRequestsPerHour             int
	MaxSkillsPerRepository          int
	DiscoveryInterval               time.Duration
}

func DefaultConfig() Config {
	return Config{
		QueryVersion: "github-v1",
		Queries: []string{
			"topic:agent-skills is:public archived:false fork:false",
			"topic:claude-skills is:public archived:false fork:false",
			"topic:codex-skills is:public archived:false fork:false",
			"SKILL.md in:readme is:public archived:false fork:false",
			"agent workflow in:name,description,readme is:public archived:false fork:false",
			"mcp server in:name,description,readme is:public archived:false fork:false",
		},
		// Small pages avoid GitHub Search timeouts on broad, recently-updated queries.
		MaxCandidates: 100, MaxPagesPerQuery: 2, PageSize: 10,
		CoreRequestsPerHour: 40, MaxSkillsPerRepository: 20,
		DiscoveryInterval: 24 * time.Hour,
	}
}

type Client struct {
	baseURL string
	hc      *http.Client
	token   string

	coreBudget int
	searchGate chan struct{}

	mu                                                     sync.Mutex
	coreBucket, searchBucket                               time.Time
	coreCount, searchCount                                 int
	coreLimit, coreRemaining, searchLimit, searchRemaining int
	coreReset, searchReset, coreRetryAt, searchRetryAt     time.Time
	searchLastReset                                        time.Time
}

type Collector struct {
	store          Storage
	client         *Client
	cfg            Config
	mu             sync.Mutex
	lastDiscovery  time.Time
	candidates     []repositoryCandidate
	search         *searchState
	discoveryCycle int
	cursor         int
}

// These domain types are shared with search.go, which owns their behavior and
// GitHub response DTOs.
type repositoryCandidate struct {
	ID          int64
	FullName    string
	HTMLURL     string
	Description string
	Topics      []string
	Fork        bool
}

type searchState struct {
	cycle, query, page int
	sortBy             string
	rowsByQuery        [][]repositoryCandidate
	querySeen          map[int64]bool
}

func NewCollector(store Storage, client *Client, cfg Config) *Collector {
	defaults := DefaultConfig()
	if cfg.QueryVersion == "" {
		cfg = defaults
	}
	if len(cfg.Queries) == 0 {
		cfg.Queries = append([]string(nil), defaults.Queries...)
	}
	if cfg.MaxCandidates < 1 || cfg.MaxCandidates > defaults.MaxCandidates {
		cfg.MaxCandidates = defaults.MaxCandidates
	}
	if cfg.MaxPagesPerQuery < 1 || cfg.MaxPagesPerQuery > defaults.MaxPagesPerQuery {
		cfg.MaxPagesPerQuery = defaults.MaxPagesPerQuery
	}
	if cfg.PageSize < 1 || cfg.PageSize > 30 {
		cfg.PageSize = defaults.PageSize
	}
	if cfg.CoreRequestsPerHour < 1 || cfg.CoreRequestsPerHour > defaults.CoreRequestsPerHour {
		cfg.CoreRequestsPerHour = defaults.CoreRequestsPerHour
	}
	if cfg.MaxSkillsPerRepository < 1 || cfg.MaxSkillsPerRepository > defaults.MaxSkillsPerRepository {
		cfg.MaxSkillsPerRepository = defaults.MaxSkillsPerRepository
	}
	if cfg.DiscoveryInterval <= 0 {
		cfg.DiscoveryInterval = defaults.DiscoveryInterval
	}
	cfg.Queries = append([]string(nil), cfg.Queries...)
	if client != nil {
		client.mu.Lock()
		client.coreBudget = cfg.CoreRequestsPerHour
		client.mu.Unlock()
	}
	return &Collector{store: store, client: client, cfg: cfg}
}
