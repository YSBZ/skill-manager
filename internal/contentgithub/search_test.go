package contentgithub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type discoverFixtureItem struct {
	ID          int64    `json:"id"`
	FullName    string   `json:"full_name"`
	HTMLURL     string   `json:"html_url"`
	Description string   `json:"description"`
	Topics      []string `json:"topics"`
	Fork        bool     `json:"fork"`
}

func writeSearchFixture(w http.ResponseWriter, incomplete any, items []discoverFixtureItem) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"items":              items,
		"incomplete_results": incomplete,
	})
}

func TestDiscoverBoundsAndDeduplicates(t *testing.T) {
	type request struct {
		path, query, page, perPage, sortBy, order string
	}
	var mu sync.Mutex
	var requests []request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search/repositories" {
			t.Errorf("endpoint = %q, want /search/repositories", r.URL.Path)
		}
		q := r.URL.Query()
		mu.Lock()
		requests = append(requests, request{
			path: r.URL.Path, query: q.Get("q"), page: q.Get("page"),
			perPage: q.Get("per_page"), sortBy: q.Get("sort"), order: q.Get("order"),
		})
		mu.Unlock()

		page, _ := strconv.Atoi(q.Get("page"))
		switch q.Get("q") {
		case "topic:one is:public archived:false fork:false":
			switch page {
			case 1:
				writeSearchFixture(w, false, []discoverFixtureItem{
					{ID: 9, FullName: "octo/nine", HTMLURL: "https://github.com/octo/nine", Description: "nine", Topics: []string{"agent"}},
					{ID: 2, FullName: "octo/two", HTMLURL: "https://github.com/octo/two"},
				})
			case 2:
				writeSearchFixture(w, false, []discoverFixtureItem{
					{ID: 2, FullName: "octo/two-renamed", HTMLURL: "https://github.com/octo/two-renamed"},
					{ID: 1, FullName: "octo/fork", Fork: true},
				})
			default:
				t.Errorf("unexpected page %d for first query", page)
			}
		case "topic:two is:public archived:false fork:false":
			switch page {
			case 1:
				writeSearchFixture(w, false, []discoverFixtureItem{
					{ID: 5, FullName: "octo/five", HTMLURL: "https://github.com/octo/five"},
					{ID: 9, FullName: "octo/nine-duplicate", HTMLURL: "https://github.com/octo/nine-duplicate"},
				})
			case 2:
				writeSearchFixture(w, false, []discoverFixtureItem{})
			default:
				t.Errorf("unexpected page %d for second query", page)
			}
		default:
			t.Errorf("unexpected search query %q", q.Get("q"))
			writeSearchFixture(w, false, nil)
		}
	}))
	defer server.Close()

	collector := NewCollector(nil, NewClient(server.URL, nil, ""), Config{
		QueryVersion: "test", Queries: []string{"topic:one", "topic:two"},
		MaxCandidates: 100, MaxPagesPerQuery: 2, PageSize: 2,
	})
	candidates, state, err := collector.discover(context.Background(), testNow)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if state != nil {
		t.Fatalf("completed discovery returned resumable state: %+v", state)
	}
	if len(candidates) != 3 {
		t.Fatalf("candidate count = %d, want 3: %+v", len(candidates), candidates)
	}
	wantIDs := []int64{2, 5, 9}
	for i, want := range wantIDs {
		if candidates[i].ID != want {
			t.Fatalf("candidate IDs = %+v, want %v", candidates, wantIDs)
		}
	}
	if candidates[2].FullName != "octo/nine" || candidates[2].Description != "nine" ||
		len(candidates[2].Topics) != 1 || candidates[2].Topics[0] != "agent" {
		t.Fatalf("candidate DTO was not preserved: %+v", candidates[2])
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 4 {
		t.Fatalf("request count = %d, want 4: %+v", len(requests), requests)
	}
	for _, req := range requests {
		if req.path != "/search/repositories" || req.perPage != "2" || req.order != "desc" {
			t.Errorf("request has unexpected endpoint/page size/order: %+v", req)
		}
		if page, _ := strconv.Atoi(req.page); page < 1 || page > 2 {
			t.Errorf("page = %q, want 1..2", req.page)
		}
		for _, qualifier := range []string{"is:public", "archived:false", "fork:false"} {
			if !strings.Contains(req.query, qualifier) {
				t.Errorf("query %q lacks fixed qualifier %q", req.query, qualifier)
			}
		}
	}
	if requests[0].sortBy != requests[1].sortBy {
		t.Errorf("sort changed across pages of one query: %q then %q", requests[0].sortBy, requests[1].sortBy)
	}
	if requests[2].sortBy != requests[3].sortBy {
		t.Errorf("sort changed across pages of second query: %q then %q", requests[2].sortBy, requests[3].sortBy)
	}
}

func TestDiscoverCandidateCapUsesFairQueryRanking(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		start := 1
		if r.URL.Query().Get("q") == "topic:high is:public archived:false fork:false" {
			start = 1001
		}
		start += (page - 1) * 30
		items := make([]discoverFixtureItem, 0, 30)
		for i := 0; i < 30; i++ {
			id := int64(start + i)
			items = append(items, discoverFixtureItem{ID: id, FullName: fmt.Sprintf("octo/repo-%d", id)})
		}
		writeSearchFixture(w, false, items)
	}))
	defer server.Close()

	collector := NewCollector(nil, NewClient(server.URL, nil, ""), Config{
		QueryVersion: "test", Queries: []string{"topic:low", "topic:high"},
		MaxCandidates: 100, MaxPagesPerQuery: 2, PageSize: 30,
	})
	candidates, _, err := collector.discover(context.Background(), testNow)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(candidates) != 100 {
		t.Fatalf("candidate count = %d, want cap 100", len(candidates))
	}
	low, high := 0, 0
	ids := make([]int64, len(candidates))
	for i, candidate := range candidates {
		ids[i] = candidate.ID
		if candidate.ID >= 1001 {
			high++
		} else {
			low++
		}
	}
	if low != 50 || high != 50 {
		t.Fatalf("capped candidates are not round-robin fair: low=%d high=%d", low, high)
	}
	if !sort.SliceIsSorted(ids, func(i, j int) bool { return ids[i] < ids[j] }) {
		t.Fatalf("candidate IDs are not sorted for execution: %v", ids)
	}
	if ids[49] != 50 || ids[50] != 1001 {
		t.Fatalf("candidate cap appears ID-biased: boundary IDs=%d,%d", ids[49], ids[50])
	}
}

func TestDiscoverResumesAtFailedPage(t *testing.T) {
	var mu sync.Mutex
	pageHits := map[int]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		mu.Lock()
		pageHits[page]++
		hit := pageHits[page]
		mu.Unlock()
		if page == 1 {
			writeSearchFixture(w, false, []discoverFixtureItem{
				{ID: 10, FullName: "octo/ten"}, {ID: 20, FullName: "octo/twenty"},
			})
			return
		}
		if hit == 1 {
			http.Error(w, "temporary fixture failure", http.StatusServiceUnavailable)
			return
		}
		writeSearchFixture(w, false, []discoverFixtureItem{
			{ID: 30, FullName: "octo/thirty"}, {ID: 40, FullName: "octo/forty"},
		})
	}))
	defer server.Close()

	collector := NewCollector(nil, NewClient(server.URL, nil, ""), Config{
		QueryVersion: "test", Queries: []string{"topic:one"},
		MaxCandidates: 100, MaxPagesPerQuery: 2, PageSize: 2,
	})
	oldPool := []repositoryCandidate{{ID: 99, FullName: "octo/old-pool"}}
	collector.candidates = append([]repositoryCandidate(nil), oldPool...)
	_, state, err := collector.discover(context.Background(), testNow)
	if err == nil {
		t.Fatal("discover unexpectedly succeeded after page 2 failure")
	}
	if state == nil || state.query != 0 || state.page != 1 || len(state.rowsByQuery) != 1 || len(state.rowsByQuery[0]) != 2 {
		t.Fatalf("failed discovery did not preserve page 1 state: %+v", state)
	}
	if len(collector.candidates) != 1 || collector.candidates[0].ID != 99 {
		t.Fatalf("failed search replaced the old candidate pool: %+v", collector.candidates)
	}

	collector.search = state
	candidates, nextState, err := collector.discover(context.Background(), testNow.Add(time.Minute))
	if err != nil {
		t.Fatalf("resume discover: %v", err)
	}
	if nextState != nil || len(candidates) != 4 {
		t.Fatalf("resumed discovery candidates=%+v state=%+v", candidates, nextState)
	}
	mu.Lock()
	defer mu.Unlock()
	if pageHits[1] != 1 || pageHits[2] != 2 {
		t.Fatalf("resume repeated successful page or skipped failed page: hits=%v", pageHits)
	}
}

func TestDiscoverRejectsIncompleteSnapshotWithoutAdvancing(t *testing.T) {
	var mu sync.Mutex
	var pages, sorts []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		pages = append(pages, r.URL.Query().Get("page"))
		sorts = append(sorts, r.URL.Query().Get("sort"))
		attempt := len(pages)
		mu.Unlock()
		if attempt == 1 {
			writeSearchFixture(w, true, []discoverFixtureItem{{ID: 1, FullName: "octo/partial"}})
			return
		}
		writeSearchFixture(w, false, []discoverFixtureItem{{ID: 2, FullName: "octo/complete"}})
	}))
	defer server.Close()

	collector := NewCollector(nil, NewClient(server.URL, nil, ""), Config{
		QueryVersion: "test", Queries: []string{"topic:one"},
		MaxCandidates: 100, MaxPagesPerQuery: 2, PageSize: 2,
	})
	_, state, err := collector.discover(context.Background(), testNow)
	if err == nil {
		t.Fatal("incomplete GitHub search response was accepted")
	}
	if state == nil || state.query != 0 || state.page != 0 || len(state.rowsByQuery[0]) != 0 {
		t.Fatalf("incomplete response advanced or polluted search state: %+v", state)
	}
	collector.search = state
	candidates, _, err := collector.discover(context.Background(), testNow.Add(time.Minute))
	if err != nil {
		t.Fatalf("retry complete snapshot: %v", err)
	}
	if len(candidates) != 1 || candidates[0].ID != 2 {
		t.Fatalf("partial response leaked into candidates: %+v", candidates)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(pages) != 2 || pages[0] != "1" || pages[1] != "1" || sorts[0] != sorts[1] {
		t.Fatalf("retry did not repeat the same page/sort: pages=%v sorts=%v", pages, sorts)
	}
}

func TestDiscoverDuplicateOnlyRankDoesNotStopFairMerge(t *testing.T) {
	groups := [][]repositoryCandidate{
		{{ID: 1, FullName: "one"}, {ID: 2, FullName: "two"}, {ID: 3, FullName: "three"}},
		{{ID: 2, FullName: "two"}, {ID: 1, FullName: "one"}, {ID: 4, FullName: "four"}},
	}
	got := mergeCandidateGroups(groups, 100)
	if len(got) != 4 || got[0].ID != 1 || got[1].ID != 2 || got[2].ID != 3 || got[3].ID != 4 {
		t.Fatalf("fair merge stopped at duplicate-only rank: %+v", got)
	}
}

func TestDiscoverRejectsMalformedSearchSchema(t *testing.T) {
	for _, body := range []string{
		`{"incomplete_results":false}`,
		`{"items":null,"incomplete_results":false}`,
		`{"items":[],"incomplete_results":null}`,
		`{"items":[],"incomplete_results":"false"}`,
	} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = fmt.Fprint(w, body)
			}))
			defer server.Close()
			collector := NewCollector(nil, NewClient(server.URL, nil, ""), Config{
				QueryVersion: "test", Queries: []string{"topic:one"},
				MaxCandidates: 100, MaxPagesPerQuery: 2, PageSize: 2,
			})
			_, state, err := collector.discover(context.Background(), testNow)
			if err == nil {
				t.Fatalf("malformed search schema accepted: %s", body)
			}
			if state == nil || state.query != 0 || state.page != 0 || len(state.rowsByQuery[0]) != 0 {
				t.Fatalf("malformed response advanced search state: %+v", state)
			}
		})
	}
}
