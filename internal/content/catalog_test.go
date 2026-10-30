package content

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"
)

type memoryCatalogStore struct {
	entries      map[string]Entry
	collections  map[string][]string
	withdrawn    map[string]bool
	checkpoints  map[string]SourceCheckpoint
	coverage     Coverage
	notices      []map[string]any
	version      int64
	onCollection func()
}

type memoryResetCalendarStore struct {
	*memoryCatalogStore
	snapshot ResetCalendarSnapshot
}

func (s *memoryResetCalendarStore) SaveResetCalendar(_ context.Context, snapshot ResetCalendarSnapshot) error {
	s.snapshot = snapshot
	return nil
}

func (s *memoryResetCalendarStore) LoadResetCalendar(context.Context) (ResetCalendarSnapshot, error) {
	return s.snapshot, nil
}

func newMemoryCatalogStore() *memoryCatalogStore {
	return &memoryCatalogStore{
		entries:     map[string]Entry{},
		collections: map[string][]string{},
		withdrawn:   map[string]bool{},
		checkpoints: map[string]SourceCheckpoint{},
		notices:     []map[string]any{},
	}
}

func (s *memoryCatalogStore) Upsert(_ context.Context, entry Entry) error {
	old, ok := s.entries[entry.ID]
	if !ok || !reflect.DeepEqual(old, entry) || s.withdrawn[entry.ID] {
		s.version++
	}
	s.entries[entry.ID] = entry
	delete(s.withdrawn, entry.ID)
	return nil
}

func (s *memoryCatalogStore) Delete(_ context.Context, id string) error {
	_, exists := s.entries[id]
	member := false
	for name, ids := range s.collections {
		filtered := ids[:0]
		for _, current := range ids {
			if current == id {
				member = true
				continue
			}
			filtered = append(filtered, current)
		}
		s.collections[name] = filtered
	}
	if exists || member || !s.withdrawn[id] {
		s.version++
	}
	delete(s.entries, id)
	s.withdrawn[id] = true
	return nil
}

func (s *memoryCatalogStore) Get(_ context.Context, id string) (Entry, error) {
	entry, ok := s.entries[id]
	if ok {
		return entry, nil
	}
	if s.withdrawn[id] {
		return Entry{}, fmt.Errorf("entry %q: %w", id, ErrWithdrawn)
	}
	return Entry{}, fmt.Errorf("entry %q: %w", id, ErrNotFound)
}

func (s *memoryCatalogStore) All(_ context.Context, kind string) ([]Entry, error) {
	ids := make([]string, 0, len(s.entries))
	for id, entry := range s.entries {
		if kind == "" || entry.Kind == kind {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	entries := make([]Entry, 0, len(ids))
	for _, id := range ids {
		entries = append(entries, s.entries[id])
	}
	return entries, nil
}

func (s *memoryCatalogStore) ReplaceCollection(_ context.Context, name string, entries []Entry) error {
	ids := make([]string, 0, len(entries))
	changed := false
	for _, entry := range entries {
		old, ok := s.entries[entry.ID]
		if !ok || !reflect.DeepEqual(old, entry) || s.withdrawn[entry.ID] {
			changed = true
		}
		s.entries[entry.ID] = entry
		delete(s.withdrawn, entry.ID)
		ids = append(ids, entry.ID)
	}
	if !reflect.DeepEqual(s.collections[name], ids) {
		changed = true
	}
	if changed {
		s.version++
	}
	s.collections[name] = ids
	return nil
}

func (s *memoryCatalogStore) Collection(_ context.Context, name string) ([]Entry, error) {
	if s.onCollection != nil {
		hook := s.onCollection
		s.onCollection = nil
		hook()
	}
	ids := s.collections[name]
	entries := make([]Entry, 0, len(ids))
	for _, id := range ids {
		if entry, ok := s.entries[id]; ok {
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

func (s *memoryCatalogStore) HasMembership(_ context.Context, id string) (bool, error) {
	for _, ids := range s.collections {
		for _, current := range ids {
			if current == id {
				return true, nil
			}
		}
	}
	return false, nil
}

func (s *memoryCatalogStore) ExpireUnreferenced(context.Context, time.Time) error { return nil }

func (s *memoryCatalogStore) IsWithdrawn(_ context.Context, id string) (bool, error) {
	return s.withdrawn[id], nil
}

func (s *memoryCatalogStore) Version(context.Context) (int64, error) { return s.version, nil }

func (s *memoryCatalogStore) SaveSnapshot(context.Context, int64, time.Time, int) error { return nil }

func (s *memoryCatalogStore) Snapshots(context.Context, int64) ([]Snapshot, error) {
	return []Snapshot{}, nil
}

func (s *memoryCatalogStore) SaveCoverage(_ context.Context, coverage Coverage) error {
	s.coverage = coverage
	return nil
}

func (s *memoryCatalogStore) LoadCoverage(context.Context) (Coverage, error) { return s.coverage, nil }

func (s *memoryCatalogStore) SaveCheckpoint(_ context.Context, name string, checkpoint SourceCheckpoint) error {
	checkpoint.Name = name
	s.checkpoints[name] = checkpoint
	return nil
}

func (s *memoryCatalogStore) Checkpoints(context.Context) ([]SourceCheckpoint, error) {
	names := make([]string, 0, len(s.checkpoints))
	for name := range s.checkpoints {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]SourceCheckpoint, 0, len(names))
	for _, name := range names {
		out = append(out, s.checkpoints[name])
	}
	return out, nil
}

func (s *memoryCatalogStore) SaveNotices(_ context.Context, notices []map[string]any) error {
	s.notices = notices
	return nil
}

func (s *memoryCatalogStore) LoadNotices(context.Context) ([]map[string]any, error) {
	if s.notices == nil {
		return []map[string]any{}, nil
	}
	return s.notices, nil
}

func testNow() time.Time { return time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC) }

func testCheckpoint(name, state string, fetchedAt, nextFetch time.Time) SourceCheckpoint {
	return SourceCheckpoint{
		Name:         name,
		LastModified: "Wed, 21 Oct 2015 07:28:00 GMT",
		NextFetchAt:  nextFetch,
		Status: SourceStatus{
			Name:          name,
			State:         state,
			LastSuccessAt: fetchedAt.UTC().Format(time.RFC3339),
		},
	}
}

func testNews(id, title, category, publishedAt string) Entry {
	return Entry{ID: id, Kind: "news", Title: title, Summary: title + " summary", Category: category,
		PublishedAt: publishedAt, Paragraphs: []Paragraph{}}
}

func testProject(id string, repositoryID int64, repo, createdAt string, growth *Growth, scenes ...string) Entry {
	return Entry{ID: id, Kind: "project", Title: repo, Paragraphs: []Paragraph{}, Project: &Project{
		RepositoryID: repositoryID, Repo: repo, CreatedAt: createdAt, Growth: growth, Scenes: append([]string{}, scenes...), Skills: []SkillRef{},
	}}
}

func TestCatalogFeedPagesCollectionOrderAndBuildsMeta(t *testing.T) {
	ctx := context.Background()
	store := newMemoryCatalogStore()
	collection := "aihot:news:all:7d"
	entries := []Entry{
		testNews("news_a", "first", "tools", "2020-01-01T00:00:00Z"),
		testNews("news_b", "second", "tools", "2035-01-01T00:00:00Z"),
		testNews("news_c", "third", "tools", "2020-01-01T00:00:00Z"),
	}
	if err := store.ReplaceCollection(ctx, collection, entries); err != nil {
		t.Fatal(err)
	}
	now := testNow()
	store.checkpoints[collection] = testCheckpoint(collection, "fresh", now.Add(-time.Minute), now.Add(time.Hour))
	store.coverage = Coverage{QueryVersion: "v2", Candidates: 18, Updated: 7, CompleteGrowth: 4}
	store.notices = []map[string]any{{"message": "upstream notice", "code": 7}}
	catalog := NewCatalog(store, func() time.Time { return now })
	query := Query{Kind: "news", View: "all", Window: "7d", Limit: 2}

	first, err := catalog.Feed(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{first.Items[0].ID, first.Items[1].ID}; !reflect.DeepEqual(got, []string{"news_a", "news_b"}) {
		t.Fatalf("first page = %v", got)
	}
	if first.NextCursor == "" {
		t.Fatal("first page has no next cursor")
	}
	if !reflect.DeepEqual(first.Meta.Coverage, store.coverage) || !reflect.DeepEqual(first.Meta.Notices, store.notices) {
		t.Fatalf("metadata lost source fields: %#v", first.Meta)
	}
	if len(first.Meta.Sources) != 1 || first.Meta.Sources[0].Name != collection {
		t.Fatalf("feed sources = %#v", first.Meta.Sources)
	}
	if first.Meta.SourceUpdatedAt != "2015-10-21T07:28:00Z" {
		t.Fatalf("Last-Modified = %q", first.Meta.SourceUpdatedAt)
	}
	if first.Meta.Stale || first.Meta.FetchedAt != now.Add(-time.Minute).Format(time.RFC3339) {
		t.Fatalf("freshness = %#v", first.Meta)
	}

	query.Cursor = first.NextCursor
	second, err := catalog.Feed(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Items[0].ID != "news_c" || second.NextCursor != "" {
		t.Fatalf("second page = %#v", second)
	}
}

func TestCatalogFeedRejectsQueryAndVersionChanges(t *testing.T) {
	ctx := context.Background()
	store := newMemoryCatalogStore()
	name := "aihot:news:selected:7d"
	entries := []Entry{
		testNews("news_1", "one", "tools", ""),
		testNews("news_2", "two", "tools", ""),
	}
	if err := store.ReplaceCollection(ctx, name, entries); err != nil {
		t.Fatal(err)
	}
	now := testNow()
	store.checkpoints[name] = testCheckpoint(name, "fresh", now, now.Add(time.Hour))
	catalog := NewCatalog(store, func() time.Time { return now })
	query := Query{Kind: "news", Limit: 1}
	first, err := catalog.Feed(ctx, query)
	if err != nil {
		t.Fatal(err)
	}

	changedQuery := query
	changedQuery.Category = "tools"
	changedQuery.Cursor = first.NextCursor
	if _, err := catalog.Feed(ctx, changedQuery); !errors.Is(err, ErrCursorExpired) {
		t.Fatalf("query change error = %v", err)
	}
	if err := store.ReplaceCollection(ctx, name, append(entries, testNews("news_3", "three", "tools", ""))); err != nil {
		t.Fatal(err)
	}
	query.Cursor = first.NextCursor
	if _, err := catalog.Feed(ctx, query); !errors.Is(err, ErrCursorExpired) {
		t.Fatalf("version change error = %v", err)
	}
}

func TestCatalogFeedFiltersNewsWithoutPublishedAtWindowFiltering(t *testing.T) {
	ctx := context.Background()
	store := newMemoryCatalogStore()
	name := "aihot:news:selected:24h"
	entries := []Entry{
		testNews("news_1", "old matching story", "agents", "2019-01-01T00:00:00Z"),
		testNews("news_2", "other story", "tools", "2026-10-28T11:59:00Z"),
		testNews("news_3", "matching tools story", "tools", "2026-10-28T11:58:00Z"),
	}
	if err := store.ReplaceCollection(ctx, name, entries); err != nil {
		t.Fatal(err)
	}
	now := testNow()
	store.checkpoints[name] = testCheckpoint(name, "fresh", now, now.Add(time.Hour))
	catalog := NewCatalog(store, func() time.Time { return now })
	page, err := catalog.Feed(ctx, Query{Kind: "news", Window: "24h", Q: " MATCHING ", Category: "tools", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "news_3" {
		t.Fatalf("filtered items = %#v", page.Items)
	}
	page, err = catalog.Feed(ctx, Query{Kind: "news", Window: "24h", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != len(entries) || page.Items[0].ID != "news_1" {
		t.Fatalf("news collection order/window membership changed: %#v", page.Items)
	}
}

func TestCatalogProjectWeeklyUsesLatestCompleteStarsAddedWindow(t *testing.T) {
	ctx := context.Background()
	store := newMemoryCatalogStore()
	now := testNow()
	start := "2026-10-20T00:00:00Z"
	end := "2026-10-27T00:00:00Z"
	olderStart, olderEnd := "2026-10-13T00:00:00Z", "2026-10-20T00:00:00Z"
	entries := []Entry{
		testProject("project_22", 22, "org/zeta", "2026-01-01T00:00:00Z", &Growth{Metric: "stars_added", Value: 9, WindowStart: start, WindowEnd: end, ObservedDays: 7, Complete: true}),
		testProject("project_11", 11, "org/alpha", "2026-01-01T00:00:00Z", &Growth{Metric: "stars_added", Value: 9, WindowStart: start, WindowEnd: end, ObservedDays: 7, Complete: true}),
		testProject("project_1", 1, "org/old", "2026-01-01T00:00:00Z", &Growth{Metric: "stars_added", Value: 500, WindowStart: olderStart, WindowEnd: olderEnd, ObservedDays: 7, Complete: true}),
		testProject("project_30", 30, "org/net", "2026-01-01T00:00:00Z", &Growth{Metric: "stars_net", Value: 1000, WindowStart: start, WindowEnd: end, ObservedDays: 7, Complete: true}),
		testProject("project_40", 40, "org/incomplete", "2026-01-01T00:00:00Z", &Growth{Metric: "stars_added", Value: 700, WindowStart: "2026-10-21T00:00:00Z", WindowEnd: "2026-10-28T00:00:00Z", ObservedDays: 7, Complete: false}),
	}
	for _, entry := range entries {
		if err := store.Upsert(ctx, entry); err != nil {
			t.Fatal(err)
		}
	}
	github := testCheckpoint("github", "fresh", now.Add(-24*time.Hour), now.Add(-time.Minute))
	store.checkpoints["github"] = github
	store.checkpoints["aihot:news:selected:7d"] = testCheckpoint("aihot:news:selected:7d", "fresh", now, now.Add(time.Hour))
	catalog := NewCatalog(store, func() time.Time { return now })
	page, err := catalog.Feed(ctx, Query{Kind: "project", View: "weekly", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{page.Items[0].ID, page.Items[1].ID}; !reflect.DeepEqual(got, []string{"project_11", "project_22"}) {
		t.Fatalf("weekly order = %v", got)
	}
	if len(page.Items) != 2 {
		t.Fatalf("ineligible growth rows leaked into weekly list: %#v", page.Items)
	}
	if len(page.Meta.Sources) != 1 || page.Meta.Sources[0].Name != "github" || page.Meta.Stale != true {
		t.Fatalf("project freshness borrowed another source: %#v", page.Meta)
	}
}

func TestCatalogProjectNewAndScenarioOrderingAndFilters(t *testing.T) {
	ctx := context.Background()
	store := newMemoryCatalogStore()
	now := testNow()
	createdRecent := now.Add(-2 * 24 * time.Hour).Format(time.RFC3339)
	createdYesterday := now.Add(-24 * time.Hour).Format(time.RFC3339)
	createdOld := now.Add(-31 * 24 * time.Hour).Format(time.RFC3339)
	for _, entry := range []Entry{
		testProject("project_20", 20, "org/zeta", createdYesterday, &Growth{Metric: "stars_net", Value: 2, ObservedDays: 2}, "search"),
		testProject("project_10", 10, "org/alpha", createdYesterday, &Growth{Metric: "stars_added", Value: 5, ObservedDays: 7}, "search", "editor"),
		testProject("project_30", 30, "org/mid", createdRecent, &Growth{Metric: "stars_net", Value: 3, ObservedDays: 1}, "editor"),
		testProject("project_40", 40, "org/old", createdOld, &Growth{Metric: "stars_added", Value: 99, ObservedDays: 7}, "search"),
		testProject("project_50", 50, "org/no-growth", createdRecent, nil, "search"),
	} {
		if err := store.Upsert(ctx, entry); err != nil {
			t.Fatal(err)
		}
	}
	store.checkpoints["github"] = testCheckpoint("github", "fresh", now, now.Add(time.Hour))
	catalog := NewCatalog(store, func() time.Time { return now })

	page, err := catalog.Feed(ctx, Query{Kind: "project", View: "new", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{page.Items[0].ID, page.Items[1].ID, page.Items[2].ID}; !reflect.DeepEqual(got, []string{"project_10", "project_20", "project_30"}) {
		t.Fatalf("new ordering/filter = %v", got)
	}
	page, err = catalog.Feed(ctx, Query{Kind: "project", View: "scenarios", Scene: "editor", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{page.Items[0].ID, page.Items[1].ID}; !reflect.DeepEqual(got, []string{"project_10", "project_30"}) {
		t.Fatalf("scenario order/filter = %v", got)
	}
}

func TestCatalogEmptyUnavailableAndStaleCache(t *testing.T) {
	ctx := context.Background()
	now := testNow()
	store := newMemoryCatalogStore()
	catalog := NewCatalog(store, func() time.Time { return now })
	if _, err := catalog.Feed(ctx, Query{Kind: "topic"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("uncached empty source error = %v", err)
	}
	store.checkpoints["aihot:topics"] = testCheckpoint("aihot:topics", "fresh", now, now.Add(time.Hour))
	page, err := catalog.Feed(ctx, Query{Kind: "topic"})
	if err != nil || page.Items == nil || len(page.Items) != 0 {
		t.Fatalf("cached empty collection = %#v, %v", page, err)
	}
	if page.Meta.Sources == nil || page.Meta.Notices == nil {
		t.Fatalf("empty meta slices are nil: %#v", page.Meta)
	}

	staleStore := newMemoryCatalogStore()
	if err := staleStore.ReplaceCollection(ctx, "aihot:topics", []Entry{{ID: "topic_1", Kind: "topic", Title: "cached", Paragraphs: []Paragraph{}}}); err != nil {
		t.Fatal(err)
	}
	stale, err := NewCatalog(staleStore, func() time.Time { return now }).Feed(ctx, Query{Kind: "topic"})
	if err != nil || len(stale.Items) != 1 || !stale.Meta.Stale || len(stale.Meta.Sources) != 1 || stale.Meta.Sources[0].State != "unavailable" {
		t.Fatalf("cached rows without checkpoint = %#v, %v", stale, err)
	}
}

func TestCatalogFeedRetriesConcurrentVersionChange(t *testing.T) {
	ctx := context.Background()
	store := newMemoryCatalogStore()
	name := "aihot:news:selected:7d"
	first := testNews("news_old", "old", "tools", "")
	second := testNews("news_new", "new", "tools", "")
	if err := store.ReplaceCollection(ctx, name, []Entry{first}); err != nil {
		t.Fatal(err)
	}
	now := testNow()
	store.checkpoints[name] = testCheckpoint(name, "fresh", now, now.Add(time.Hour))
	store.onCollection = func() {
		if err := store.ReplaceCollection(ctx, name, []Entry{second}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := NewCatalog(store, func() time.Time { return now }).Feed(ctx, Query{Kind: "news"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "news_new" || page.Meta.Version != fmt.Sprint(store.version) {
		t.Fatalf("feed did not retry on version change: %#v, version %d", page, store.version)
	}
}

func TestCatalogEntryLoadsReportDetailsByMembershipAndCheckpoint(t *testing.T) {
	ctx := context.Background()
	store := newMemoryCatalogStore()
	now := testNow()
	base := Entry{ID: "daily_2026-10-28", Kind: "daily", Title: "index", Summary: "lead", Period: "2026-10-28", Paragraphs: []Paragraph{}}
	if err := store.ReplaceCollection(ctx, "aihot:daily", []Entry{base}); err != nil {
		t.Fatal(err)
	}
	store.checkpoints["aihot:daily"] = testCheckpoint("aihot:daily", "fresh", now, now.Add(time.Hour))
	calls := 0
	catalog := NewCatalog(store, func() time.Time { return now })
	catalog.SetDetailLoader(func(_ context.Context, id string) (Entry, error) {
		calls++
		if id != base.ID {
			return Entry{}, fmt.Errorf("unexpected ID %s", id)
		}
		return Entry{ID: base.ID, Kind: base.Kind, Title: "complete", Summary: "detail", Period: base.Period, Paragraphs: []Paragraph{}}, nil
	})
	result, err := catalog.Entry(ctx, base.ID)
	if err != nil || result.Entry.Summary != "detail" {
		t.Fatalf("loaded detail = %#v, %v", result, err)
	}
	if calls != 1 {
		t.Fatalf("loader calls = %d", calls)
	}
	checkpoint, ok := store.checkpoints["aihot:detail:"+base.ID]
	if !ok || checkpoint.Status.State != "fresh" || !checkpoint.NextFetchAt.After(now) {
		t.Fatalf("detail checkpoint not stored: %#v", checkpoint)
	}
	if _, err := catalog.Entry(ctx, base.ID); err != nil || calls != 1 {
		t.Fatalf("fresh empty-paragraph detail was reloaded: calls=%d err=%v", calls, err)
	}
}

func TestCatalogEntryTombstoneAndMembershipSemantics(t *testing.T) {
	ctx := context.Background()
	store := newMemoryCatalogStore()
	store.entries["daily_2026-10-28"] = Entry{ID: "daily_2026-10-28", Kind: "daily"}
	loaderCalls := 0
	catalog := NewCatalog(store, func() time.Time { return testNow() })
	catalog.SetDetailLoader(func(context.Context, string) (Entry, error) {
		loaderCalls++
		return Entry{}, nil
	})
	if _, err := catalog.Entry(ctx, "daily_2026-10-28"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cached nonmember entry = %v", err)
	}
	if loaderCalls != 0 {
		t.Fatalf("loader called before membership check: %d", loaderCalls)
	}
	if err := store.Delete(ctx, "daily_2026-10-28"); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Entry(ctx, "daily_2026-10-28"); !errors.Is(err, ErrWithdrawn) {
		t.Fatalf("tombstoned report = %v", err)
	}
	if _, err := catalog.Entry(ctx, "missing_1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown entry = %v", err)
	}
	if _, err := catalog.Entry(ctx, "../secret"); !errors.Is(err, ErrBadQuery) {
		t.Fatalf("invalid ID = %v", err)
	}
}

func TestCatalogEntryRetainsCachedReportOnLoaderFailure(t *testing.T) {
	ctx := context.Background()
	store := newMemoryCatalogStore()
	now := testNow()
	base := Entry{ID: "weekly_2026-W44", Kind: "weekly", Title: "cached index", Summary: "old detail", Period: "2026-W44", Paragraphs: []Paragraph{}}
	if err := store.ReplaceCollection(ctx, "aihot:weekly", []Entry{base}); err != nil {
		t.Fatal(err)
	}
	archive := testCheckpoint("aihot:weekly", "fresh", now.Add(-time.Minute), now.Add(time.Hour))
	store.checkpoints[archive.Name] = archive
	detail := testCheckpoint("aihot:detail:"+base.ID, "fresh", now.Add(-time.Hour), now.Add(-time.Minute))
	detail.ETag = "detail-etag"
	store.checkpoints[detail.Name] = detail
	catalog := NewCatalog(store, func() time.Time { return now })
	catalog.SetDetailLoader(func(context.Context, string) (Entry, error) { return Entry{}, errors.New("temporary failure") })
	result, err := catalog.Entry(ctx, base.ID)
	if err != nil || result.Entry.Summary != base.Summary {
		t.Fatalf("cached fallback = %#v, %v", result, err)
	}
	if !result.Meta.Stale || len(result.Meta.Sources) != 2 {
		t.Fatalf("fallback meta = %#v", result.Meta)
	}
	checkpoint := store.checkpoints[detail.Name]
	if checkpoint.Status.State != "stale" || checkpoint.Status.LastSuccessAt != detail.Status.LastSuccessAt || checkpoint.ETag != detail.ETag {
		t.Fatalf("stale detail checkpoint lost success metadata: %#v", checkpoint)
	}
}

func TestCatalogEntrySuccessfulDetailRetryClearsStaleCheckpoint(t *testing.T) {
	ctx := context.Background()
	store := newMemoryCatalogStore()
	startedAt := testNow()
	clock := startedAt
	base := Entry{ID: "daily_2026-10-28", Kind: "daily", Title: "index", Summary: "cached", Period: "2026-10-28", Paragraphs: []Paragraph{}}
	if err := store.ReplaceCollection(ctx, "aihot:daily", []Entry{base}); err != nil {
		t.Fatal(err)
	}
	store.checkpoints["aihot:daily"] = testCheckpoint("aihot:daily", "fresh", startedAt, startedAt.Add(time.Hour))
	detailName := "aihot:detail:" + base.ID
	checkpoint := testCheckpoint(detailName, "fresh", startedAt.Add(-time.Hour), startedAt.Add(-time.Minute))
	checkpoint.ETag = "etag-before"
	checkpoint.CacheControl = "max-age=60"
	checkpoint.FailureCount = 2
	checkpoint.Status.RetryAt = startedAt.Add(-time.Minute).Format(time.RFC3339)
	store.checkpoints[detailName] = checkpoint
	calls := 0
	catalog := NewCatalog(store, func() time.Time { return clock })
	catalog.SetDetailLoader(func(_ context.Context, id string) (Entry, error) {
		calls++
		if calls == 1 {
			return Entry{}, errors.New("temporary upstream failure")
		}
		if id != base.ID {
			return Entry{}, fmt.Errorf("unexpected ID %s", id)
		}
		// Model the adapter's successful response checkpoint, including its
		// upstream cache lifetime, before returning the normalized detail.
		adapterCheckpoint := store.checkpoints[detailName]
		adapterCheckpoint.ETag = "etag-after"
		adapterCheckpoint.CacheControl = "max-age=7200"
		adapterCheckpoint.NextFetchAt = clock.Add(2 * time.Hour)
		if err := store.SaveCheckpoint(ctx, detailName, adapterCheckpoint); err != nil {
			return Entry{}, err
		}
		return Entry{ID: base.ID, Kind: base.Kind, Title: "complete", Summary: "refreshed", Period: base.Period, Paragraphs: []Paragraph{}}, nil
	})

	first, err := catalog.Entry(ctx, base.ID)
	if err != nil || first.Entry.Summary != base.Summary || !first.Meta.Stale {
		t.Fatalf("first failed load should preserve cached entry: %#v, %v", first, err)
	}
	failedCheckpoint := store.checkpoints[detailName]
	if failedCheckpoint.Status.State != "stale" || failedCheckpoint.FailureCount != 3 || !failedCheckpoint.NextFetchAt.After(startedAt) {
		t.Fatalf("failed load checkpoint = %#v", failedCheckpoint)
	}

	clock = startedAt.Add(31 * time.Minute)
	result, err := catalog.Entry(ctx, base.ID)
	if err != nil || result.Entry.Summary != "refreshed" || result.Meta.Stale {
		t.Fatalf("successful retry = %#v, %v", result, err)
	}
	if calls != 2 {
		t.Fatalf("loader calls = %d, want 2", calls)
	}
	got := store.checkpoints[detailName]
	if got.Status.State != "fresh" || got.Status.LastSuccessAt != clock.Format(time.RFC3339) || got.Status.RetryAt != "" || got.FailureCount != 0 {
		t.Fatalf("successful retry did not reset checkpoint state: %#v", got)
	}
	if got.ETag != "etag-after" || got.CacheControl != "max-age=7200" || !got.NextFetchAt.Equal(clock.Add(2*time.Hour)) {
		t.Fatalf("successful retry lost adapter cache metadata: %#v", got)
	}
}

func TestCatalogEntrySuccessfulExpiredCheckpointRefreshesLastSuccess(t *testing.T) {
	ctx := context.Background()
	store := newMemoryCatalogStore()
	now := testNow()
	base := Entry{ID: "weekly_2026-W44", Kind: "weekly", Title: "index", Summary: "cached", Period: "2026-W44", Paragraphs: []Paragraph{}}
	if err := store.ReplaceCollection(ctx, "aihot:weekly", []Entry{base}); err != nil {
		t.Fatal(err)
	}
	store.checkpoints["aihot:weekly"] = testCheckpoint("aihot:weekly", "fresh", now, now.Add(time.Hour))
	detailName := "aihot:detail:" + base.ID
	checkpoint := testCheckpoint(detailName, "fresh", now.Add(-24*time.Hour), now.Add(-time.Minute))
	checkpoint.ETag = "existing-etag"
	checkpoint.CacheControl = "max-age=300"
	checkpoint.FailureCount = 4
	checkpoint.Status.RetryAt = now.Add(-time.Minute).Format(time.RFC3339)
	store.checkpoints[detailName] = checkpoint
	catalog := NewCatalog(store, func() time.Time { return now })
	catalog.SetDetailLoader(func(context.Context, string) (Entry, error) {
		return Entry{ID: base.ID, Kind: base.Kind, Title: "complete", Summary: "new success", Period: base.Period, Paragraphs: []Paragraph{}}, nil
	})
	result, err := catalog.Entry(ctx, base.ID)
	if err != nil || result.Entry.Summary != "new success" || result.Meta.Stale {
		t.Fatalf("expired detail refresh = %#v, %v", result, err)
	}
	got := store.checkpoints[detailName]
	if got.Status.State != "fresh" || got.Status.LastSuccessAt != now.Format(time.RFC3339) || got.Status.RetryAt != "" || got.FailureCount != 0 {
		t.Fatalf("expired success metadata was not refreshed: %#v", got)
	}
	if got.ETag != "existing-etag" || got.CacheControl != "max-age=300" || !got.NextFetchAt.After(now) {
		t.Fatalf("expired success lost adapter metadata or retry lifetime: %#v", got)
	}
}

func TestCatalogProjectEntryUsesOnlyGitHubFreshness(t *testing.T) {
	ctx := context.Background()
	store := newMemoryCatalogStore()
	now := testNow()
	entry := testProject("project_42", 42, "org/project", now.Add(-time.Hour).Format(time.RFC3339), nil)
	if err := store.Upsert(ctx, entry); err != nil {
		t.Fatal(err)
	}
	github := testCheckpoint("github", "fresh", now.Add(-24*time.Hour), now.Add(-time.Minute))
	store.checkpoints["github"] = github
	aihot := testCheckpoint("aihot:topics", "fresh", now, now.Add(time.Hour))
	store.checkpoints[aihot.Name] = aihot
	result, err := NewCatalog(store, func() time.Time { return now }).Entry(ctx, entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Meta.Sources) != 1 || result.Meta.Sources[0].Name != "github" || !result.Meta.Stale {
		t.Fatalf("project freshness = %#v", result.Meta)
	}
}

func TestCatalogStatusIncludesAllSourceStatesAndNonNilCollections(t *testing.T) {
	ctx := context.Background()
	store := newMemoryCatalogStore()
	now := testNow()
	store.checkpoints["github"] = testCheckpoint("github", "fresh", now, now.Add(time.Hour))
	store.checkpoints["aihot:topics"] = testCheckpoint("aihot:topics", "rate_limited", now.Add(-time.Hour), now.Add(-time.Minute))
	meta, err := NewCatalog(store, func() time.Time { return now }).Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.Sources) != 2 || meta.Sources[0].Name != "aihot:topics" || meta.Sources[1].Name != "github" {
		t.Fatalf("status sources = %#v", meta.Sources)
	}
	if !meta.Stale || meta.Notices == nil {
		t.Fatalf("status meta = %#v", meta)
	}
}

func TestCatalogWeeklyNetFallbackUsesOnlyCompleteSameWindowWhenNoAddedGroup(t *testing.T) {
	ctx := context.Background()
	store := newMemoryCatalogStore()
	now := testNow()
	makeGrowth := func(metric string, value int, start, end string, days int, complete bool) *Growth {
		return &Growth{Metric: metric, Value: value, WindowStart: start, WindowEnd: end, ObservedDays: days, Complete: complete}
	}
	entries := []Entry{
		testProject("project_1", 1, "org/net-high", "", makeGrowth("stars_net", 20, "2026-10-20T06:30:00Z", "2026-10-27T06:30:00Z", 7, true)),
		testProject("project_2", 2, "org/net-low", "", makeGrowth("stars_net", -2, "2026-10-20T06:30:00Z", "2026-10-27T06:30:00Z", 7, true)),
		testProject("project_3", 3, "org/older", "", makeGrowth("stars_net", 200, "2026-10-19T06:30:00Z", "2026-10-26T06:30:00Z", 7, true)),
		testProject("project_4", 4, "org/short", "", makeGrowth("stars_net", 999, "2026-10-26T06:30:00Z", "2026-10-27T06:30:00Z", 1, false)),
		testProject("project_5", 5, "org/added-short", "", makeGrowth("stars_added", 999, "2026-10-26T06:30:00Z", "2026-10-27T06:30:00Z", 1, false)),
	}
	for _, entry := range entries {
		if err := store.Upsert(ctx, entry); err != nil {
			t.Fatal(err)
		}
	}
	store.checkpoints["github"] = testCheckpoint("github", "fresh", now, now.Add(time.Hour))
	catalog := NewCatalog(store, func() time.Time { return now })
	page, err := catalog.Feed(ctx, Query{Kind: "project", View: "weekly"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Items[0].ID != "project_1" || page.Items[1].ID != "project_2" {
		t.Fatalf("net fallback = %#v", page.Items)
	}
	// A complete additions group takes priority even if its window is older than net samples.
	added := testProject("project_6", 6, "org/added", "", makeGrowth("stars_added", 1, "2026-10-18T06:30:00Z", "2026-10-25T06:30:00Z", 7, true))
	if err := store.Upsert(ctx, added); err != nil {
		t.Fatal(err)
	}
	page, err = catalog.Feed(ctx, Query{Kind: "project", View: "weekly"})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != "project_6" {
		t.Fatalf("added priority = %#v, %v", page.Items, err)
	}
}

func TestResetCalendarMarksExpiredFreshCheckpointStaleAndReturnsCachedSnapshot(t *testing.T) {
	now := time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)
	store := &memoryResetCalendarStore{memoryCatalogStore: newMemoryCatalogStore()}
	store.snapshot = ResetCalendarSnapshot{
		Timezone: "Asia/Shanghai",
		Events:   []ResetCalendarEvent{{ID: "reset-1", Type: "direct_reset", DisplayLabel: "额度重置", Status: "confirmed"}},
	}
	store.checkpoints["aihot:codex-resets"] = SourceCheckpoint{
		Name:        "aihot:codex-resets",
		NextFetchAt: now,
		Status:      SourceStatus{Name: "aihot:codex-resets", State: "fresh", LastSuccessAt: now.Add(-10 * time.Minute).Format(time.RFC3339)},
	}

	got, err := NewCatalog(store, func() time.Time { return now }).ResetCalendar(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !got.Meta.Stale || got.Meta.LastError == "" || len(got.Events) != 1 || got.Events[0].ID != "reset-1" {
		t.Fatalf("expired checkpoint should mark cached calendar stale and preserve events: %#v", got)
	}
}
