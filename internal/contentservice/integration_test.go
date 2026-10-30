package contentservice

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"skillmanage/internal/content"
	"skillmanage/internal/content/aihot"
	"skillmanage/internal/content/sqlite"
)

func TestA08RealSQLiteRefresherCatalogHTTPIntegration(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "content.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	const notice = `{"type":"partial","details":{"count":9007199254740993}}`
	const firstItem = `{"id":"first","title":"First item","originalTitle":null,"summary":"first summary","source":{"name":"Publisher"},"links":{"aihot":"https://aihot.news/read/first","original":"https://publisher.example/first"},"publishedAt":null,"discoveredAt":"2026-10-28T10:00:00Z","category":null,"score":null,"selected":true,"reason":null}`
	const windowItem = `{"id":"window","title":"Window item","originalTitle":null,"summary":"window summary","source":{"name":"Publisher"},"links":{"aihot":"https://aihot.news/read/window","original":"https://publisher.example/window"},"publishedAt":null,"discoveredAt":"2026-10-28T09:00:00Z","category":null,"score":null,"selected":true,"reason":null}`
	const replacementItem = `{"id":"replacement","title":"Incomplete replacement","originalTitle":null,"summary":"replacement summary","source":{"name":"Publisher"},"links":{"aihot":"https://aihot.news/read/replacement","original":"https://publisher.example/replacement"},"publishedAt":null,"discoveredAt":"2026-10-28T11:00:00Z","category":null,"score":null,"selected":true,"reason":null}`
	const storyResponse = `{"schemaVersion":1,"story":{"publicId":"story-1","title":"Expanded story","latest":"latest update","digest":"story digest","links":{"aihot":"https://aihot.news/stories/story-1"},"reports":[{"id":"report-1","title":"Publisher report","summary":"report summary","source":{"name":"Publisher","firstParty":false},"publishedAt":"2026-10-28T09:30:00Z","links":{"aihot":"https://aihot.news/reports/report-1","original":"https://publisher.example/report-1"}}]},"notice":null}`
	const topicsResponse = `{"schemaVersion":1,"count":1,"items":[{"rank":1,"id":"story-1","title":"Hot topic","latestAt":"2026-10-28T09:00:00Z","source":{"name":"Publisher"},"links":{"aihot":"https://aihot.news/hot/story-1","original":"https://publisher.example/topic-1","story":"https://aihot.news/stories/story-1"}}],"notice":null}`

	const initial, partial, windowExit = 0, 1, 2
	var stateMu sync.Mutex
	phase := initial
	itemPageTwoCalls := 0
	detailCalls := map[string]int{}
	storyCalls := 0
	setPhase := func(value int) {
		stateMu.Lock()
		defer stateMu.Unlock()
		phase = value
	}
	getPhase := func() int {
		stateMu.Lock()
		defer stateMu.Unlock()
		return phase
	}
	addItemPageTwoCall := func() {
		stateMu.Lock()
		defer stateMu.Unlock()
		itemPageTwoCalls++
	}
	getItemPageTwoCalls := func() int {
		stateMu.Lock()
		defer stateMu.Unlock()
		return itemPageTwoCalls
	}
	addDetailCall := func(path string) {
		stateMu.Lock()
		defer stateMu.Unlock()
		detailCalls[path]++
	}
	getDetailCall := func(path string) int {
		stateMu.Lock()
		defer stateMu.Unlock()
		return detailCalls[path]
	}
	addStoryCall := func() {
		stateMu.Lock()
		defer stateMu.Unlock()
		storyCalls++
	}
	getStoryCalls := func() int {
		stateMu.Lock()
		defer stateMu.Unlock()
		return storyCalls
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/items":
			mode, window, cursor := r.URL.Query().Get("mode"), r.URL.Query().Get("window"), r.URL.Query().Get("cursor")
			etag := fmt.Sprintf(`"items-%s-%s-v1"`, mode, window)
			if mode == "selected" && window == "7d" {
				switch getPhase() {
				case initial:
					etag = `"items-selected-7d-v1"`
					if cursor == "" {
						w.Header().Set("ETag", etag)
						_, _ = io.WriteString(w, itemsResponseJSON(mode, window, []string{firstItem}, true, "page-2", notice))
						return
					}
					if cursor != "page-2" {
						t.Errorf("initial second-page cursor = %q, want page-2", cursor)
					}
					_, _ = io.WriteString(w, itemsResponseJSON(mode, window, []string{windowItem}, false, "", "null"))
					return
				case partial:
					if cursor == "" {
						if got := r.Header.Get("If-None-Match"); got != `"items-selected-7d-v1"` {
							t.Errorf("due refresh If-None-Match = %q, want prior ETag", got)
						}
						w.Header().Set("ETag", `"items-selected-7d-v2"`)
						_, _ = io.WriteString(w, itemsResponseJSON(mode, window, []string{replacementItem}, true, "fail-page-2", "null"))
						return
					}
					if cursor != "fail-page-2" {
						t.Errorf("partial second-page cursor = %q, want fail-page-2", cursor)
					}
					addItemPageTwoCall()
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = io.WriteString(w, `{"detail":"upstream failure body must not leak"}`)
					return
				case windowExit:
					if cursor != "" {
						t.Errorf("window-exit unexpectedly requested cursor %q", cursor)
					}
					if got := r.Header.Get("If-None-Match"); got != `"items-selected-7d-v1"` {
						t.Errorf("window-exit If-None-Match = %q, want retained successful ETag", got)
					}
					w.Header().Set("ETag", `"items-selected-7d-v3"`)
					_, _ = io.WriteString(w, itemsResponseJSON(mode, window, nil, false, "", "null"))
					return
				}
			}

			if cursor != "" {
				t.Errorf("unexpected cursor for %s/%s: %q", mode, window, cursor)
			}
			w.Header().Set("ETag", etag)
			if r.Header.Get("If-None-Match") != "" {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			_, _ = io.WriteString(w, itemsResponseJSON(mode, window, nil, false, "", "null"))
		case "/api/v1/hot-topics":
			w.Header().Set("ETag", `"topics-v1"`)
			if r.Header.Get("If-None-Match") != "" {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			_, _ = io.WriteString(w, topicsResponse)
		case "/api/v1/stories/story-1":
			addStoryCall()
			_, _ = io.WriteString(w, storyResponse)
		case "/api/v1/dailies":
			w.Header().Set("ETag", `"daily-index-v1"`)
			if r.Header.Get("If-None-Match") != "" {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			_, _ = io.WriteString(w, dailyIndexResponseJSON())
		case "/api/v1/dailies/2026-10-28":
			addDetailCall(r.URL.Path)
			_, _ = io.WriteString(w, dailyDetailResponseJSON("2026-10-28"))
		case "/api/v1/dailies/2026-10-27":
			addDetailCall(r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"detail":"report intentionally withdrawn"}`)
		case "/api/v1/weeklies", "/api/v1/monthlies":
			w.Header().Set("ETag", `"empty-archive-v1"`)
			if r.Header.Get("If-None-Match") != "" {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			_, _ = io.WriteString(w, `{"schemaVersion":1,"count":0,"items":[],"notice":null}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	refresher := aihot.NewRefresher(aihot.NewClient(upstream.URL, upstream.Client()), store)
	now := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	if err := refresher.Refresh(ctx, now); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}
	catalog := content.NewCatalog(store, func() time.Time { return now })
	catalog.SetDetailLoader(refresher.ResolveDetail)
	handler := NewHandler(catalog)

	firstFeed := serviceRequest(handler, "/v1/feed?kind=news&view=selected&window=7d", "")
	if firstFeed.Code != http.StatusOK {
		t.Fatalf("initial news feed status=%d body=%q", firstFeed.Code, firstFeed.Body.String())
	}
	var newsPage content.Page
	if err := json.Unmarshal(firstFeed.Body.Bytes(), &newsPage); err != nil {
		t.Fatalf("decode initial news feed: %v; body=%q", err, firstFeed.Body.String())
	}
	if len(newsPage.Items) != 2 || newsPage.Items[0].ID != "news_first" || newsPage.Items[1].ID != "news_window" {
		t.Fatalf("two-page item collection = %#v", newsPage.Items)
	}
	if !strings.Contains(firstFeed.Body.String(), `"count":9007199254740993`) {
		t.Fatalf("notice number token was not preserved in HTTP JSON: %s", firstFeed.Body.String())
	}
	secondFeed := serviceRequest(handler, "/v1/feed?kind=news&view=selected&window=7d", firstFeed.Header().Get("ETag"))
	if secondFeed.Code != http.StatusNotModified || secondFeed.Body.Len() != 0 {
		t.Fatalf("conditional feed status=%d ETag=%q body=%q", secondFeed.Code, secondFeed.Header().Get("ETag"), secondFeed.Body.String())
	}

	dailyIndex := serviceRequest(handler, "/v1/feed?kind=daily", "")
	var dailyPage content.Page
	if dailyIndex.Code != http.StatusOK || json.Unmarshal(dailyIndex.Body.Bytes(), &dailyPage) != nil || len(dailyPage.Items) != 2 {
		t.Fatalf("daily archive index status=%d items=%#v body=%q", dailyIndex.Code, dailyPage.Items, dailyIndex.Body.String())
	}
	if getDetailCall("/api/v1/dailies/2026-10-28") != 1 {
		t.Fatalf("latest daily detail was not fetched during refresh: calls=%d", getDetailCall("/api/v1/dailies/2026-10-28"))
	}
	dailyDetail := serviceRequest(handler, "/v1/entries/daily_2026-10-28", "")
	var dailyResult content.EntryResult
	if dailyDetail.Code != http.StatusOK || json.Unmarshal(dailyDetail.Body.Bytes(), &dailyResult) != nil || dailyResult.Entry.Summary != "lead 2026-10-28" || len(dailyResult.Entry.Paragraphs) != 1 {
		t.Fatalf("daily detail status=%d result=%#v body=%q", dailyDetail.Code, dailyResult.Entry, dailyDetail.Body.String())
	}
	topicFeed := serviceRequest(handler, "/v1/feed?kind=topic", "")
	var topicPage content.Page
	if topicFeed.Code != http.StatusOK || json.Unmarshal(topicFeed.Body.Bytes(), &topicPage) != nil || len(topicPage.Items) != 1 {
		t.Fatalf("topic feed status=%d body=%q", topicFeed.Code, topicFeed.Body.String())
	}
	if topicPage.Items[0].Title != "Expanded story" || len(topicPage.Items[0].Paragraphs) != 1 || getStoryCalls() != 1 {
		t.Fatalf("hot story detail not integrated: item=%#v story calls=%d", topicPage.Items[0], getStoryCalls())
	}

	setPhase(partial)
	failedAt := now.Add(11 * time.Minute)
	if err := refresher.Refresh(ctx, failedAt); err == nil {
		t.Fatal("partial second page refresh succeeded")
	}
	if getItemPageTwoCalls() != 1 {
		t.Fatalf("failed second page requests=%d, want 1", getItemPageTwoCalls())
	}
	stored, err := store.Collection(ctx, "aihot:news:selected:7d")
	if err != nil || len(stored) != 2 || stored[0].ID != "news_first" || stored[1].ID != "news_window" {
		t.Fatalf("partial refresh overwrote successful collection: entries=%#v err=%v", stored, err)
	}
	checkpoints, err := store.Checkpoints(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var selectedCheckpoint content.SourceCheckpoint
	for _, checkpoint := range checkpoints {
		if checkpoint.Name == "aihot:news:selected:7d" {
			selectedCheckpoint = checkpoint
		}
	}
	if selectedCheckpoint.Status.State != "stale" || selectedCheckpoint.Status.LastSuccessAt != now.Format(time.RFC3339Nano) {
		t.Fatalf("partial refresh checkpoint = %#v", selectedCheckpoint)
	}
	staleFeed := serviceRequest(handler, "/v1/feed?kind=news&view=selected&window=7d", "")
	var stalePage content.Page
	if staleFeed.Code != http.StatusOK || json.Unmarshal(staleFeed.Body.Bytes(), &stalePage) != nil || len(stalePage.Items) != 2 || !stalePage.Meta.Stale {
		t.Fatalf("stale cache response status=%d page=%#v body=%q", staleFeed.Code, stalePage, staleFeed.Body.String())
	}
	if missingReplacement := serviceRequest(handler, "/v1/entries/news_replacement", ""); missingReplacement.Code != http.StatusNotFound {
		t.Fatalf("partial replacement detail status=%d body=%q, want 404", missingReplacement.Code, missingReplacement.Body.String())
	}

	setPhase(windowExit)
	if err := refresher.Refresh(ctx, failedAt.Add(11*time.Minute)); err != nil {
		t.Fatalf("complete window refresh: %v", err)
	}
	windowExitDetail := serviceRequest(handler, "/v1/entries/news_window", "")
	if windowExitDetail.Code != http.StatusNotFound {
		t.Fatalf("window-exited item status=%d body=%q, want 404 without a tombstone", windowExitDetail.Code, windowExitDetail.Body.String())
	}
	if withdrawn, err := store.IsWithdrawn(ctx, "news_window"); err != nil || withdrawn {
		t.Fatalf("rolling-window exit wrote tombstone=%v err=%v", withdrawn, err)
	}

	withdrawnDaily := serviceRequest(handler, "/v1/entries/daily_2026-10-27", "")
	if withdrawnDaily.Code != http.StatusGone {
		t.Fatalf("explicit upstream 404 detail status=%d body=%q, want 410", withdrawnDaily.Code, withdrawnDaily.Body.String())
	}
	if withdrawn, err := store.IsWithdrawn(ctx, "daily_2026-10-27"); err != nil || !withdrawn {
		t.Fatalf("explicit report 404 tombstone=%v err=%v", withdrawn, err)
	}
}

func TestModelLeaderboardCatalogUsesIndependentFreshnessAndNewsStatus(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "model-leaderboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	now := time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)
	category := content.ModelLeaderboardOverall
	checkpointName := "aihot:model-leaderboard:" + category
	checkpoint := content.SourceCheckpoint{
		Name: checkpointName, ETag: `"overall-v1"`, NextFetchAt: now.Add(time.Hour),
		Status: content.SourceStatus{Name: checkpointName, State: "fresh", LastSuccessAt: now.Format(time.RFC3339Nano)},
	}
	snapshot := content.ModelLeaderboard{Category: category, Title: "AIHOT大模型排行榜", SourceURL: "https://aihot.news/leaderboard", UpdatedLabel: "10/29 14:06 更新", FetchedAt: now.Format(time.RFC3339Nano), Currency: "CNY", PriceUnit: "人民币 / 百万 Token", Entries: []content.ModelLeaderboardEntry{}, PriceNotes: []string{}}
	if err := store.SaveModelLeaderboard(ctx, snapshot, checkpoint); err != nil {
		t.Fatal(err)
	}
	catalog := content.NewCatalog(store, func() time.Time { return now })
	fresh, err := catalog.ModelLeaderboard(ctx, category)
	if err != nil || fresh.Meta.Stale || fresh.Meta.LastError != "" {
		t.Fatalf("fresh model leaderboard=%+v err=%v", fresh.Meta, err)
	}
	newsStatus, err := catalog.Status(ctx)
	if err != nil || len(newsStatus.Sources) != 0 || newsStatus.Stale {
		t.Fatalf("model checkpoint leaked into news status: meta=%+v err=%v", newsStatus, err)
	}

	now = now.Add(2 * time.Hour)
	expired, err := catalog.ModelLeaderboard(ctx, category)
	if err != nil || !expired.Meta.Stale {
		t.Fatalf("expired model snapshot freshness=%+v err=%v", expired.Meta, err)
	}
	checkpoint.Status.State = "stale"
	checkpoint.NextFetchAt = now.Add(time.Hour)
	if err := store.SaveCheckpoint(ctx, checkpointName, checkpoint); err != nil {
		t.Fatal(err)
	}
	failed, err := catalog.ModelLeaderboard(ctx, category)
	if err != nil || !failed.Meta.Stale || failed.Meta.LastError == "" {
		t.Fatalf("failed model snapshot freshness=%+v err=%v", failed.Meta, err)
	}
	newsStatus, err = catalog.Status(ctx)
	if err != nil || len(newsStatus.Sources) != 0 || newsStatus.Stale {
		t.Fatalf("stale model checkpoint leaked into news status: meta=%+v err=%v", newsStatus, err)
	}
}

func itemsResponseJSON(mode, window string, items []string, hasMore bool, cursor, notice string) string {
	nextCursor := "null"
	if cursor != "" {
		nextCursor = fmt.Sprintf("%q", cursor)
	}
	return fmt.Sprintf(`{"schemaVersion":1,"query":{"mode":%q,"category":null,"window":%q,"q":null,"by":"timeline","ordering":"timelineDesc"},"items":[%s],"page":{"count":%d,"hasMore":%t,"nextCursor":%s},"notice":%s}`,
		mode, window, strings.Join(items, ","), len(items), hasMore, nextCursor, notice)
}

func dailyIndexResponseJSON() string {
	return `{"schemaVersion":1,"count":2,"items":[{"date":"2026-10-28","generatedAt":"2026-10-28T08:00:00Z","leadTitle":"Daily 2026-10-28","leadParagraph":"lead 2026-10-28","links":{"aihot":"https://aihot.news/dailies/2026-10-28"},"attribution":{"name":"AIHOT","url":"https://aihot.news"}},{"date":"2026-10-27","generatedAt":"2026-10-27T08:00:00Z","leadTitle":"Daily 2026-10-27","leadParagraph":"lead 2026-10-27","links":{"aihot":"https://aihot.news/dailies/2026-10-27"},"attribution":{"name":"AIHOT","url":"https://aihot.news"}}],"notice":null}`
}

func dailyDetailResponseJSON(date string) string {
	return fmt.Sprintf(`{"schemaVersion":1,"report":{"date":%q,"generatedAt":"2026-10-28T08:00:00Z","windowStart":"2026-10-27T08:00:00Z","windowEnd":"2026-10-28T08:00:00Z","links":{"aihot":"https://aihot.news/dailies/%s"},"attribution":{"name":"AIHOT","url":"https://aihot.news"},"lead":{"title":"Daily %s","leadParagraph":"lead %s"},"sections":[{"label":"Models","items":[{"title":"model update","summary":"daily story body","source":{"name":"Publisher"},"links":{"aihot":"https://aihot.news/read/%s","original":"https://publisher.example/%s"},"publishedAt":"2026-10-28T07:00:00Z","attribution":{"name":"AIHOT","url":"https://aihot.news"}}]}],"flashes":[]},"notice":null}`,
		date, date, date, date, date, date)
}

func serviceRequest(handler http.Handler, target, ifNoneMatch string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, target, nil)
	if ifNoneMatch != "" {
		request.Header.Set("If-None-Match", ifNoneMatch)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}
