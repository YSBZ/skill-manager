package aihot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"skillmanage/internal/content"
)

const (
	defaultFreshness     = 10 * time.Minute
	reportFreshness      = 30 * time.Minute
	leaderboardFreshness = time.Hour
	maxRateLimitBackoff  = time.Hour
	rateLimitBackoffStep = time.Minute
	unreferencedEntryAge = 7 * 24 * time.Hour
)

var errSourceRetryPending = errors.New("AIHOT source is stale pending retry")

type Refresher struct {
	client        *Client
	store         content.CatalogStore
	mu            sync.Mutex
	calendarMu    sync.Mutex
	leaderboardMu sync.Mutex
}

func NewRefresher(client *Client, store content.CatalogStore) *Refresher {
	return &Refresher{client: client, store: store}
}

func (r *Refresher) Refresh(ctx context.Context, now time.Time) error {
	if r == nil || r.client == nil || r.store == nil {
		return errors.New("AIHOT refresher unavailable")
	}
	var failures []error
	runSource := func(refresh func() error) bool {
		if err := ctx.Err(); err != nil {
			failures = append(failures, err)
			return false
		}
		if err := refresh(); err != nil {
			failures = append(failures, err)
		}
		if err := ctx.Err(); err != nil {
			failures = append(failures, err)
			return false
		}
		return true
	}
	for _, query := range []struct{ mode, window string }{
		{mode: "selected", window: "7d"},
		{mode: "selected", window: "24h"},
		{mode: "all", window: "7d"},
		{mode: "all", window: "24h"},
	} {
		if !runSource(func() error { return r.refreshItems(ctx, query.mode, query.window, now) }) {
			return errors.Join(failures...)
		}
	}
	if !runSource(func() error { return r.refreshTopics(ctx, now) }) {
		return errors.Join(failures...)
	}
	for _, archive := range []struct {
		kind, path string
		limit      int
	}{
		{kind: "daily", path: "/api/v1/dailies", limit: 180},
		{kind: "weekly", path: "/api/v1/weeklies", limit: 60},
		{kind: "monthly", path: "/api/v1/monthlies", limit: 60},
	} {
		if !runSource(func() error { return r.refreshArchive(ctx, archive.kind, archive.path, archive.limit, now) }) {
			return errors.Join(failures...)
		}
	}
	// The calendar has its own source checkpoint and stale state. A failure
	// here must not make otherwise-fresh news feeds appear unavailable.
	if err := r.refreshResetCalendar(ctx, now); err != nil && ctx.Err() != nil {
		failures = append(failures, err)
		return errors.Join(failures...)
	}
	if err := r.refreshModelLeaderboards(ctx, now); err != nil {
		failures = append(failures, err)
	}
	if err := errors.Join(failures...); err != nil {
		return err
	}
	if err := r.store.ExpireUnreferenced(ctx, now.UTC().Add(-unreferencedEntryAge)); err != nil {
		return fmt.Errorf("expire unreferenced AIHOT entries: %w", err)
	}
	return nil
}

func (r *Refresher) refreshModelLeaderboards(ctx context.Context, now time.Time) error {
	if _, ok := r.store.(content.ModelLeaderboardStore); !ok {
		return nil
	}
	r.leaderboardMu.Lock()
	defer r.leaderboardMu.Unlock()
	var failures []error
	for _, category := range []string{
		content.ModelLeaderboardOverall,
		content.ModelLeaderboardCoding,
		content.ModelLeaderboardReasoning,
		content.ModelLeaderboardProfessional,
		content.ModelLeaderboardKnowledge,
	} {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		if err := r.refreshModelLeaderboard(ctx, category, now); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (r *Refresher) refreshModelLeaderboard(ctx context.Context, category string, now time.Time) error {
	store, ok := r.store.(content.ModelLeaderboardStore)
	if !ok {
		return nil
	}
	category, err := content.NormalizeModelLeaderboardCategory(category)
	if err != nil {
		return err
	}
	name := modelLeaderboardSourceNamePrefix + category
	previous, exists, err := r.checkpoint(ctx, name)
	if err != nil {
		return fmt.Errorf("read AIHOT model leaderboard checkpoint %q: %w", name, err)
	}
	due, err := sourceDue(previous, exists, now)
	if errors.Is(err, errSourceRetryPending) || !due && err == nil {
		return nil
	}
	if err != nil {
		return fmt.Errorf("refresh AIHOT model leaderboard %q: %w", category, err)
	}
	snapshot, err := r.client.FetchModelLeaderboard(ctx, category, previous.ETag)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return r.recordModelLeaderboardFailure(ctx, name, previous, exists, now, err)
	}
	if snapshot.NotModified {
		if !exists || previous.Status.LastSuccessAt == "" {
			return r.recordModelLeaderboardFailure(ctx, name, previous, exists, now, errNoCachedResponse)
		}
		if _, err := store.LoadModelLeaderboard(ctx, category); err != nil {
			return r.recordModelLeaderboardFailure(ctx, name, previous, exists, now, err)
		}
		return r.successSource(ctx, name, previous, snapshot.ETag, snapshot.CacheControl, snapshot.LastModified, now, true)
	}
	snapshot.Snapshot.FetchedAt = content.ModelLeaderboardFetchedAt(now)
	checkpoint := successfulCheckpoint(name, previous, snapshot.ETag, snapshot.CacheControl, snapshot.LastModified, now, false)
	if err := store.SaveModelLeaderboard(ctx, snapshot.Snapshot, checkpoint); err != nil {
		return fmt.Errorf("save AIHOT model leaderboard %q: %w", category, err)
	}
	return nil
}

func (r *Refresher) recordModelLeaderboardFailure(ctx context.Context, name string, previous content.SourceCheckpoint, exists bool, now time.Time, cause error) error {
	if cause == nil {
		cause = errors.New("unknown upstream error")
	}
	previous.Name = name
	previous.Status.Name = name
	previous.Status.State = "stale"
	previous.FailureCount++
	retry := now.UTC().Add(sourceFreshness(name))
	var upstream *upstreamError
	if errors.As(cause, &upstream) {
		if upstream.Status == http.StatusTooManyRequests {
			previous.Status.State = "rate_limited"
			if parsed, ok := retryAfterAt(now, upstream.RetryAfter); ok {
				retry = parsed.UTC()
			} else {
				retry = now.UTC().Add(rateLimitBackoff(previous.FailureCount))
			}
		} else if parsed, ok := retryAfterAt(now, upstream.RetryAfter); ok && parsed.After(retry) {
			retry = parsed.UTC()
		}
	}
	previous.NextFetchAt = retry
	previous.Status.RetryAt = retry.Format(time.RFC3339Nano)
	if !exists && previous.LastModified == "" && previous.ETag == "" {
		previous.CacheControl = ""
	}
	if err := r.store.SaveCheckpoint(ctx, name, previous); err != nil {
		return fmt.Errorf("record AIHOT model leaderboard failure %q: %w", name, err)
	}
	return nil
}

func (r *Refresher) refreshResetCalendar(ctx context.Context, now time.Time) error {
	calendarStore, ok := r.store.(content.ResetCalendarStore)
	if !ok {
		return nil
	}
	r.calendarMu.Lock()
	defer r.calendarMu.Unlock()
	const name = "aihot:codex-resets"
	previous, exists, err := r.checkpoint(ctx, name)
	if err != nil {
		return fmt.Errorf("read AIHOT checkpoint %q: %w", name, err)
	}
	due, err := sourceDue(previous, exists, now)
	if err != nil || !due {
		return err
	}
	snapshot, err := r.client.FetchResetCalendar(ctx, previous.ETag)
	if err != nil {
		return r.failedSource(ctx, name, previous, exists, now, err)
	}
	if snapshot.NotModified {
		if !exists || previous.Status.LastSuccessAt == "" {
			return r.failedSource(ctx, name, previous, exists, now, errNoCachedResponse)
		}
		if _, err := calendarStore.LoadResetCalendar(ctx); err != nil {
			return r.failedSource(ctx, name, previous, exists, now, err)
		}
		return r.successSource(ctx, name, previous, snapshot.ETag, snapshot.CacheControl, snapshot.LastModified, now, true)
	}
	if err := calendarStore.SaveResetCalendar(ctx, snapshot.Snapshot); err != nil {
		return r.failedSource(ctx, name, previous, exists, now, err)
	}
	return r.successSource(ctx, name, previous, snapshot.ETag, snapshot.CacheControl, snapshot.LastModified, now, false)
}

func (r *Refresher) refreshItems(ctx context.Context, mode, window string, now time.Time) error {
	name := "aihot:news:" + mode + ":" + window
	previous, exists, err := r.checkpoint(ctx, name)
	if err != nil {
		return fmt.Errorf("read AIHOT checkpoint %q: %w", name, err)
	}
	due, err := sourceDue(previous, exists, now)
	if err != nil {
		return fmt.Errorf("refresh AIHOT source %q: %w", name, err)
	}
	if !due {
		return nil
	}
	snapshot, err := r.client.FetchItems(ctx, mode, window, previous.ETag)
	if err != nil {
		return r.failedSource(ctx, name, previous, exists, now, err)
	}
	if snapshot.NotModified {
		if !exists || previous.Status.LastSuccessAt == "" {
			return r.failedSource(ctx, name, previous, exists, now, errNoCachedResponse)
		}
		if _, err := r.store.Collection(ctx, name); err != nil {
			return r.failedSource(ctx, name, previous, exists, now, err)
		}
		if err := r.saveNotices(ctx, snapshot.Notices); err != nil {
			return fmt.Errorf("save AIHOT notices: %w", err)
		}
		return r.successSource(ctx, name, previous, snapshot.ETag, snapshot.CacheControl, snapshot.LastModified, now, true)
	}
	if err := r.store.ReplaceCollection(ctx, name, snapshot.Entries); err != nil {
		return r.failedSource(ctx, name, previous, exists, now, err)
	}
	if err := r.saveNotices(ctx, snapshot.Notices); err != nil {
		return fmt.Errorf("save AIHOT notices: %w", err)
	}
	return r.successSource(ctx, name, previous, snapshot.ETag, snapshot.CacheControl, snapshot.LastModified, now, false)
}

func (r *Refresher) refreshTopics(ctx context.Context, now time.Time) error {
	const name = "aihot:topics"
	previous, exists, err := r.checkpoint(ctx, name)
	if err != nil {
		return fmt.Errorf("read AIHOT checkpoint %q: %w", name, err)
	}
	due, err := sourceDue(previous, exists, now)
	if err != nil {
		return fmt.Errorf("refresh AIHOT source %q: %w", name, err)
	}
	if !due {
		return nil
	}
	status, headers, body, err := r.client.get(ctx, "/api/v1/hot-topics", nil, previous.ETag)
	if err != nil {
		return r.failedSource(ctx, name, previous, exists, now, err)
	}
	if status == http.StatusNotModified {
		if !exists || previous.Status.LastSuccessAt == "" {
			return r.failedSource(ctx, name, previous, exists, now, errNoCachedResponse)
		}
		if _, err := r.store.Collection(ctx, name); err != nil {
			return r.failedSource(ctx, name, previous, exists, now, err)
		}
		if err := r.saveNotices(ctx, nil); err != nil {
			return fmt.Errorf("save AIHOT notices: %w", err)
		}
		return r.successSource(ctx, name, previous, headers.Get("ETag"), headers.Get("Cache-Control"), headers.Get("Last-Modified"), now, true)
	}
	if status != http.StatusOK {
		return r.failedSource(ctx, name, previous, exists, now, &upstreamError{Status: status, RetryAfter: headers.Get("Retry-After")})
	}
	var response hotTopicsResponseWire
	if err := json.Unmarshal(body, &response); err != nil || response.SchemaVersion != 1 || response.Items == nil || response.Count != len(response.Items) || response.Count > 10 {
		return r.failedSource(ctx, name, previous, exists, now, errIncompletePage)
	}
	entries := make([]content.Entry, 0, len(response.Items))
	seen := make(map[string]bool, len(response.Items))
	notices, err := appendNotice(nil, response.Notice)
	if err != nil {
		return r.failedSource(ctx, name, previous, exists, now, err)
	}
	for index, topic := range response.Items {
		if topic.Rank != index+1 || seen[topic.ID] {
			return r.failedSource(ctx, name, previous, exists, now, errIncompletePage)
		}
		seen[topic.ID] = true
		baseEntry, err := normalizeTopic(topic, nil)
		if err != nil {
			return r.failedSource(ctx, name, previous, exists, now, err)
		}
		var story *storyWire
		if topic.Links.Story != nil {
			publicID, ok := storyPublicID(*topic.Links.Story)
			if !ok {
				return r.failedSource(ctx, name, previous, exists, now, errInvalidTopic)
			}
			loaded, storyNotices, gone, loadErr := r.fetchStory(ctx, publicID)
			if loadErr != nil {
				return r.failedSource(ctx, name, previous, exists, now, loadErr)
			}
			notices = append(notices, storyNotices...)
			if gone {
				if err := r.store.Delete(ctx, baseEntry.ID); err != nil {
					return r.failedSource(ctx, name, previous, exists, now, err)
				}
				continue
			}
			story = loaded
		}
		if story == nil {
			entries = append(entries, baseEntry)
			continue
		}
		entry, err := normalizeTopic(topic, story)
		if err != nil {
			return r.failedSource(ctx, name, previous, exists, now, err)
		}
		entries = append(entries, entry)
	}
	if err := r.store.ReplaceCollection(ctx, name, entries); err != nil {
		return r.failedSource(ctx, name, previous, exists, now, err)
	}
	if err := r.saveNotices(ctx, notices); err != nil {
		return fmt.Errorf("save AIHOT notices: %w", err)
	}
	return r.successSource(ctx, name, previous, headers.Get("ETag"), headers.Get("Cache-Control"), headers.Get("Last-Modified"), now, false)
}

func (r *Refresher) fetchStory(ctx context.Context, publicID string) (*storyWire, []map[string]any, bool, error) {
	if publicID == "" || strings.ContainsAny(publicID, "/\\") || len(publicID) > 128 {
		return nil, nil, false, errInvalidTopic
	}
	status, headers, body, err := r.client.get(ctx, "/api/v1/stories/"+publicID, nil, "")
	if err != nil {
		return nil, nil, false, err
	}
	if status == http.StatusNotFound || status == http.StatusGone {
		return nil, nil, true, nil
	}
	if status != http.StatusOK {
		return nil, nil, false, &upstreamError{Status: status, RetryAfter: headers.Get("Retry-After")}
	}
	var response storyResponseWire
	if err := json.Unmarshal(body, &response); err != nil || response.SchemaVersion != 1 || response.Story.PublicID != publicID {
		return nil, nil, false, errInvalidTopic
	}
	notices, err := appendNotice(nil, response.Notice)
	if err != nil {
		return nil, nil, false, err
	}
	return &response.Story, notices, false, nil
}

func (r *Refresher) refreshArchive(ctx context.Context, kind, endpoint string, limit int, now time.Time) error {
	name := "aihot:" + kind
	previous, exists, err := r.checkpoint(ctx, name)
	if err != nil {
		return fmt.Errorf("read AIHOT checkpoint %q: %w", name, err)
	}
	due, err := sourceDue(previous, exists, now)
	if err != nil {
		return fmt.Errorf("refresh AIHOT source %q: %w", name, err)
	}
	if !due {
		return nil
	}
	status, headers, body, err := r.client.get(ctx, endpoint, url.Values{"limit": {strconv.Itoa(limit)}}, previous.ETag)
	if err != nil {
		return r.failedSource(ctx, name, previous, exists, now, err)
	}
	if status == http.StatusNotModified {
		if !exists || previous.Status.LastSuccessAt == "" {
			return r.failedSource(ctx, name, previous, exists, now, errNoCachedResponse)
		}
		if _, err := r.store.Collection(ctx, name); err != nil {
			return r.failedSource(ctx, name, previous, exists, now, err)
		}
		if err := r.saveNotices(ctx, nil); err != nil {
			return fmt.Errorf("save AIHOT notices: %w", err)
		}
		return r.successSource(ctx, name, previous, headers.Get("ETag"), headers.Get("Cache-Control"), headers.Get("Last-Modified"), now, true)
	}
	if status != http.StatusOK {
		return r.failedSource(ctx, name, previous, exists, now, &upstreamError{Status: status, RetryAfter: headers.Get("Retry-After")})
	}
	entries, notices, err := decodeArchive(kind, body)
	if err != nil {
		return r.failedSource(ctx, name, previous, exists, now, err)
	}
	entries, err = r.replaceArchiveCollection(ctx, name, entries, now)
	if err != nil {
		return r.failedSource(ctx, name, previous, exists, now, err)
	}
	if err := r.saveNotices(ctx, notices); err != nil {
		return fmt.Errorf("save AIHOT notices: %w", err)
	}
	if err := r.successSource(ctx, name, previous, headers.Get("ETag"), headers.Get("Cache-Control"), headers.Get("Last-Modified"), now, false); err != nil {
		return err
	}
	if latest, ok := latestPeriodEntry(entries); ok {
		if _, err := r.resolveDetailAt(ctx, latest.ID, now); err != nil && !errors.Is(err, content.ErrWithdrawn) {
			return fmt.Errorf("warm AIHOT report detail %q: %w", latest.ID, err)
		}
	}
	return nil
}

func (r *Refresher) replaceArchiveCollection(ctx context.Context, name string, entries []content.Entry, now time.Time) ([]content.Entry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	merged, checkpointUpdates, err := r.mergeCachedDetails(ctx, entries, now)
	if err != nil {
		return nil, err
	}
	if err := r.store.ReplaceCollection(ctx, name, merged); err != nil {
		return nil, err
	}
	for detailName, checkpoint := range checkpointUpdates {
		if err := r.store.SaveCheckpoint(ctx, detailName, checkpoint); err != nil {
			return nil, fmt.Errorf("save AIHOT report checkpoint %q: %w", detailName, err)
		}
	}
	return merged, nil
}

func decodeArchive(kind string, body []byte) ([]content.Entry, []map[string]any, error) {
	var entries []content.Entry
	var rawNotice json.RawMessage
	switch kind {
	case "daily":
		var response dailyArchiveResponseWire
		if err := json.Unmarshal(body, &response); err != nil || response.SchemaVersion != 1 || response.Items == nil || response.Count != len(response.Items) {
			return nil, nil, errIncompletePage
		}
		entries = make([]content.Entry, 0, len(response.Items))
		seen := map[string]bool{}
		for _, wire := range response.Items {
			entry, err := normalizeDailyArchiveItem(wire)
			if err != nil || seen[entry.ID] {
				return nil, nil, errIncompletePage
			}
			seen[entry.ID] = true
			entries = append(entries, entry)
		}
		rawNotice = response.Notice
	case "weekly":
		var response weeklyArchiveResponseWire
		if err := json.Unmarshal(body, &response); err != nil || response.SchemaVersion != 1 || response.Items == nil || response.Count != len(response.Items) {
			return nil, nil, errIncompletePage
		}
		entries = make([]content.Entry, 0, len(response.Items))
		seen := map[string]bool{}
		for _, wire := range response.Items {
			entry, err := normalizePeriodArchiveItem("weekly", wire.Week, wire.GeneratedAt, wire.Headline, wire.Links, wire.Attribution)
			if err != nil || seen[entry.ID] {
				return nil, nil, errIncompletePage
			}
			seen[entry.ID] = true
			entries = append(entries, entry)
		}
		rawNotice = response.Notice
	case "monthly":
		var response monthlyArchiveResponseWire
		if err := json.Unmarshal(body, &response); err != nil || response.SchemaVersion != 1 || response.Items == nil || response.Count != len(response.Items) {
			return nil, nil, errIncompletePage
		}
		entries = make([]content.Entry, 0, len(response.Items))
		seen := map[string]bool{}
		for _, wire := range response.Items {
			entry, err := normalizePeriodArchiveItem("monthly", wire.Month, wire.GeneratedAt, wire.Headline, wire.Links, wire.Attribution)
			if err != nil || seen[entry.ID] {
				return nil, nil, errIncompletePage
			}
			seen[entry.ID] = true
			entries = append(entries, entry)
		}
		rawNotice = response.Notice
	default:
		return nil, nil, errInvalidReport
	}
	notices, err := appendNotice(nil, rawNotice)
	if err != nil {
		return nil, nil, err
	}
	return entries, notices, nil
}

func (r *Refresher) mergeCachedDetails(ctx context.Context, entries []content.Entry, now time.Time) ([]content.Entry, map[string]content.SourceCheckpoint, error) {
	checkpoints, err := r.store.Checkpoints(ctx)
	if err != nil {
		return nil, nil, err
	}
	byName := make(map[string]content.SourceCheckpoint, len(checkpoints))
	for _, checkpoint := range checkpoints {
		byName[checkpoint.Name] = checkpoint
	}
	updates := make(map[string]content.SourceCheckpoint)
	for i := range entries {
		entry := &entries[i]
		name := "aihot:detail:" + entry.ID
		checkpoint, hasCheckpoint := byName[name]
		if !hasCheckpoint {
			continue
		}
		cached, getErr := r.store.Get(ctx, entry.ID)
		if getErr == nil && cached.Kind == entry.Kind && cached.Period == entry.Period && checkpoint.Status.LastSuccessAt != "" {
			changed := indexFieldsChanged(*entry, cached)
			entry.Summary = cached.Summary
			entry.Paragraphs = append([]content.Paragraph{}, cached.Paragraphs...)
			if changed {
				checkpoint.NextFetchAt = now.UTC()
				checkpoint.Status.State = "stale"
				checkpoint.Status.RetryAt = ""
				updates[name] = checkpoint
			}
			continue
		}
		if !errors.Is(getErr, content.ErrNotFound) && !errors.Is(getErr, content.ErrWithdrawn) && getErr != nil {
			return nil, nil, getErr
		}
		checkpoint.ETag = ""
		checkpoint.NextFetchAt = now.UTC()
		checkpoint.Status.State = "stale"
		checkpoint.Status.LastSuccessAt = ""
		checkpoint.Status.RetryAt = ""
		updates[name] = checkpoint
	}
	return entries, updates, nil
}

func indexFieldsChanged(index, cached content.Entry) bool {
	if index.Title != cached.Title || index.Period != cached.Period || index.PublishedAt != cached.PublishedAt ||
		index.CanonicalURL != cached.CanonicalURL || index.Attribution != cached.Attribution {
		return true
	}
	if index.Kind == "daily" && index.Summary != cached.Summary {
		return true
	}
	return false
}

func latestPeriodEntry(entries []content.Entry) (content.Entry, bool) {
	if len(entries) == 0 {
		return content.Entry{}, false
	}
	latest := entries[0]
	for _, entry := range entries[1:] {
		if entry.Period > latest.Period {
			latest = entry
		}
	}
	return latest, true
}

func (r *Refresher) checkpoint(ctx context.Context, name string) (content.SourceCheckpoint, bool, error) {
	checkpoints, err := r.store.Checkpoints(ctx)
	if err != nil {
		return content.SourceCheckpoint{}, false, err
	}
	for _, checkpoint := range checkpoints {
		if checkpoint.Name == name || (checkpoint.Name == "" && checkpoint.Status.Name == name) {
			checkpoint.Name = name
			return checkpoint, true, nil
		}
	}
	return content.SourceCheckpoint{Name: name, Status: content.SourceStatus{Name: name}}, false, nil
}

func sourceDue(previous content.SourceCheckpoint, exists bool, now time.Time) (bool, error) {
	if !exists || previous.NextFetchAt.IsZero() || !previous.NextFetchAt.After(now) {
		return true, nil
	}
	if previous.Status.State == "fresh" && previous.Status.LastSuccessAt != "" {
		return false, nil
	}
	return false, errSourceRetryPending
}

func (r *Refresher) successSource(ctx context.Context, name string, previous content.SourceCheckpoint, etag, cacheControl, lastModified string, now time.Time, preserveMissing bool) error {
	checkpoint := successfulCheckpoint(name, previous, etag, cacheControl, lastModified, now, preserveMissing)
	return r.store.SaveCheckpoint(ctx, name, checkpoint)
}

func successfulCheckpoint(name string, previous content.SourceCheckpoint, etag, cacheControl, lastModified string, now time.Time, preserveMissing bool) content.SourceCheckpoint {
	if preserveMissing {
		if etag == "" {
			etag = previous.ETag
		}
		if cacheControl == "" {
			cacheControl = previous.CacheControl
		}
		if lastModified == "" {
			lastModified = previous.LastModified
		}
	}
	ttl := cacheTTL(cacheControl)
	if minimum := sourceFreshness(name); ttl < minimum {
		ttl = minimum
	}
	return content.SourceCheckpoint{
		Name: name, ETag: etag, CacheControl: cacheControl, LastModified: lastModified,
		NextFetchAt: now.UTC().Add(ttl), Status: content.SourceStatus{
			Name: name, State: "fresh", LastSuccessAt: now.UTC().Format(time.RFC3339Nano),
		},
	}
}

func (r *Refresher) failedSource(ctx context.Context, name string, previous content.SourceCheckpoint, exists bool, now time.Time, cause error) error {
	if cause == nil {
		cause = errors.New("unknown upstream error")
	}
	previous.Name = name
	previous.Status.Name = name
	previous.Status.State = "stale"
	previous.FailureCount++
	retry := now.UTC().Add(sourceFreshness(name))
	var upstream *upstreamError
	if errors.As(cause, &upstream) {
		if upstream.Status == http.StatusTooManyRequests {
			previous.Status.State = "rate_limited"
			if parsed, ok := retryAfterAt(now, upstream.RetryAfter); ok {
				retry = parsed.UTC()
			} else {
				retry = now.UTC().Add(rateLimitBackoff(previous.FailureCount))
			}
		} else if parsed, ok := retryAfterAt(now, upstream.RetryAfter); ok && parsed.After(retry) {
			retry = parsed.UTC()
		}
	}
	previous.NextFetchAt = retry
	previous.Status.RetryAt = retry.Format(time.RFC3339Nano)
	if !exists && previous.LastModified == "" && previous.ETag == "" {
		previous.CacheControl = ""
	}
	if err := r.store.SaveCheckpoint(ctx, name, previous); err != nil {
		return fmt.Errorf("refresh AIHOT source %q: %v (record failure: %w)", name, cause, err)
	}
	return fmt.Errorf("refresh AIHOT source %q: %w", name, cause)
}

func retryAfterAt(now time.Time, value string) (time.Time, bool) {
	if seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil && seconds > 0 {
		maxDurationSeconds := int64((1<<63 - 1) / int64(time.Second))
		if seconds > maxDurationSeconds {
			return time.Date(9999, time.December, 31, 23, 59, 59, 0, time.UTC), true
		}
		return now.Add(time.Duration(seconds) * time.Second), true
	}
	parsed, err := http.ParseTime(value)
	return parsed, err == nil && parsed.After(now)
}

func rateLimitBackoff(failureCount int) time.Duration {
	if failureCount < 1 {
		failureCount = 1
	}
	if failureCount >= int(maxRateLimitBackoff/rateLimitBackoffStep) {
		return maxRateLimitBackoff
	}
	return time.Duration(failureCount) * rateLimitBackoffStep
}

func sourceFreshness(name string) time.Duration {
	switch {
	case strings.HasPrefix(name, "aihot:model-leaderboard:"):
		return leaderboardFreshness
	case strings.HasPrefix(name, "aihot:news:"), name == "aihot:topics", name == "aihot:codex-resets":
		return defaultFreshness
	case strings.HasPrefix(name, "aihot:detail:"), name == "aihot:daily", name == "aihot:weekly", name == "aihot:monthly":
		return reportFreshness
	default:
		return defaultFreshness
	}
}

func cacheTTL(cacheControl string) time.Duration {
	ttl := defaultFreshness
	for _, directive := range strings.Split(cacheControl, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(directive), "=")
		if !ok || (strings.ToLower(key) != "max-age" && strings.ToLower(key) != "s-maxage") {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"`)
		seconds, err := strconv.Atoi(value)
		if err == nil && seconds >= 0 {
			candidate := time.Duration(seconds) * time.Second
			if candidate > ttl {
				ttl = candidate
			}
		}
	}
	return ttl
}

func (r *Refresher) saveNotices(ctx context.Context, notices []map[string]any) error {
	if len(notices) == 0 {
		return nil
	}
	current, err := r.store.LoadNotices(ctx)
	if err != nil {
		return err
	}
	seen := make(map[string]bool, len(current)+len(notices))
	merged := make([]map[string]any, 0, len(current)+len(notices))
	for _, notice := range append(current, notices...) {
		encoded, err := json.Marshal(notice)
		if err != nil {
			return err
		}
		key := string(encoded)
		if seen[key] {
			continue
		}
		seen[key] = true
		merged = append(merged, notice)
	}
	return r.store.SaveNotices(ctx, merged)
}
