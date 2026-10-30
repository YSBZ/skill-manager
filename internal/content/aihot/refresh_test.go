package aihot

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"skillmanage/internal/content"
	"skillmanage/internal/content/sqlite"
)

func openTestStore(t *testing.T) *sqlite.Store {
	t.Helper()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "content.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

type replaceBarrierStore struct {
	content.CatalogStore
	reached       chan struct{}
	resume        chan struct{}
	detailWritten chan struct{}
}

func (s *replaceBarrierStore) ReplaceCollection(ctx context.Context, name string, entries []content.Entry) error {
	if name == "aihot:daily" {
		close(s.reached)
		select {
		case <-s.resume:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.CatalogStore.ReplaceCollection(ctx, name, entries)
}

func (s *replaceBarrierStore) Upsert(ctx context.Context, entry content.Entry) error {
	if err := s.CatalogStore.Upsert(ctx, entry); err != nil {
		return err
	}
	if entry.Kind == "daily" {
		select {
		case s.detailWritten <- struct{}{}:
		default:
		}
	}
	return nil
}

func TestA04ReviewArchiveRefreshCannotEraseConcurrentDetail(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	baseStore := openTestStore(t)
	old := dailyIndexEntry("2026-10-27")
	if err := baseStore.ReplaceCollection(ctx, "aihot:daily", []content.Entry{old}); err != nil {
		t.Fatal(err)
	}
	barrier := &replaceBarrierStore{CatalogStore: baseStore, reached: make(chan struct{}), resume: make(chan struct{}), detailWritten: make(chan struct{}, 1)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/api/v1/dailies":
			_, _ = io.WriteString(w, dailyIndexJSON([]string{"2026-10-28", "2026-10-27"}, "Daily"))
		case "/api/v1/dailies/2026-10-27":
			_, _ = io.WriteString(w, dailyDetailJSON("2026-10-27"))
		case "/api/v1/dailies/2026-10-28":
			_, _ = io.WriteString(w, dailyDetailJSON("2026-10-28"))
		default:
			http.NotFound(w, req)
		}
	}))
	defer srv.Close()
	refresher := NewRefresher(NewClient(srv.URL, srv.Client()), barrier)
	now := time.Now()
	refreshDone := make(chan error, 1)
	go func() { refreshDone <- refresher.refreshArchive(ctx, "daily", "/api/v1/dailies", 180, now) }()
	select {
	case <-barrier.reached:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	type detailResult struct {
		entry content.Entry
		err   error
	}
	detailDone := make(chan detailResult, 1)
	var loadedDetail *detailResult
	detailStarted := make(chan struct{})
	go func() {
		close(detailStarted)
		entry, err := refresher.resolveDetailAt(ctx, old.ID, now)
		detailDone <- detailResult{entry: entry, err: err}
	}()
	<-detailStarted
	// If an unprotected resolver persists before replacement, give it a bounded
	// chance to finish. A resolver serialized behind the archive writer produces
	// no write during this probe, so the writer is released without awaiting it.
	select {
	case <-barrier.detailWritten:
		select {
		case result := <-detailDone:
			loadedDetail = &result
		case <-time.After(250 * time.Millisecond):
		}
	case <-time.After(250 * time.Millisecond):
	}
	close(barrier.resume)
	select {
	case err := <-refreshDone:
		if err != nil {
			t.Fatalf("refresh failed: %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if loadedDetail == nil {
		select {
		case result := <-detailDone:
			loadedDetail = &result
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if loadedDetail.err != nil || len(loadedDetail.entry.Paragraphs) != 1 {
		t.Fatalf("concurrent detail load = %#v, %v", loadedDetail.entry, loadedDetail.err)
	}
	cached, err := baseStore.Get(ctx, old.ID)
	if err != nil || len(cached.Paragraphs) != 1 || cached.Paragraphs[0].Text != "body 2026-10-27" {
		t.Fatalf("concurrent archive refresh erased persisted detail: entry=%#v err=%v", cached, err)
	}
}

func TestRefreshPartialCursorFailurePreservesCollectionWithoutTombstone(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	old := content.Entry{ID: "news_old", Kind: "news", Title: "old cached", Paragraphs: []content.Paragraph{}}
	if err := store.ReplaceCollection(ctx, "aihot:news:selected:7d", []content.Entry{old}); err != nil {
		t.Fatal(err)
	}
	calls := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls[r.URL.Path+"?"+r.URL.RawQuery]++
		switch r.URL.Path {
		case "/api/v1/items":
			mode, window := r.URL.Query().Get("mode"), r.URL.Query().Get("window")
			if mode == "selected" && window == "7d" {
				if r.URL.Query().Get("cursor") == "" {
					_, _ = io.WriteString(w, itemsPageJSON("selected", "7d", `[{"id":"new","title":"new","originalTitle":null,"summary":"summary","source":{"name":"Publisher"},"links":{"aihot":"https://aihot.news/read/new","original":"https://publisher.example/new"},"publishedAt":null,"discoveredAt":"2026-10-28T10:00:00Z","category":null,"score":null,"selected":true,"reason":null}]`, 1, true, "next"))
					return
				}
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, `{"type":"service_unavailable","detail":"body must not leak"}`)
				return
			}
			_, _ = io.WriteString(w, itemsPageJSON(mode, window, `[]`, 0, false, ""))
		case "/api/v1/hot-topics":
			_, _ = io.WriteString(w, `{"schemaVersion":1,"count":0,"items":[],"notice":null}`)
		case "/api/v1/dailies":
			_, _ = io.WriteString(w, `{"schemaVersion":1,"count":0,"items":[],"notice":null}`)
		case "/api/v1/weeklies", "/api/v1/monthlies":
			_, _ = io.WriteString(w, `{"schemaVersion":1,"count":0,"items":[],"notice":null}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	refresher := NewRefresher(NewClient(srv.URL, srv.Client()), store)
	now := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	if err := refresher.Refresh(ctx, now); err == nil {
		t.Fatal("partial second page accepted")
	}
	entries, err := store.Collection(ctx, "aihot:news:selected:7d")
	if err != nil || len(entries) != 1 || entries[0].ID != old.ID {
		t.Fatalf("collection after partial response = %#v, %v", entries, err)
	}
	if withdrawn, err := store.IsWithdrawn(ctx, old.ID); err != nil || withdrawn {
		t.Fatalf("rolling window removal wrote tombstone = %v, %v", withdrawn, err)
	}
	checkpoint, ok := testCheckpointByName(t, store, "aihot:news:selected:7d")
	if !ok || checkpoint.Status.State != "stale" {
		t.Fatalf("failed collection checkpoint = %#v, exists=%v", checkpoint, ok)
	}
}

func TestRefreshCompleteWindowExitRemovesMembershipWithoutTombstone(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	old := content.Entry{ID: "news_old", Kind: "news", Title: "old cached", Paragraphs: []content.Paragraph{}}
	if err := store.ReplaceCollection(ctx, "aihot:news:selected:7d", []content.Entry{old}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/items":
			_, _ = io.WriteString(w, itemsPageJSON(r.URL.Query().Get("mode"), r.URL.Query().Get("window"), `[]`, 0, false, ""))
		case "/api/v1/hot-topics":
			_, _ = io.WriteString(w, `{"schemaVersion":1,"count":0,"items":[],"notice":null}`)
		case "/api/v1/dailies", "/api/v1/weeklies", "/api/v1/monthlies":
			_, _ = io.WriteString(w, `{"schemaVersion":1,"count":0,"items":[],"notice":null}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	refresher := NewRefresher(NewClient(srv.URL, srv.Client()), store)
	if err := refresher.Refresh(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	entries, err := store.Collection(ctx, "aihot:news:selected:7d")
	if err != nil || len(entries) != 0 {
		t.Fatalf("completed rolling collection = %#v, %v", entries, err)
	}
	if member, err := store.HasMembership(ctx, old.ID); err != nil || member {
		t.Fatalf("expired item membership = %v, %v", member, err)
	}
	if withdrawn, err := store.IsWithdrawn(ctx, old.ID); err != nil || withdrawn {
		t.Fatalf("expired item tombstone = %v, %v", withdrawn, err)
	}
}

func TestItems304RequiresAndRefreshesAValidCachedCollection(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	name := "aihot:news:selected:7d"
	entry := content.Entry{ID: "news_cached", Kind: "news", Title: "cached", Paragraphs: []content.Paragraph{}}
	if err := store.ReplaceCollection(ctx, name, []content.Entry{entry}); err != nil {
		t.Fatal(err)
	}
	oldSuccess := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	if err := store.SaveCheckpoint(ctx, name, content.SourceCheckpoint{Name: name, ETag: `"old"`, CacheControl: "max-age=60",
		Status: content.SourceStatus{Name: name, State: "stale", LastSuccessAt: oldSuccess}}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != `"old"` {
			t.Errorf("If-None-Match = %q", r.Header.Get("If-None-Match"))
		}
		w.Header().Set("ETag", `"new"`)
		w.Header().Set("Cache-Control", "max-age=600")
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()
	refresher := NewRefresher(NewClient(srv.URL, srv.Client()), store)
	now := time.Now().UTC()
	if err := refresher.refreshItems(ctx, "selected", "7d", now); err != nil {
		t.Fatal(err)
	}
	entries, err := store.Collection(ctx, name)
	if err != nil || len(entries) != 1 || entries[0].ID != entry.ID {
		t.Fatalf("cached collection after 304 = %#v, %v", entries, err)
	}
	checkpoint, _ := testCheckpointByName(t, store, name)
	if checkpoint.ETag != `"new"` || checkpoint.Status.State != "fresh" || checkpoint.Status.LastSuccessAt == oldSuccess || !checkpoint.NextFetchAt.After(now) {
		t.Fatalf("checkpoint after valid 304 = %#v", checkpoint)
	}
}

func TestItems304WithoutSuccessfulCacheIsStaleAndRejected(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()
	refresher := NewRefresher(NewClient(srv.URL, srv.Client()), store)
	if err := refresher.refreshItems(ctx, "selected", "7d", time.Now()); err == nil {
		t.Fatal("304 without cached success accepted")
	}
	checkpoint, ok := testCheckpointByName(t, store, "aihot:news:selected:7d")
	if !ok || checkpoint.Status.State != "stale" || checkpoint.Status.LastSuccessAt != "" {
		t.Fatalf("invalid-cache checkpoint = %#v, exists=%v", checkpoint, ok)
	}
}

func TestRefreshArchiveIndexesPrewarmLatestAndResolveOlderOnDemand(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	detailCalls := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/items":
			_, _ = io.WriteString(w, itemsPageJSON(r.URL.Query().Get("mode"), r.URL.Query().Get("window"), `[]`, 0, false, ""))
		case "/api/v1/hot-topics":
			_, _ = io.WriteString(w, `{"schemaVersion":1,"count":0,"items":[],"notice":null}`)
		case "/api/v1/dailies":
			_, _ = io.WriteString(w, dailyIndexJSON([]string{"2026-10-28", "2026-10-27"}, "daily"))
		case "/api/v1/weeklies":
			_, _ = io.WriteString(w, periodIndexJSON("weekly", []string{"2026-W44", "2026-W43"}))
		case "/api/v1/monthlies":
			_, _ = io.WriteString(w, periodIndexJSON("monthly", []string{"2026-10", "2026-09"}))
		case "/api/v1/dailies/2026-10-28", "/api/v1/dailies/2026-10-27":
			detailCalls[r.URL.Path]++
			date := strings.TrimPrefix(r.URL.Path, "/api/v1/dailies/")
			_, _ = io.WriteString(w, dailyDetailJSON(date))
		case "/api/v1/weeklies/2026-W44", "/api/v1/weeklies/2026-W43":
			detailCalls[r.URL.Path]++
			period := strings.TrimPrefix(r.URL.Path, "/api/v1/weeklies/")
			_, _ = io.WriteString(w, periodDetailJSON("weekly", period))
		case "/api/v1/monthlies/2026-10", "/api/v1/monthlies/2026-09":
			detailCalls[r.URL.Path]++
			period := strings.TrimPrefix(r.URL.Path, "/api/v1/monthlies/")
			_, _ = io.WriteString(w, periodDetailJSON("monthly", period))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	refresher := NewRefresher(NewClient(srv.URL, srv.Client()), store)
	if err := refresher.Refresh(ctx, time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if len(detailCalls) != 3 || detailCalls["/api/v1/dailies/2026-10-28"] != 1 || detailCalls["/api/v1/weeklies/2026-W44"] != 1 || detailCalls["/api/v1/monthlies/2026-10"] != 1 {
		t.Fatalf("prewarmed detail requests = %#v", detailCalls)
	}
	dailies, err := store.Collection(ctx, "aihot:daily")
	if err != nil || len(dailies) != 2 || dailies[0].Period != "2026-10-28" || dailies[1].Period != "2026-10-27" {
		t.Fatalf("daily archive = %#v, %v", dailies, err)
	}
	oldDetail, err := refresher.ResolveDetail(ctx, "daily_2026-10-27")
	if err != nil || oldDetail.Summary != "lead 2026-10-27" || detailCalls["/api/v1/dailies/2026-10-27"] != 1 {
		t.Fatalf("on-demand historical detail = %#v, %v; calls=%#v", oldDetail, err, detailCalls)
	}
}

func TestRefreshPreservesFetchedBodyWhenArchiveIndexChanges(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	indexVersion := 0
	detailCalls := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/items":
			_, _ = io.WriteString(w, itemsPageJSON(r.URL.Query().Get("mode"), r.URL.Query().Get("window"), `[]`, 0, false, ""))
		case "/api/v1/hot-topics":
			_, _ = io.WriteString(w, `{"schemaVersion":1,"count":0,"items":[],"notice":null}`)
		case "/api/v1/dailies":
			indexVersion++
			dates := []string{"2026-10-28", "2026-10-27"}
			if indexVersion > 1 {
				dates = []string{"2026-10-29", "2026-10-28", "2026-10-27"}
			}
			index := dailyIndexJSON(dates, "daily")
			if indexVersion > 2 {
				index = strings.Replace(index, `"daily 2026-10-27"`, `"daily changed 2026-10-27"`, 1)
			}
			_, _ = io.WriteString(w, index)
		case "/api/v1/weeklies", "/api/v1/monthlies":
			_, _ = io.WriteString(w, `{"schemaVersion":1,"count":0,"items":[],"notice":null}`)
		case "/api/v1/dailies/2026-10-28", "/api/v1/dailies/2026-10-29", "/api/v1/dailies/2026-10-27":
			detailCalls[r.URL.Path]++
			date := pathLast(r.URL.Path)
			_, _ = io.WriteString(w, dailyDetailJSON(date))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	refresher := NewRefresher(NewClient(srv.URL, srv.Client()), store)
	now := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	if err := refresher.Refresh(ctx, now); err != nil {
		t.Fatal(err)
	}
	if _, err := refresher.ResolveDetail(ctx, "daily_2026-10-27"); err != nil {
		t.Fatal(err)
	}
	if err := refresher.Refresh(ctx, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	old, err := store.Get(ctx, "daily_2026-10-27")
	if err != nil || old.Summary != "lead 2026-10-27" || len(old.Paragraphs) != 1 || old.Paragraphs[0].Text != "body 2026-10-27" {
		t.Fatalf("historical details after new edition = %#v, %v", old, err)
	}
	if detailCalls["/api/v1/dailies/2026-10-27"] != 1 {
		t.Fatalf("historical detail refetched without need: %#v", detailCalls)
	}
	if err := refresher.Refresh(ctx, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	checkpoint, _ := testCheckpointByName(t, store, "aihot:detail:daily_2026-10-27")
	if !checkpoint.NextFetchAt.Equal(now.Add(2 * time.Hour)) {
		t.Fatalf("changed index did not mark detail due: %#v", checkpoint)
	}
	if _, err := refresher.resolveDetailAt(ctx, "daily_2026-10-27", now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if detailCalls["/api/v1/dailies/2026-10-27"] != 2 {
		t.Fatalf("changed historical index was not revalidated: %#v", detailCalls)
	}
}

func TestRefreshTopicsDeleteOnlyWhenStoryIsExplicitlyMissing(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	dropped := content.Entry{ID: "topic_dropped", Kind: "topic", Title: "dropped", Paragraphs: []content.Paragraph{}}
	if err := store.ReplaceCollection(ctx, "aihot:topics", []content.Entry{dropped}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/items":
			_, _ = io.WriteString(w, itemsPageJSON(r.URL.Query().Get("mode"), r.URL.Query().Get("window"), `[]`, 0, false, ""))
		case "/api/v1/hot-topics":
			_, _ = io.WriteString(w, `{"schemaVersion":1,"count":2,"items":[{"rank":1,"id":"gone","title":"Gone","source":{"name":"Publisher"},"links":{"aihot":"https://aihot.news/hot/gone","original":"https://publisher.example/gone","story":"https://aihot.news/story/gone"},"sourceCount":1,"signalCount":1,"sourceNames":["Publisher"],"latestAt":"2026-10-28T10:00:00Z"},{"rank":2,"id":"kept","title":"Kept","source":{"name":"Publisher"},"links":{"aihot":"https://aihot.news/hot/kept","original":"https://publisher.example/kept"},"sourceCount":1,"signalCount":1,"sourceNames":["Publisher"],"latestAt":"2026-10-28T10:00:00Z"}],"notice":null}`)
		case "/api/v1/stories/gone":
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"secret":"must not be surfaced"}`)
		case "/api/v1/dailies", "/api/v1/weeklies", "/api/v1/monthlies":
			_, _ = io.WriteString(w, `{"schemaVersion":1,"count":0,"items":[],"notice":null}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	refresher := NewRefresher(NewClient(srv.URL, srv.Client()), store)
	if err := refresher.Refresh(ctx, time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	topics, err := store.Collection(ctx, "aihot:topics")
	if err != nil || len(topics) != 1 || topics[0].ID != "topic_kept" {
		t.Fatalf("topic collection = %#v, %v", topics, err)
	}
	if withdrawn, err := store.IsWithdrawn(ctx, "topic_gone"); err != nil || !withdrawn {
		t.Fatalf("missing story tombstone = %v, %v", withdrawn, err)
	}
	if withdrawn, err := store.IsWithdrawn(ctx, dropped.ID); err != nil || withdrawn {
		t.Fatalf("topic that left Top 10 tombstoned = %v, %v", withdrawn, err)
	}
	if member, err := store.HasMembership(ctx, dropped.ID); err != nil || member {
		t.Fatalf("dropped topic membership = %v, %v", member, err)
	}
}

func TestRefreshItemsUsesMinimumFreshnessAndSkipsUntilDue(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Cache-Control", "public, max-age=30")
		} else {
			w.Header().Set("Cache-Control", "max-age=3600")
		}
		_, _ = io.WriteString(w, itemsPageJSON(r.URL.Query().Get("mode"), r.URL.Query().Get("window"), `[]`, 0, false, ""))
	}))
	defer srv.Close()
	refresher := NewRefresher(NewClient(srv.URL, srv.Client()), store)
	now := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	if err := refresher.refreshItems(ctx, "selected", "7d", now); err != nil {
		t.Fatal(err)
	}
	checkpoint, ok := testCheckpointByName(t, store, "aihot:news:selected:7d")
	if !ok || !checkpoint.NextFetchAt.Equal(now.Add(10*time.Minute)) {
		t.Fatalf("news checkpoint with short max-age = %#v, exists=%v", checkpoint, ok)
	}
	if err := refresher.refreshItems(ctx, "selected", "7d", now.Add(9*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("fresh news source called before due time: calls=%d", calls)
	}
	if err := refresher.refreshItems(ctx, "selected", "7d", now.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	checkpoint, _ = testCheckpointByName(t, store, "aihot:news:selected:7d")
	if calls != 2 || !checkpoint.NextFetchAt.Equal(now.Add(70*time.Minute)) {
		t.Fatalf("news max-age checkpoint = %#v, calls=%d", checkpoint, calls)
	}
}

func TestRefreshArchiveUsesThirtyMinuteMinimumFreshness(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=60")
		_, _ = io.WriteString(w, dailyIndexJSON(nil, "daily"))
	}))
	defer srv.Close()
	refresher := NewRefresher(NewClient(srv.URL, srv.Client()), store)
	now := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	if err := refresher.refreshArchive(ctx, "daily", "/api/v1/dailies", 180, now); err != nil {
		t.Fatal(err)
	}
	checkpoint, ok := testCheckpointByName(t, store, "aihot:daily")
	if !ok || !checkpoint.NextFetchAt.Equal(now.Add(30*time.Minute)) {
		t.Fatalf("daily checkpoint with short max-age = %#v, exists=%v", checkpoint, ok)
	}
}

func TestRefreshItemsReturnsErrorWithoutCallingStaleSourceBeforeRetry(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	name := "aihot:news:selected:7d"
	now := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	checkpoint := content.SourceCheckpoint{Name: name, ETag: `"cached"`, FailureCount: 2, NextFetchAt: now.Add(time.Minute),
		Status: content.SourceStatus{Name: name, State: "stale", LastSuccessAt: now.Add(-time.Hour).Format(time.RFC3339Nano)}}
	if err := store.SaveCheckpoint(ctx, name, checkpoint); err != nil {
		t.Fatal(err)
	}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = io.WriteString(w, itemsPageJSON("selected", "7d", `[]`, 0, false, ""))
	}))
	defer srv.Close()
	refresher := NewRefresher(NewClient(srv.URL, srv.Client()), store)
	if err := refresher.refreshItems(ctx, "selected", "7d", now); err == nil {
		t.Fatal("stale source waiting for retry returned nil error")
	}
	if calls != 0 {
		t.Fatalf("stale source called before retry time: calls=%d", calls)
	}
	after, _ := testCheckpointByName(t, store, name)
	if after.FailureCount != checkpoint.FailureCount || !after.NextFetchAt.Equal(checkpoint.NextFetchAt) || after.Status.State != checkpoint.Status.State {
		t.Fatalf("stale retry hit changed checkpoint: before=%#v after=%#v", checkpoint, after)
	}
}

func TestRefreshItemsUsesRateLimitRetryAfterAndBoundedLinearFallback(t *testing.T) {
	tests := []struct {
		name          string
		previousFails int
		retryAfter    string
		wantDelay     time.Duration
	}{
		{name: "first fallback", wantDelay: time.Minute},
		{name: "second fallback", previousFails: 1, wantDelay: 2 * time.Minute},
		{name: "capped fallback", previousFails: 100, wantDelay: time.Hour},
		{name: "retry after", retryAfter: "120", wantDelay: 2 * time.Minute},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store := openTestStore(t)
			name := "aihot:news:selected:7d"
			now := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
			if tc.previousFails > 0 {
				previous := content.SourceCheckpoint{Name: name, FailureCount: tc.previousFails, NextFetchAt: now.Add(-time.Second),
					Status: content.SourceStatus{Name: name, State: "rate_limited"}}
				if err := store.SaveCheckpoint(ctx, name, previous); err != nil {
					t.Fatal(err)
				}
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.retryAfter != "" {
					w.Header().Set("Retry-After", tc.retryAfter)
				}
				w.WriteHeader(http.StatusTooManyRequests)
			}))
			defer srv.Close()
			refresher := NewRefresher(NewClient(srv.URL, srv.Client()), store)
			if err := refresher.refreshItems(ctx, "selected", "7d", now); err == nil {
				t.Fatal("429 response returned nil error")
			}
			checkpoint, ok := testCheckpointByName(t, store, name)
			if !ok || checkpoint.Status.State != "rate_limited" || checkpoint.FailureCount != tc.previousFails+1 || !checkpoint.NextFetchAt.Equal(now.Add(tc.wantDelay)) {
				t.Fatalf("429 checkpoint = %#v, exists=%v", checkpoint, ok)
			}
		})
	}
}

func TestRefreshExpiresUnreferencedRowsOnlyAfterCompleteSuccess(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	old := content.Entry{ID: "news_unreferenced", Kind: "news", Title: "old", Paragraphs: []content.Paragraph{}}
	project := content.Entry{ID: "project_kept", Kind: "project", Title: "project", Paragraphs: []content.Paragraph{}}
	if err := store.Upsert(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(ctx, project); err != nil {
		t.Fatal(err)
	}
	failNews := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/items":
			if failNews && r.URL.Query().Get("mode") == "selected" && r.URL.Query().Get("window") == "7d" {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = io.WriteString(w, itemsPageJSON(r.URL.Query().Get("mode"), r.URL.Query().Get("window"), `[]`, 0, false, ""))
		case "/api/v1/hot-topics":
			_, _ = io.WriteString(w, `{"schemaVersion":1,"count":0,"items":[],"notice":null}`)
		case "/api/v1/dailies", "/api/v1/weeklies", "/api/v1/monthlies":
			_, _ = io.WriteString(w, `{"schemaVersion":1,"count":0,"items":[],"notice":null}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	refresher := NewRefresher(NewClient(srv.URL, srv.Client()), store)
	now := time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := refresher.Refresh(ctx, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, old.ID); err == nil {
		t.Fatal("old unreferenced entry survived successful expiry")
	}
	if withdrawn, err := store.IsWithdrawn(ctx, old.ID); err != nil || withdrawn {
		t.Fatalf("expiry wrote withdrawal tombstone = %v, %v", withdrawn, err)
	}
	if _, err := store.Get(ctx, project.ID); err != nil {
		t.Fatalf("project was expired: %v", err)
	}

	if err := store.Upsert(ctx, old); err != nil {
		t.Fatal(err)
	}
	failNews = true
	if err := refresher.Refresh(ctx, now.Add(time.Hour)); err == nil {
		t.Fatal("partial refresh returned nil error")
	}
	if _, err := store.Get(ctx, old.ID); err != nil {
		t.Fatalf("failed refresh expired unreferenced entry: %v", err)
	}
}

func TestRefreshFetchesEachModelLeaderboardAsHTML(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	var leaderboardRequests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/items":
			_, _ = io.WriteString(w, itemsPageJSON(r.URL.Query().Get("mode"), r.URL.Query().Get("window"), `[]`, 0, false, ""))
		case "/api/v1/hot-topics", "/api/v1/dailies", "/api/v1/weeklies", "/api/v1/monthlies":
			_, _ = io.WriteString(w, `{"schemaVersion":1,"count":0,"items":[],"notice":null}`)
		case "/api/v1/codex-resets":
			w.WriteHeader(http.StatusNotFound)
		default:
			if r.URL.Path == "/leaderboard" || strings.HasPrefix(r.URL.Path, "/leaderboard/category/") {
				leaderboardRequests = append(leaderboardRequests, r.URL.Path+"|"+r.Header.Get("Accept"))
				_, _ = io.WriteString(w, "<html></html>")
				return
			}
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	refresher := NewRefresher(NewClient(srv.URL, srv.Client()), store)
	now := time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)
	if err := refresher.Refresh(ctx, now); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	want := []string{
		"/leaderboard|text/html",
		"/leaderboard/category/coding|text/html",
		"/leaderboard/category/reasoning|text/html",
		"/leaderboard/category/professional|text/html",
		"/leaderboard/category/knowledge|text/html",
	}
	if !reflect.DeepEqual(leaderboardRequests, want) {
		t.Fatalf("model leaderboard requests=%v, want %v", leaderboardRequests, want)
	}
}

func itemsPageJSON(mode, window, items string, count int, more bool, cursor string) string {
	next := "null"
	if cursor != "" {
		next = fmt.Sprintf("%q", cursor)
	}
	return fmt.Sprintf(`{"schemaVersion":1,"query":{"mode":%q,"category":null,"window":%q,"q":null,"by":"timeline","ordering":"timelineDesc"},"items":%s,"page":{"count":%d,"hasMore":%t,"nextCursor":%s},"notice":null}`, mode, window, items, count, more, next)
}

func dailyIndexJSON(dates []string, titlePrefix string) string {
	items := make([]string, 0, len(dates))
	for _, date := range dates {
		items = append(items, fmt.Sprintf(`{"date":%q,"generatedAt":"2026-10-28T08:00:00Z","leadTitle":%q,"leadParagraph":%q,"links":{"aihot":%q},"attribution":{"name":"AIHOT","url":"https://aihot.news"}}`, date, titlePrefix+" "+date, "lead "+date, "https://aihot.news/dailies/"+date))
	}
	return fmt.Sprintf(`{"schemaVersion":1,"count":%d,"items":[%s],"notice":null}`, len(items), strings.Join(items, ","))
}

func periodIndexJSON(kind string, periods []string) string {
	items := make([]string, 0, len(periods))
	for _, period := range periods {
		key := "week"
		if kind == "monthly" {
			key = "month"
		}
		items = append(items, fmt.Sprintf(`{%q:%q,"periodStart":"2026-10-01","periodEnd":"2026-10-28","generatedAt":"2026-10-28T08:00:00Z","headline":%q,"links":{"aihot":%q},"attribution":{"name":"AIHOT","url":"https://aihot.news"}}`, key, period, "Headline "+period, "https://aihot.news/"+kind+"lies/"+period))
	}
	return fmt.Sprintf(`{"schemaVersion":1,"count":%d,"items":[%s],"notice":null}`, len(items), strings.Join(items, ","))
}

func dailyDetailJSON(date string) string {
	return fmt.Sprintf(`{"schemaVersion":1,"report":{"date":%q,"generatedAt":"2026-10-28T08:00:00Z","windowStart":"2026-10-27T08:00:00Z","windowEnd":"2026-10-28T08:00:00Z","links":{"aihot":"https://aihot.news/dailies/%s"},"attribution":{"name":"AIHOT","url":"https://aihot.news"},"lead":{"title":"Daily %s","leadParagraph":"lead %s"},"sections":[{"label":"Models","items":[{"title":"item %s","summary":"body %s","source":{"name":"Publisher"},"links":{"aihot":"https://aihot.news/read/%s","original":"https://publisher.example/%s"},"publishedAt":"2026-10-28T07:00:00Z","attribution":{"name":"AIHOT","url":"https://aihot.news"}}]}],"flashes":[]},"notice":null}`, date, date, date, date, date, date, date, date)
}

func periodDetailJSON(kind, period string) string {
	key := "week"
	if kind == "monthly" {
		key = "month"
	}
	return fmt.Sprintf(`{"schemaVersion":1,"report":{"periodStart":"2026-10-01","periodEnd":"2026-10-28","generatedAt":"2026-10-28T08:00:00Z","windowStart":"2026-10-01T08:00:00Z","windowEnd":"2026-10-28T08:00:00Z","links":{"aihot":"https://aihot.news/%s/%s"},"attribution":{"name":"AIHOT","url":"https://aihot.news"},"headline":"Headline %s","overview":"Overview %s","sections":[],%q:%q},"notice":null}`, kind, period, period, period, key, period)
}

func pathLast(value string) string {
	parts := strings.Split(value, "/")
	return parts[len(parts)-1]
}

func testCheckpointByName(t *testing.T, store content.CatalogStore, name string) (content.SourceCheckpoint, bool) {
	t.Helper()
	checkpoints, err := store.Checkpoints(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, checkpoint := range checkpoints {
		if checkpoint.Name == name {
			return checkpoint, true
		}
	}
	return content.SourceCheckpoint{}, false
}
