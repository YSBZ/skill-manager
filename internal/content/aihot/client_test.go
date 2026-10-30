package aihot

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchItemsRejectsPartialCursorChain(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/v1/items" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if calls == 1 {
			if r.URL.Query().Get("by") != "timeline" || r.URL.Query().Get("limit") != "100" {
				t.Fatalf("query = %s", r.URL.RawQuery)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"schemaVersion":1,"query":{"mode":"selected","category":null,"window":"7d","q":null,"by":"timeline","ordering":"timelineDesc"},"items":[{"id":"1","title":"T","originalTitle":null,"summary":"S","source":{"name":"P"},"links":{"aihot":"https://aihot.news/1","original":"https://publisher.example/1"},"publishedAt":null,"discoveredAt":"2026-10-28T10:00:00Z","category":null,"score":null,"selected":true,"reason":null}],"page":{"count":1,"hasMore":true,"nextCursor":"page-2"},"notice":null}`)
			return
		}
		if r.URL.Query().Get("cursor") != "page-2" {
			t.Fatalf("cursor = %q", r.URL.Query().Get("cursor"))
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	client := NewClient(srv.URL, srv.Client())
	if _, err := client.FetchItems(context.Background(), "selected", "7d", ""); err == nil {
		t.Fatal("partial result accepted")
	}
	if calls != 2 {
		t.Fatalf("requests = %d, want 2", calls)
	}
}

func TestFetchItemsFollowsEveryCursorAndReturnsCompleteEntries(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("by") != "timeline" || r.URL.Query().Get("limit") != "100" {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		if calls == 1 {
			if r.URL.Query().Get("cursor") != "" {
				t.Errorf("first cursor = %q", r.URL.Query().Get("cursor"))
			}
			_, _ = io.WriteString(w, itemsPageJSON("all", "7d", `[{"id":"first","title":"First","originalTitle":null,"summary":"summary","source":{"name":"Publisher"},"links":{"aihot":"https://aihot.news/read/first","original":"https://publisher.example/first"},"publishedAt":null,"discoveredAt":"2026-10-28T10:00:00Z","category":null,"score":null,"selected":false,"reason":null}]`, 1, true, "cursor-2"))
			return
		}
		if r.URL.Query().Get("cursor") != "cursor-2" {
			t.Errorf("second cursor = %q", r.URL.Query().Get("cursor"))
		}
		_, _ = io.WriteString(w, itemsPageJSON("all", "7d", `[{"id":"second","title":"Second","originalTitle":null,"summary":null,"source":{"name":"Publisher"},"links":{"aihot":"https://aihot.news/read/second","original":"https://publisher.example/second"},"publishedAt":"2026-10-28T09:00:00Z","discoveredAt":"2026-10-28T09:01:00Z","category":"ai-models","score":null,"selected":false,"reason":null}]`, 1, false, ""))
	}))
	defer srv.Close()
	snapshot, err := NewClient(srv.URL, srv.Client()).FetchItems(context.Background(), "all", "7d", "")
	if err != nil || len(snapshot.Entries) != 2 || calls != 2 {
		t.Fatalf("FetchItems() = %#v, %v; calls=%d", snapshot, err, calls)
	}
	if snapshot.Entries[0].ID != "news_first" || snapshot.Entries[1].ID != "news_second" || snapshot.Entries[1].PublishedAt != "2026-10-28T09:00:00Z" {
		t.Fatalf("entries = %#v", snapshot.Entries)
	}
}

func TestFetchItemsRejectsRepeatedCursor(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"schemaVersion":1,"query":{"mode":"all","category":null,"window":"24h","q":null,"by":"timeline","ordering":"timelineDesc"},"items":[],"page":{"count":0,"hasMore":true,"nextCursor":"same"},"notice":null}`)
	}))
	defer srv.Close()
	client := NewClient(srv.URL, srv.Client())
	if _, err := client.FetchItems(context.Background(), "all", "24h", ""); err == nil {
		t.Fatal("repeated cursor accepted")
	}
	if calls != 2 {
		t.Fatalf("requests = %d, want 2 before loop guard", calls)
	}
}

func TestFetchItemsUsesETagOnlyForFirstPageAndAccepts304(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Accept") != "application/json" || !strings.Contains(r.Header.Get("User-Agent"), "SkillManager-contentd/") {
			t.Errorf("headers = %#v", r.Header)
		}
		if r.Header.Get("If-None-Match") != `"cached"` {
			t.Errorf("If-None-Match = %q", r.Header.Get("If-None-Match"))
		}
		if r.URL.Query().Get("by") != "timeline" || r.URL.Query().Get("limit") != "100" {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		w.Header().Set("ETag", `"cached"`)
		w.Header().Set("Cache-Control", "max-age=300")
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()
	client := NewClient(srv.URL, srv.Client())
	snapshot, err := client.FetchItems(context.Background(), "selected", "7d", `"cached"`)
	if err != nil || !snapshot.NotModified || snapshot.ETag != `"cached"` || calls != 1 {
		t.Fatalf("FetchItems() = %#v, %v, calls %d", snapshot, err, calls)
	}
}

func TestClientBoundsResponseBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"schemaVersion":1,"query":{"mode":"all","category":null,"window":"7d","q":null,"by":"timeline","ordering":"timelineDesc"},"items":[]`)
		_, _ = io.WriteString(w, strings.Repeat(" ", maxResponseBody+1))
	}))
	defer srv.Close()
	_, err := NewClient(srv.URL, srv.Client()).FetchItems(context.Background(), "all", "7d", "")
	if !errors.Is(err, errResponseTooLarge) {
		t.Fatalf("oversized response error = %v", err)
	}
}

func TestFetchItemsRejectsMissingRequiredPageOrItemsArray(t *testing.T) {
	for _, body := range []string{
		`{"schemaVersion":1,"query":{"mode":"all","category":null,"window":"7d","q":null,"by":"timeline","ordering":"timelineDesc"},"page":{"count":0,"hasMore":false,"nextCursor":null},"notice":null}`,
		`{"schemaVersion":1,"query":{"mode":"all","category":null,"window":"7d","q":null,"by":"timeline","ordering":"timelineDesc"},"items":null,"page":{"count":0,"hasMore":false,"nextCursor":null},"notice":null}`,
	} {
		t.Run(body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) }))
			defer srv.Close()
			if _, err := NewClient(srv.URL, srv.Client()).FetchItems(context.Background(), "all", "7d", ""); !errors.Is(err, errIncompletePage) {
				t.Fatalf("invalid page error = %v", err)
			}
		})
	}
}
