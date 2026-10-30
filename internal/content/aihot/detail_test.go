package aihot

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"skillmanage/internal/content"
)

func TestResolveDetailFetchesAndRevalidatesPerEntry(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	entry := dailyIndexEntry("2026-10-28")
	if err := store.ReplaceCollection(ctx, "aihot:daily", []content.Entry{entry}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/v1/dailies/2026-10-28" {
			t.Errorf("detail path = %q", r.URL.Path)
		}
		if calls == 1 {
			if r.Header.Get("If-None-Match") != "" {
				t.Errorf("first request If-None-Match = %q", r.Header.Get("If-None-Match"))
			}
			w.Header().Set("ETag", `"detail-v1"`)
			w.Header().Set("Cache-Control", "max-age=300")
			_, _ = io.WriteString(w, dailyDetailJSON("2026-10-28"))
			return
		}
		if r.Header.Get("If-None-Match") != `"detail-v1"` {
			t.Errorf("revalidation If-None-Match = %q", r.Header.Get("If-None-Match"))
		}
		w.Header().Set("ETag", `"detail-v1"`)
		w.Header().Set("Cache-Control", "max-age=300")
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()
	refresher := NewRefresher(NewClient(srv.URL, srv.Client()), store)
	first, err := refresher.ResolveDetail(ctx, entry.ID)
	if err != nil || first.Summary != "lead 2026-10-28" || len(first.Paragraphs) != 1 {
		t.Fatalf("first ResolveDetail() = %#v, %v", first, err)
	}
	checkpointName := "aihot:detail:" + entry.ID
	checkpoint, ok := testCheckpointByName(t, store, checkpointName)
	if !ok || checkpoint.ETag != `"detail-v1"` || checkpoint.Status.LastSuccessAt == "" || !checkpoint.NextFetchAt.After(time.Now()) {
		t.Fatalf("detail checkpoint after 200 = %#v, exists=%v", checkpoint, ok)
	}
	checkpoint.NextFetchAt = time.Now().Add(-time.Second)
	if err := store.SaveCheckpoint(ctx, checkpointName, checkpoint); err != nil {
		t.Fatal(err)
	}
	second, err := refresher.ResolveDetail(ctx, entry.ID)
	if err != nil || second.Summary != first.Summary || len(second.Paragraphs) != 1 || calls != 2 {
		t.Fatalf("304 ResolveDetail() = %#v, %v; calls=%d", second, err, calls)
	}
	stored, err := store.Get(ctx, entry.ID)
	if err != nil || stored.Summary != first.Summary || len(stored.Paragraphs) != 1 {
		t.Fatalf("stored detail after 304 = %#v, %v", stored, err)
	}
}

func TestResolveDetailCachesValidReportWithNoParagraphs(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	entry := dailyIndexEntry("2026-10-28")
	if err := store.ReplaceCollection(ctx, "aihot:daily", []content.Entry{entry}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Cache-Control", "max-age=300")
		_, _ = io.WriteString(w, `{"schemaVersion":1,"report":{"date":"2026-10-28","generatedAt":"2026-10-28T08:00:00Z","windowStart":"2026-10-27T08:00:00Z","windowEnd":"2026-10-28T08:00:00Z","links":{"aihot":"https://aihot.news/dailies/2026-10-28"},"lead":{"title":"Quiet day","leadParagraph":"No new items."},"sections":[],"flashes":[]},"notice":null}`)
	}))
	defer srv.Close()
	refresher := NewRefresher(NewClient(srv.URL, srv.Client()), store)
	first, err := refresher.ResolveDetail(ctx, entry.ID)
	if err != nil || first.Paragraphs == nil || len(first.Paragraphs) != 0 {
		t.Fatalf("empty report detail = %#v, %v", first, err)
	}
	second, err := refresher.ResolveDetail(ctx, entry.ID)
	if err != nil || second.Summary != "No new items." || calls != 1 {
		t.Fatalf("cached empty report = %#v, %v; calls=%d", second, err, calls)
	}
}

func TestResolveDetailReturnsErrorAndPreservesBodyOnTemporaryFailure(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	entry := dailyIndexEntry("2026-10-28")
	if err := store.ReplaceCollection(ctx, "aihot:daily", []content.Entry{entry}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("ETag", `"detail-v1"`)
			w.Header().Set("Cache-Control", "max-age=300")
			_, _ = io.WriteString(w, dailyDetailJSON("2026-10-28"))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"token":"do-not-leak"}`)
	}))
	defer srv.Close()
	refresher := NewRefresher(NewClient(srv.URL, srv.Client()), store)
	if _, err := refresher.ResolveDetail(ctx, entry.ID); err != nil {
		t.Fatal(err)
	}
	checkpointName := "aihot:detail:" + entry.ID
	checkpoint, _ := testCheckpointByName(t, store, checkpointName)
	checkpoint.NextFetchAt = time.Now().Add(-time.Second)
	if err := store.SaveCheckpoint(ctx, checkpointName, checkpoint); err != nil {
		t.Fatal(err)
	}
	if _, err := refresher.ResolveDetail(ctx, entry.ID); err == nil {
		t.Fatal("temporary upstream failure returned nil error")
	} else if strings.Contains(err.Error(), "do-not-leak") || strings.Contains(err.Error(), "token") {
		t.Fatalf("upstream body leaked in error: %v", err)
	}
	stored, err := store.Get(ctx, entry.ID)
	if err != nil || stored.Summary != "lead 2026-10-28" || len(stored.Paragraphs) != 1 {
		t.Fatalf("cached detail after failure = %#v, %v", stored, err)
	}
	if member, err := store.HasMembership(ctx, entry.ID); err != nil || !member {
		t.Fatalf("membership after failure = %v, %v", member, err)
	}
	checkpoint, _ = testCheckpointByName(t, store, checkpointName)
	if checkpoint.Status.State != "stale" {
		t.Fatalf("checkpoint after failure = %#v", checkpoint)
	}
}

func TestA04ReviewStaleRetryDoesNotReturnSuccessfulDetail(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	entry := dailyIndexEntry("2026-10-27")
	if err := store.ReplaceCollection(ctx, "aihot:daily", []content.Entry{entry}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Cache-Control", "max-age=1")
			_, _ = io.WriteString(w, dailyDetailJSON("2026-10-27"))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	refresher := NewRefresher(NewClient(srv.URL, srv.Client()), store)
	now := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	if _, err := refresher.resolveDetailAt(ctx, entry.ID, now); err != nil {
		t.Fatal(err)
	}
	failureAt := now.Add(31 * time.Minute)
	if _, err := refresher.resolveDetailAt(ctx, entry.ID, failureAt); err == nil {
		t.Fatal("initial upstream failure returned nil error")
	}
	checkpointName := "aihot:detail:" + entry.ID
	before, ok := testCheckpointByName(t, store, checkpointName)
	if !ok || before.Status.State != "stale" || !before.NextFetchAt.Equal(failureAt.Add(30*time.Minute)) {
		t.Fatalf("retry checkpoint after 503 = %#v, exists=%v", before, ok)
	}
	entryBefore, err := store.Get(ctx, entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := refresher.resolveDetailAt(ctx, entry.ID, failureAt.Add(time.Minute)); err == nil {
		t.Fatalf("stale retry hit returned successful nil error; HTTP calls=%d", calls)
	}
	if calls != 2 {
		t.Fatalf("stale retry hit made an upstream call: calls=%d", calls)
	}
	entryAfter, err := store.Get(ctx, entry.ID)
	if err != nil || entryAfter.Summary != entryBefore.Summary || len(entryAfter.Paragraphs) != len(entryBefore.Paragraphs) || entryAfter.Paragraphs[0].Text != entryBefore.Paragraphs[0].Text {
		t.Fatalf("stale retry hit changed cached body: before=%#v after=%#v err=%v", entryBefore, entryAfter, err)
	}
	if member, err := store.HasMembership(ctx, entry.ID); err != nil || !member {
		t.Fatalf("stale retry hit changed membership: member=%v err=%v", member, err)
	}
	after, ok := testCheckpointByName(t, store, checkpointName)
	if !ok || after.Status.State != before.Status.State || !after.NextFetchAt.Equal(before.NextFetchAt) || after.FailureCount != before.FailureCount {
		t.Fatalf("stale retry hit changed deadline/checkpoint: before=%#v after=%#v exists=%v", before, after, ok)
	}
}

func TestResolveDetailRequiresMembershipAndExplicitWithdrawalTombstones(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	entry := dailyIndexEntry("2026-10-28")
	if err := store.ReplaceCollection(ctx, "aihot:daily", []content.Entry{entry}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/dailies/2026-10-28" {
			t.Errorf("unsafe detail path = %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusGone)
	}))
	defer srv.Close()
	refresher := NewRefresher(NewClient(srv.URL, srv.Client()), store)
	if _, err := refresher.ResolveDetail(ctx, "daily_2026-10-28/../secret"); !errors.Is(err, content.ErrBadQuery) {
		t.Fatalf("invalid ID error = %v", err)
	}
	if _, err := refresher.ResolveDetail(ctx, "daily_2026-10-27"); !errors.Is(err, content.ErrNotFound) {
		t.Fatalf("nonmember error = %v", err)
	}
	if _, err := refresher.ResolveDetail(ctx, entry.ID); !errors.Is(err, content.ErrWithdrawn) {
		t.Fatalf("gone report error = %v", err)
	}
	if withdrawn, err := store.IsWithdrawn(ctx, entry.ID); err != nil || !withdrawn {
		t.Fatalf("explicit detail withdrawal = %v, %v", withdrawn, err)
	}
	if member, err := store.HasMembership(ctx, entry.ID); err != nil || member {
		t.Fatalf("withdrawn membership = %v, %v", member, err)
	}
}

func TestResolveDetailRejects304WithoutRetainedBody(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	entry := dailyIndexEntry("2026-10-28")
	if err := store.ReplaceCollection(ctx, "aihot:daily", []content.Entry{entry}); err != nil {
		t.Fatal(err)
	}
	checkpointName := "aihot:detail:" + entry.ID
	if err := store.SaveCheckpoint(ctx, checkpointName, content.SourceCheckpoint{Name: checkpointName, ETag: `"bare-index"`, NextFetchAt: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()
	refresher := NewRefresher(NewClient(srv.URL, srv.Client()), store)
	if _, err := refresher.ResolveDetail(ctx, entry.ID); err == nil {
		t.Fatal("304 without a retained body returned success")
	}
	checkpoint, _ := testCheckpointByName(t, store, checkpointName)
	if checkpoint.Status.State != "stale" || checkpoint.Status.LastSuccessAt != "" {
		t.Fatalf("bare-index checkpoint = %#v", checkpoint)
	}
}

func dailyIndexEntry(date string) content.Entry {
	return content.Entry{ID: "daily_" + date, Kind: "daily", Title: "Daily " + date, Period: date,
		Source:       content.Source{Name: "AIHOT", URL: "https://aihot.news"},
		Attribution:  content.Source{Name: "AIHOT", URL: "https://aihot.news"},
		CanonicalURL: "https://aihot.news/dailies/" + date, Paragraphs: []content.Paragraph{}}
}
