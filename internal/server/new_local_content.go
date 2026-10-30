package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"skillmanage/internal/content/aihot"
	"skillmanage/internal/content/sqlite"
	"skillmanage/internal/contentgithub"
	"skillmanage/internal/contentservice"
)

const (
	localAIHOTBaseURL  = "https://aihot.news"
	localGitHubBaseURL = "https://api.github.com"
	localContentDBName = "content.db"
)

// localContentUpstreams permits isolated HTTP fixtures while production keeps
// using the public source APIs. It is populated only by server package tests.
type localContentUpstreams struct {
	aihotURL    string
	githubURL   string
	githubToken string
}

// StartLocalContent explicitly starts the app-owned content cache and
// collectors when no shared content service URL is configured. A non-empty
// invalid URL is an error, while a valid remote URL preserves proxy mode.
func (s *Server) StartLocalContent(ctx context.Context) error {
	s.contentMu.Lock()
	defer s.contentMu.Unlock()
	if s.localContentClosed {
		return errors.New("server is already closed")
	}
	if s.localContent != nil {
		return nil
	}

	if configuredContentServiceURLValue() != "" {
		remote, err := configuredContentServiceURL()
		if err != nil {
			return fmt.Errorf("invalid configured content service URL: %w", err)
		}
		s.contentBaseURL = remote
		return nil
	}
	s.contentBaseURL = nil

	if ctx == nil {
		ctx = s.baseCtx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if s.localContentUpstreams.aihotURL == "" {
		s.localContentUpstreams.aihotURL = localAIHOTBaseURL
	}
	if s.localContentUpstreams.githubURL == "" {
		s.localContentUpstreams.githubURL = localGitHubBaseURL
	}
	if s.localContentUpstreams.githubToken == "" {
		s.localContentUpstreams.githubToken = os.Getenv("GITHUB_TOKEN")
	}

	dbPath := filepath.Join(s.centralDir, "content", localContentDBName)
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return fmt.Errorf("create local content cache directory: %w", err)
	}
	store, err := sqlite.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open local content cache: %w", err)
	}

	client := &http.Client{}
	aihotRefresher := aihot.NewRefresher(aihot.NewClient(s.localContentUpstreams.aihotURL, client), store)
	githubClient := contentgithub.NewClient(s.localContentUpstreams.githubURL, client, s.localContentUpstreams.githubToken)
	githubRefresher := contentgithub.NewCollector(store, githubClient, contentgithub.DefaultConfig())
	handler := contentservice.NewCatalogHandler(store, aihotRefresher.ResolveDetail)
	s.localContent = contentservice.NewLocalRuntime(ctx, store, handler, aihotRefresher, githubRefresher)
	return nil
}
