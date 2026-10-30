package content

import "time"

type Snapshot struct {
	RepositoryID int64
	At           time.Time
	Stars        int
}

type Query struct {
	Kind     string
	View     string
	Window   string
	Q        string
	Category string
	Scene    string
	Limit    int
	Cursor   string
}

type Source struct {
	Name string `json:"name"`
	URL  string `json:"url,omitempty"`
}

type Paragraph struct {
	Heading     string  `json:"heading,omitempty"`
	Text        string  `json:"text"`
	URL         string  `json:"url,omitempty"`
	Source      *Source `json:"source,omitempty"`
	Attribution *Source `json:"attribution,omitempty"`
}

type SkillRef struct {
	RepositoryID int64  `json:"repositoryId"`
	Repo         string `json:"repo"`
	Path         string `json:"path"`
	Name         string `json:"name"`
	Verified     bool   `json:"verified"`
}

type Growth struct {
	Metric       string `json:"metric"`
	Value        int    `json:"value"`
	WindowStart  string `json:"windowStart"`
	WindowEnd    string `json:"windowEnd"`
	ObservedDays int    `json:"observedDays"`
	Complete     bool   `json:"complete"`
}

type Project struct {
	// Inactive retains cached evidence after a complete discovery/classification
	// excludes a repository. Absence preserves legacy usable cache membership.
	Inactive     bool       `json:"inactive,omitempty"`
	RepositoryID int64      `json:"repositoryId"`
	Repo         string     `json:"repo"`
	Type         string     `json:"type"`
	Stars        int        `json:"stars"`
	CreatedAt    string     `json:"createdAt"`
	Scenes       []string   `json:"scenes"`
	Growth       *Growth    `json:"growth,omitempty"`
	Skills       []SkillRef `json:"skills"`
}

type Entry struct {
	ID           string      `json:"id"`
	Kind         string      `json:"kind"`
	Title        string      `json:"title"`
	Summary      string      `json:"summary"`
	Reason       string      `json:"reason,omitempty"`
	Category     string      `json:"category,omitempty"`
	Source       Source      `json:"source"`
	Attribution  Source      `json:"attribution"`
	CanonicalURL string      `json:"canonicalUrl"`
	OriginalURL  string      `json:"originalUrl,omitempty"`
	PublishedAt  string      `json:"publishedAt,omitempty"`
	DiscoveredAt string      `json:"discoveredAt,omitempty"`
	Selected     bool        `json:"selected,omitempty"`
	Period       string      `json:"period,omitempty"`
	Paragraphs   []Paragraph `json:"paragraphs"`
	Project      *Project    `json:"project,omitempty"`
}

type Coverage struct {
	QueryVersion   string `json:"queryVersion"`
	Candidates     int    `json:"candidates"`
	Updated        int    `json:"updated"`
	CompleteGrowth int    `json:"completeGrowth"`
}

type SourceStatus struct {
	Name          string `json:"name"`
	State         string `json:"state"`
	LastSuccessAt string `json:"lastSuccessAt,omitempty"`
	RetryAt       string `json:"retryAt,omitempty"`
}

type Meta struct {
	FetchedAt       string           `json:"fetchedAt"`
	SourceUpdatedAt string           `json:"sourceUpdatedAt,omitempty"`
	Stale           bool             `json:"stale"`
	Version         string           `json:"version"`
	Coverage        Coverage         `json:"coverage"`
	Sources         []SourceStatus   `json:"sources"`
	Notices         []map[string]any `json:"notices"`
}

type Page struct {
	Items      []Entry `json:"items"`
	NextCursor string  `json:"nextCursor,omitempty"`
	Meta       Meta    `json:"meta"`
}

type EntryResult struct {
	Entry Entry `json:"entry"`
	Meta  Meta  `json:"meta"`
}

type APIError struct {
	ErrorCode string `json:"error_code"`
	Error     string `json:"error"`
}
