package aihot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"skillmanage/internal/content"
)

func resetCalendarJSON(events string, count int) string {
	return fmt.Sprintf(`{"schemaVersion":1,"timezone":"Asia/Shanghai","today":"2026-10-29","checkedAt":"2026-10-29T09:00:00+08:00","historyFrom":"2026-09-01T00:00:00+08:00","count":%d,"events":%s}`, count, events)
}

func resetCalendarEventJSON(id string) string {
	return fmt.Sprintf(`{"id":%q,"type":"direct_reset","label":"全员重置","displayLabel":"额度重置","status":"confirmed","title":"额度重置已完成","scope":"","createdAt":"2026-10-28T10:00:00+08:00","updatedAt":"2026-10-29T09:00:00+08:00","confirmedAt":"2026-10-28T10:01:00+08:00","occurredOn":null,"schedule":{"precision":"deadline","from":"2026-10-28T15:00:00+08:00","through":"2026-10-28T15:00:00+08:00","label":"预计北京时间 15:00 前"},"posts":[{"id":"post-1","publishedAt":"2026-10-28T10:01:00+08:00","stage":"确认完成","text":"重置已完成。","url":"https://x.com/example/status/1"}],"url":"https://aihot.news/codex-reset"}`, id)
}

func TestFetchResetCalendarUsesFixedFullSnapshotAndConditionalETag(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/v1/codex-resets" || r.URL.RawQuery != "" {
			t.Errorf("request = %s?%s, want fixed full snapshot without query", r.URL.Path, r.URL.RawQuery)
		}
		if r.Header.Get("Accept") != "application/json" || !strings.Contains(r.Header.Get("User-Agent"), "SkillManager-contentd/") {
			t.Errorf("request headers = %#v", r.Header)
		}
		w.Header().Set("ETag", `"calendar-v1"`)
		w.Header().Set("Cache-Control", "max-age=60")
		if r.Header.Get("If-None-Match") == `"calendar-v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, resetCalendarJSON("["+resetCalendarEventJSON("reset-1")+"]", 1))
	}))
	defer srv.Close()
	client := NewClient(srv.URL, srv.Client())
	first, err := client.FetchResetCalendar(context.Background(), "")
	if err != nil || first.NotModified || first.ETag != `"calendar-v1"` || len(first.Snapshot.Events) != 1 {
		t.Fatalf("first fetch = %#v, %v", first, err)
	}
	event := first.Snapshot.Events[0]
	if event.DisplayLabel != "额度重置" || event.OccurredOn != nil || event.ConfirmedAt == nil || event.Schedule == nil || len(event.Posts) != 1 {
		t.Fatalf("event source fields were not preserved: %#v", event)
	}
	second, err := client.FetchResetCalendar(context.Background(), `"calendar-v1"`)
	if err != nil || !second.NotModified || calls != 2 {
		t.Fatalf("conditional fetch = %#v, %v, calls=%d", second, err, calls)
	}
}

func TestNormalizeResetCalendarRejectsIncompleteOrUnsafeSnapshots(t *testing.T) {
	valid := resetCalendarJSON("["+resetCalendarEventJSON("reset-1")+"]", 1)
	cases := []struct {
		name string
		body string
	}{
		{name: "wrong count", body: strings.Replace(valid, `"count":1`, `"count":2`, 1)},
		{name: "duplicate IDs", body: resetCalendarJSON("["+resetCalendarEventJSON("reset-1")+","+resetCalendarEventJSON("reset-1")+"]", 2)},
		{name: "unknown event type", body: strings.Replace(valid, `"direct_reset"`, `"unknown"`, 1)},
		{name: "invalid event timestamp", body: strings.Replace(valid, `2026-10-28T10:00:00+08:00`, `yesterday`, 1)},
		{name: "unsafe source URL", body: strings.Replace(valid, `https://aihot.news/codex-reset`, `javascript:alert(1)`, 1)},
		{name: "invalid occurred date", body: strings.Replace(valid, `"occurredOn":null`, `"occurredOn":"2026-02-30"`, 1)},
		{name: "mismatched type and label", body: strings.Replace(valid, `"direct_reset"`, `"reset_credit"`, 1)},
		{name: "missing schedule timestamp", body: strings.Replace(valid, `"from":"2026-10-28T15:00:00+08:00"`, `"from":null`, 1)},
		{name: "null event list", body: strings.Replace(valid, `"events":[`, `"events":null,"ignored":[`, 1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var wire resetCalendarWire
			if err := json.Unmarshal([]byte(tc.body), &wire); err != nil {
				if tc.name == "null event list" {
					return
				}
				t.Fatalf("fixture is invalid JSON: %v", err)
			}
			if _, err := normalizeResetCalendar(wire); err == nil {
				t.Fatal("invalid snapshot was accepted")
			}
		})
	}
}

func TestRefreshResetCalendarCachesFullSnapshotsAndKeepsFailuresStale(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	calls := 0
	mode := "initial"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("ETag", `"calendar-v1"`)
		w.Header().Set("Cache-Control", "max-age=1")
		switch mode {
		case "initial":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, resetCalendarJSON("["+resetCalendarEventJSON("reset-1")+"]", 1))
		case "not-modified":
			if r.Header.Get("If-None-Match") != `"calendar-v1"` {
				t.Errorf("If-None-Match=%q", r.Header.Get("If-None-Match"))
			}
			w.WriteHeader(http.StatusNotModified)
		case "rate-limited":
			w.Header().Set("Retry-After", "1200")
			w.WriteHeader(http.StatusTooManyRequests)
		case "withdrawn":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, resetCalendarJSON("[]", 0))
		case "malformed":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, resetCalendarJSON("["+resetCalendarEventJSON("reset-2")+"]", 2))
		}
	}))
	defer srv.Close()
	refresher := NewRefresher(NewClient(srv.URL, srv.Client()), store)
	now := time.Date(2026, 10, 29, 1, 0, 0, 0, time.UTC)
	if err := refresher.refreshResetCalendar(ctx, now); err != nil {
		t.Fatal(err)
	}
	checkpoint, exists, err := refresher.checkpoint(ctx, "aihot:codex-resets")
	if err != nil || !exists || checkpoint.Status.State != "fresh" || !checkpoint.NextFetchAt.Equal(now.Add(10*time.Minute)) {
		t.Fatalf("checkpoint = %#v, exists=%v err=%v", checkpoint, exists, err)
	}
	// Keep catalog-time assertions aligned with the synthetic scheduler clock.
	catalog := content.NewCatalog(store, func() time.Time { return now })
	calendar, err := catalog.ResetCalendar(ctx)
	if err != nil || calendar.Meta.Stale || len(calendar.Events) != 1 {
		t.Fatalf("fresh calendar = %#v, %v", calendar, err)
	}

	mode = "not-modified"
	if err := refresher.refreshResetCalendar(ctx, now.Add(5*time.Minute)); err != nil || calls != 1 {
		t.Fatalf("refresh before 10 minute minimum = err %v, calls %d", err, calls)
	}
	if err := refresher.refreshResetCalendar(ctx, now.Add(10*time.Minute)); err != nil || calls != 2 {
		t.Fatalf("304 refresh = err %v, calls %d", err, calls)
	}

	mode = "rate-limited"
	failureAt := now.Add(20 * time.Minute)
	if err := refresher.refreshResetCalendar(ctx, failureAt); err == nil {
		t.Fatal("429 response was accepted")
	}
	checkpoint, _, err = refresher.checkpoint(ctx, "aihot:codex-resets")
	if err != nil || checkpoint.Status.State != "rate_limited" || !checkpoint.NextFetchAt.Equal(failureAt.Add(20*time.Minute)) {
		t.Fatalf("rate-limit checkpoint = %#v, err=%v", checkpoint, err)
	}
	calendar, err = catalog.ResetCalendar(ctx)
	if err != nil || !calendar.Meta.Stale || calendar.Meta.LastError == "" || len(calendar.Events) != 1 {
		t.Fatalf("stale cached calendar = %#v, %v", calendar, err)
	}

	mode = "withdrawn"
	if err := refresher.refreshResetCalendar(ctx, failureAt.Add(19*time.Minute)); err == nil {
		t.Fatal("refresh during Retry-After window should remain pending")
	}
	if err := refresher.refreshResetCalendar(ctx, failureAt.Add(20*time.Minute)); err != nil {
		t.Fatalf("full snapshot replacement after retry = %v", err)
	}
	calendar, err = catalog.ResetCalendar(ctx)
	if err != nil || len(calendar.Events) != 0 || calendar.Meta.Stale {
		t.Fatalf("withdrawn event remained cached: %#v, %v", calendar, err)
	}

	mode = "malformed"
	if err := refresher.refreshResetCalendar(ctx, failureAt.Add(40*time.Minute)); err == nil {
		t.Fatal("malformed snapshot was accepted")
	}
	stored, err := store.LoadResetCalendar(ctx)
	if err != nil || len(stored.Events) != 0 {
		t.Fatalf("bad snapshot replaced the valid empty calendar: %#v, %v", stored, err)
	}
}

func TestResetCalendarFailureDoesNotEnterNewsFeedStatus(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	if err := store.SaveCheckpoint(ctx, "aihot:news:selected:7d", content.SourceCheckpoint{Status: content.SourceStatus{Name: "aihot:news:selected:7d", State: "fresh", LastSuccessAt: "2026-10-29T00:00:00Z"}}); err != nil {
		t.Fatal(err)
	}
	calendar := content.ResetCalendarSnapshot{Timezone: "Asia/Shanghai", Events: []content.ResetCalendarEvent{}}
	if err := store.SaveResetCalendar(ctx, calendar); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCheckpoint(ctx, "aihot:codex-resets", content.SourceCheckpoint{Status: content.SourceStatus{Name: "aihot:codex-resets", State: "stale"}}); err != nil {
		t.Fatal(err)
	}
	page, err := content.NewCatalog(store, nil).Feed(ctx, content.Query{Kind: "news", View: "selected", Window: "7d"})
	if err != nil || page.Meta.Stale {
		t.Fatalf("news feed status includes calendar failure: stale=%v err=%v", page.Meta.Stale, err)
	}
}
