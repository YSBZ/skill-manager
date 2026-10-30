package contentgithub

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"skillmanage/internal/content"
)

// Refresh discovers repositories periodically and refreshes the current fair
// candidate window without replacing cached projects with partial evidence.
func (c *Collector) Refresh(ctx context.Context, now time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.store == nil || c.client == nil {
		return errors.New("github collector is not configured")
	}
	entries, err := c.store.All(ctx, "project")
	if err != nil {
		return storageFailure(err)
	}
	if len(c.candidates) == 0 {
		for _, entry := range entries {
			if entry.Project != nil && !entry.Project.Inactive && entry.Project.RepositoryID > 0 {
				c.candidates = append(c.candidates, repositoryCandidate{
					ID: entry.Project.RepositoryID, FullName: entry.Project.Repo,
				})
			}
		}
	}

	var sourceErr, storeErr error
	if c.lastDiscovery.IsZero() || now.Sub(c.lastDiscovery) >= c.cfg.DiscoveryInterval {
		rows, state, discoverErr := c.discover(ctx, now)
		if discoverErr == nil {
			// Only a complete search can retire projects outside the monitored
			// pool. Keep their cached evidence so later reentry is recoverable.
			pool := make(map[int64]bool, len(rows))
			for _, candidate := range rows {
				pool[candidate.ID] = true
			}
			for _, entry := range entries {
				if entry.Project != nil && !pool[entry.Project.RepositoryID] {
					if err := c.deactivateProject(ctx, entry.ID); err != nil {
						return err
					}
				}
			}
			c.candidates, c.search, c.lastDiscovery = rows, nil, now
		} else {
			sourceErr = errors.Join(sourceErr, discoverErr)
			if state != nil {
				c.search = state
				c.bootstrapCompletedQueries(state)
			}
		}
	}

	sort.Slice(c.candidates, func(i, j int) bool {
		return c.candidates[i].ID < c.candidates[j].ID
	})
	updated := 0
	for visited := 0; visited < len(c.candidates); visited++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		i := c.cursor % len(c.candidates)
		ok, err := c.processCandidate(ctx, now, c.candidates[i])
		if isRateLimitPause(err, now) {
			// Retry the incomplete candidate once the budget or server limit resets.
			c.cursor = i
		} else {
			c.cursor = (i + 1) % len(c.candidates)
		}
		if err != nil {
			var storageErr *storageError
			if errors.As(err, &storageErr) {
				storeErr = errors.Join(storeErr, err)
			} else {
				sourceErr = errors.Join(sourceErr, err)
			}
			break
		}
		if ok {
			updated++
		}
	}

	entries, err = c.store.All(ctx, "project")
	if err != nil {
		storeErr = errors.Join(storeErr, storageFailure(err))
		entries = nil
	}
	complete := 0
	for _, entry := range entries {
		if entry.Project != nil && !entry.Project.Inactive && entry.Project.Growth != nil &&
			entry.Project.Growth.Metric == "stars_added" && entry.Project.Growth.Complete {
			complete++
		}
	}
	coverageErr := storageFailure(c.store.SaveCoverage(ctx, content.Coverage{
		QueryVersion: c.cfg.QueryVersion, Candidates: len(c.candidates),
		Updated: updated, CompleteGrowth: complete,
	}))
	checkpointErr := c.saveCheckpoint(ctx, now, errors.Join(sourceErr, storeErr, coverageErr), updated > 0)
	return errors.Join(storeErr, coverageErr, checkpointErr, sourceErr)
}

// Completed query groups can supply positive candidates before the whole search
// finishes. They never establish absence or replace the previous monitored pool.
func (c *Collector) bootstrapCompletedQueries(state *searchState) {
	if state.query == 0 {
		return
	}
	var nextID int64
	if len(c.candidates) > 0 {
		nextID = c.candidates[c.cursor%len(c.candidates)].ID
	} else {
		// Give the first completed query's highest-ranked recent hit its first
		// inspection before ID sorting favors older repositories.
		for _, group := range state.rowsByQuery[:state.query] {
			if len(group) > 0 {
				nextID = group[0].ID
				break
			}
		}
	}
	seen := make(map[int64]bool, len(c.candidates))
	for _, candidate := range c.candidates {
		seen[candidate.ID] = true
	}
	for _, candidate := range mergeCandidateGroups(state.rowsByQuery[:state.query], c.cfg.MaxCandidates) {
		if len(c.candidates) >= c.cfg.MaxCandidates {
			break
		}
		if !seen[candidate.ID] {
			c.candidates = append(c.candidates, candidate)
			seen[candidate.ID] = true
		}
	}
	sort.Slice(c.candidates, func(i, j int) bool { return c.candidates[i].ID < c.candidates[j].ID })
	for i, candidate := range c.candidates {
		if candidate.ID == nextID {
			c.cursor = i
			break
		}
	}
}

func (c *Collector) processCandidate(ctx context.Context, now time.Time, candidate repositoryCandidate) (bool, error) {
	if candidate.ID <= 0 {
		return false, errors.New("invalid github repository candidate id")
	}
	if _, _, err := repositoryNameParts(candidate.FullName); err != nil {
		return false, err
	}

	inspection, inspectErr := c.client.inspectRepository(ctx, now, candidate)
	metadata := inspection.Metadata
	if metadata.ID > 0 && !metadata.Archived && metadata.Stars >= 0 {
		if err := c.store.SaveSnapshot(ctx, metadata.ID, now, metadata.Stars); err != nil {
			return false, &storageError{cause: errors.Join(err, inspectErr)}
		}
	}
	if inspectErr != nil {
		var requestErr *requestError
		if metadata.ID == 0 && errors.As(inspectErr, &requestErr) && requestErr.status == 404 {
			if err := c.store.Delete(ctx, projectID(candidate.ID)); err != nil {
				return false, storageFailure(err)
			}
			return false, nil
		}
		return false, inspectErr
	}
	if metadata.ID != candidate.ID {
		return false, errors.New("github repository id does not match candidate")
	}
	if metadata.Archived {
		if err := c.store.Delete(ctx, projectID(candidate.ID)); err != nil {
			return false, storageFailure(err)
		}
		return false, nil
	}
	if !inspection.Tree.Complete || !inspection.Complete {
		return false, nil
	}
	if metadata.Fork || inspection.Type == "" {
		return false, c.deactivateProject(ctx, projectID(candidate.ID))
	}
	if metadata.Stars < 0 {
		return false, errors.New("github repository has invalid star count")
	}
	if _, err := time.Parse(time.RFC3339Nano, metadata.CreatedAt); err != nil {
		return false, errors.New("github repository has invalid created_at")
	}

	growth, err := c.growth(ctx, now, metadata)
	if err != nil {
		return false, err
	}
	entry := normalizeProject(metadata, inspection.Skills, growth)
	oldEntry, err := c.store.Get(ctx, entry.ID)
	if err != nil && !errors.Is(err, content.ErrNotFound) && !errors.Is(err, content.ErrWithdrawn) {
		return false, storageFailure(err)
	}
	if err == nil {
		if oldEntry.Kind != "project" {
			return false, errors.New("github project identity conflicts with a non-project entry")
		}
		entry.DiscoveredAt = oldEntry.DiscoveredAt
	} else {
		entry.DiscoveredAt = now.UTC().Format(time.RFC3339)
	}
	if err := c.store.Upsert(ctx, entry); err != nil {
		return false, storageFailure(err)
	}
	return true, nil
}

func (c *Collector) deactivateProject(ctx context.Context, id string) error {
	entry, err := c.store.Get(ctx, id)
	if errors.Is(err, content.ErrNotFound) || errors.Is(err, content.ErrWithdrawn) {
		return nil
	}
	if err != nil {
		return storageFailure(err)
	}
	if entry.Project == nil || entry.Project.Inactive {
		return nil
	}
	project := *entry.Project
	project.Inactive = true
	entry.Project = &project
	return storageFailure(c.store.Upsert(ctx, entry))
}

func normalizeProject(metadata repositoryMetadata, skills []content.SkillRef, growth *content.Growth) content.Entry {
	canonicalURL := (&url.URL{Scheme: "https", Host: "github.com", Path: "/" + metadata.FullName}).String()
	summary := strings.TrimSpace(metadata.Description)
	if summary == "" {
		summary = "GitHub repository"
	}
	typeName, scenes := classifyRepository(metadata, skills)
	if skills == nil {
		skills = []content.SkillRef{}
	} else {
		skills = append([]content.SkillRef(nil), skills...)
	}
	if scenes == nil {
		scenes = []string{}
	} else {
		scenes = append([]string(nil), scenes...)
	}
	source := content.Source{Name: "GitHub", URL: canonicalURL}
	return content.Entry{
		ID: projectID(metadata.ID), Kind: "project", Title: metadata.FullName, Summary: summary,
		Source: source, Attribution: source, CanonicalURL: canonicalURL, Paragraphs: []content.Paragraph{},
		Project: &content.Project{
			RepositoryID: metadata.ID, Repo: metadata.FullName, Type: typeName, Stars: metadata.Stars,
			CreatedAt: metadata.CreatedAt, Scenes: scenes, Growth: growth, Skills: skills,
		},
	}
}

func (c *Collector) growth(ctx context.Context, now time.Time, metadata repositoryMetadata) (*content.Growth, error) {
	base, err := repositoryAPIPath(metadata.FullName)
	if err != nil {
		return nil, err
	}
	var raw json.RawMessage
	if err := c.client.getJSON(ctx, now, "core", base+"/stargazers/history", nil, &raw); err != nil {
		if !historyRequestMayFallback(err) && !errors.Is(err, errInvalidResponseJSON) {
			return nil, err
		}
		return c.snapshotGrowth(ctx, now, metadata.ID)
	}
	var rows []starHistoryWeek
	if err := json.Unmarshal(raw, &rows); err != nil {
		return c.snapshotGrowth(ctx, now, metadata.ID)
	}
	if growth := sevenDayGrowth(rows, now); growth != nil && growth.Metric == "stars_added" && growth.Complete {
		return growth, nil
	}
	return c.snapshotGrowth(ctx, now, metadata.ID)
}

func historyRequestMayFallback(err error) bool {
	var requestErr *requestError
	if !errors.As(err, &requestErr) || requestErr.rateLimited {
		return false
	}
	return requestErr.status == 401 || requestErr.status == 403 || requestErr.status == 404
}

func (c *Collector) snapshotGrowth(ctx context.Context, now time.Time, repositoryID int64) (*content.Growth, error) {
	rows, err := c.store.Snapshots(ctx, repositoryID)
	if err != nil {
		return nil, storageFailure(err)
	}
	valid := make([]content.Snapshot, 0, len(rows))
	for _, row := range rows {
		if row.RepositoryID == repositoryID {
			valid = append(valid, row)
		}
	}
	return snapshotGrowth(valid, now), nil
}

func (c *Collector) saveCheckpoint(ctx context.Context, now time.Time, sourceErr error, verifiedData bool) error {
	rows, err := c.store.Checkpoints(ctx)
	if err != nil {
		return storageFailure(err)
	}
	var checkpoint content.SourceCheckpoint
	for _, row := range rows {
		if row.Name == "github" {
			checkpoint = row
			break
		}
	}
	checkpoint.Name = "github"
	checkpoint.Status.Name = "github"
	if sourceErr == nil {
		checkpoint.FailureCount = 0
		checkpoint.NextFetchAt = now.Add(10 * time.Minute)
		checkpoint.Status.State = "fresh"
		checkpoint.Status.LastSuccessAt = now.UTC().Format(time.RFC3339)
		checkpoint.Status.RetryAt = ""
	} else {
		if verifiedData && checkpoint.Status.LastSuccessAt == "" {
			// A validated entry is usable cache even while discovery is pending.
			// Keep the failure state and backoff rather than claiming a full refresh.
			checkpoint.Status.LastSuccessAt = now.UTC().Format(time.RFC3339)
		}
		checkpoint.FailureCount++
		checkpoint.NextFetchAt = now.Add(10 * time.Minute)
		checkpoint.Status.RetryAt = ""
		if rateLimited, retryAt := latestRateLimitRetryAt(sourceErr, now); rateLimited {
			if !retryAt.After(now) {
				retryAt = now.Add(10 * time.Minute)
			}
			checkpoint.Status.State = "rate_limited"
			checkpoint.Status.RetryAt = retryAt.UTC().Format(time.RFC3339)
			checkpoint.NextFetchAt = retryAt
		} else {
			checkpoint.Status.State = "stale"
		}
	}
	if err := c.store.SaveCheckpoint(ctx, "github", checkpoint); err != nil {
		return storageFailure(err)
	}
	return nil
}

func latestRateLimitRetryAt(err error, now time.Time) (bool, time.Time) {
	limited := false
	var latest time.Time
	stack := []error{err}
	for len(stack) > 0 {
		last := len(stack) - 1
		current := stack[last]
		stack = stack[:last]
		if current == nil {
			continue
		}
		if current == errRequestBudget {
			limited = true
			budgetRetryAt := now.Add(10 * time.Minute)
			if budgetRetryAt.After(latest) {
				latest = budgetRetryAt
			}
		}
		if requestErr, ok := current.(*requestError); ok && requestErr.rateLimited {
			limited = true
			if requestErr.retryAt.After(now) && requestErr.retryAt.After(latest) {
				latest = requestErr.retryAt
			}
		}
		switch wrapped := current.(type) {
		case interface{ Unwrap() []error }:
			stack = append(stack, wrapped.Unwrap()...)
		case interface{ Unwrap() error }:
			stack = append(stack, wrapped.Unwrap())
		}
	}
	return limited, latest
}

func isRateLimitPause(err error, now time.Time) bool {
	if err == nil {
		return false
	}
	var storeErr *storageError
	if errors.As(err, &storeErr) {
		return false
	}
	paused, _ := latestRateLimitRetryAt(err, now)
	return paused
}

type storageError struct{ cause error }

func (e *storageError) Error() string { return "github storage operation failed" }
func (e *storageError) Unwrap() error { return e.cause }

func storageFailure(err error) error {
	if err == nil {
		return nil
	}
	var existing *storageError
	if errors.As(err, &existing) {
		return err
	}
	return &storageError{cause: err}
}

func projectID(repositoryID int64) string {
	return "project_" + strconv.FormatInt(repositoryID, 10)
}
