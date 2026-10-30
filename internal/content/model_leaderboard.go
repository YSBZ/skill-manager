package content

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	ModelLeaderboardOverall      = "overall"
	ModelLeaderboardCoding       = "coding"
	ModelLeaderboardReasoning    = "reasoning"
	ModelLeaderboardProfessional = "professional"
	ModelLeaderboardKnowledge    = "knowledge"
)

var ErrInvalidModelLeaderboardCategory = errors.New("invalid model leaderboard category")

// ModelLeaderboard represents one complete AIHOT model leaderboard snapshot.
// Entries retain the source rank and the price labels exactly as published.
type ModelLeaderboard struct {
	Category     string                  `json:"category"`
	Title        string                  `json:"title"`
	SourceURL    string                  `json:"sourceURL"`
	UpdatedLabel string                  `json:"updatedLabel"`
	FetchedAt    string                  `json:"fetchedAt"`
	Currency     string                  `json:"currency"`
	PriceUnit    string                  `json:"priceUnit"`
	Entries      []ModelLeaderboardEntry `json:"entries"`
	PriceNotes   []string                `json:"priceNotes"`
	Meta         ModelLeaderboardMeta    `json:"meta"`
}

type ModelLeaderboardEntry struct {
	ID              string   `json:"id"`
	Rank            int      `json:"rank"`
	Name            string   `json:"name"`
	Provider        string   `json:"provider"`
	ReleasedOn      string   `json:"releasedOn"`
	CoveragePercent *float64 `json:"coveragePercent"`
	BenchmarkCount  *int     `json:"benchmarkCount"`
	Score           float64  `json:"score"`
	CachePrice      string   `json:"cachePrice"`
	InputPrice      string   `json:"inputPrice"`
	OutputPrice     string   `json:"outputPrice"`
	URL             string   `json:"url"`
	OpenWeightsURL  string   `json:"openWeightsURL"`
}

type ModelLeaderboardMeta struct {
	Stale     bool   `json:"stale"`
	LastError string `json:"lastError"`
}

// ModelLeaderboardStore persists each complete category snapshot separately.
// Successful replacements include the source checkpoint in the same commit.
type ModelLeaderboardStore interface {
	SaveModelLeaderboard(context.Context, ModelLeaderboard, SourceCheckpoint) error
	LoadModelLeaderboard(context.Context, string) (ModelLeaderboard, error)
}

// ModelLeaderboardReader is an optional extension so existing Reader fakes
// and alternate backends do not need to implement model leaderboard support.
type ModelLeaderboardReader interface {
	ModelLeaderboard(context.Context, string) (ModelLeaderboard, error)
}

// NormalizeModelLeaderboardCategory returns the canonical category. An empty
// value means the default overview; HTTP handlers separately reject an
// explicitly supplied empty query value.
func NormalizeModelLeaderboardCategory(category string) (string, error) {
	if category == "" {
		return ModelLeaderboardOverall, nil
	}
	switch category {
	case ModelLeaderboardOverall, ModelLeaderboardCoding, ModelLeaderboardReasoning, ModelLeaderboardProfessional, ModelLeaderboardKnowledge:
		return category, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidModelLeaderboardCategory, category)
	}
}

// ModelLeaderboardSourceURL maps a whitelisted category to its fixed AIHOT
// public page. The result never depends on URL data supplied by a caller.
func ModelLeaderboardSourceURL(category string) (string, error) {
	category, err := NormalizeModelLeaderboardCategory(category)
	if err != nil {
		return "", err
	}
	switch category {
	case ModelLeaderboardOverall:
		return "https://aihot.news/leaderboard", nil
	default:
		return "https://aihot.news/leaderboard/category/" + category, nil
	}
}

// ModelLeaderboardFetchedAt records when this service successfully parsed the
// source page. The source's human update label is intentionally kept separate.
func ModelLeaderboardFetchedAt(now time.Time) string {
	return now.UTC().Format(time.RFC3339Nano)
}
