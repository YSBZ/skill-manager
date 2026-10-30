package contentgithub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"skillmanage/internal/content"
	"skillmanage/internal/content/sqlite"
	"skillmanage/internal/contentservice"
)

func TestCompletedQueryBootstrapServesVerifiedDataWhileSearchResumes(t *testing.T) {
	ctx := context.Background()
	recent, legacy := defaultCollectorFixture("octo/recent", 90), defaultCollectorFixture("octo/legacy", 1)
	partial := defaultCollectorFixture("octo/partial", 20)
	recent.metadata.CreatedAt = testNow.Add(-24 * time.Hour).Format(time.RFC3339)
	recent.files["SKILL.md"] = validSkillFile("recent")
	partial.files["SKILL.md"] = validSkillFile("partial")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search/repositories" {
			items := []discoverFixtureItem{}
			incomplete := false
			if strings.Contains(r.URL.Query().Get("q"), "topic:complete") && r.URL.Query().Get("page") == "1" {
				items = []discoverFixtureItem{{ID: 90, FullName: "octo/recent"}, {ID: 1, FullName: "octo/legacy"}}
			} else if strings.Contains(r.URL.Query().Get("q"), "topic:partial") {
				items = []discoverFixtureItem{{ID: 20, FullName: "octo/partial"}, {ID: 21, FullName: "octo/other"}}
				incomplete = r.URL.Query().Get("page") == "2"
			}
			writeSearchFixture(w, incomplete, items)
			return
		}
		collectorFixtureHandler{recent, legacy, partial}.ServeHTTP(w, r)
	}))
	defer upstream.Close()
	path := filepath.Join(t.TempDir(), "catalog.db")
	store, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	collector := NewCollector(store, NewClient(upstream.URL, nil, ""), Config{
		QueryVersion: "bootstrap", Queries: []string{"topic:complete", "topic:partial"}, PageSize: 2, CoreRequestsPerHour: 4,
	})
	if err := collector.Refresh(ctx, testNow); !errors.Is(err, errIncompleteSearchResults) || !errors.Is(err, errRequestBudget) {
		t.Fatalf("Refresh must retain search failure and bounded core pause, got %v", err)
	}
	if recent.metadataRequests != 1 || legacy.metadataRequests != 0 || partial.metadataRequests != 0 {
		t.Fatalf("first complete query top hit must bootstrap first; requests recent=%d legacy=%d partial=%d", recent.metadataRequests, legacy.metadataRequests, partial.metadataRequests)
	}
	if !collector.lastDiscovery.IsZero() || collector.discoveryCycle != 0 || collector.search == nil || collector.search.query != 1 || collector.search.page != 1 {
		t.Fatalf("partial discovery advanced completed-cycle state: %+v", collector)
	}
	if len(collector.candidates) != 2 || collector.candidates[collector.cursor].ID != 1 {
		t.Fatalf("bootstrap queue/cursor=%+v cursor=%d", collector.candidates, collector.cursor)
	}
	checkpoints, err := store.Checkpoints(ctx)
	if err != nil || len(checkpoints) != 1 {
		t.Fatalf("checkpoints=%+v error=%v", checkpoints, err)
	}
	checkpoint := checkpoints[0]
	if checkpoint.Status.State != "rate_limited" || checkpoint.Status.LastSuccessAt != testNow.Format(time.RFC3339) || checkpoint.FailureCount != 1 || !checkpoint.NextFetchAt.After(testNow) {
		t.Fatalf("verified data must retain truthful failed-source status: %+v", checkpoint)
	}
	catalog := content.NewCatalog(store, func() time.Time { return testNow })
	for _, view := range []string{"scenarios", "new", "weekly"} {
		response := httptest.NewRecorder()
		contentservice.NewHandler(catalog).ServeHTTP(response, httptest.NewRequest("GET", "/v1/feed?kind=project&view="+view, nil))
		var page content.Page
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &page) != nil || !page.Meta.Stale {
			t.Fatalf("%s HTTP=%d body=%s", view, response.Code, response.Body.String())
		}
		want := 1
		if view != "scenarios" {
			want = 0
		}
		if len(page.Items) != want || page.Meta.Coverage.Candidates != 2 || page.Meta.Coverage.Updated != 1 || page.Meta.Coverage.CompleteGrowth != 0 {
			t.Fatalf("%s page=%+v", view, page)
		}
		if want == 1 && (page.Items[0].ID != "project_90" || len(page.Items[0].Project.Skills) != 1 || !page.Items[0].Project.Skills[0].Verified) {
			t.Fatalf("published unverified or wrong project: %+v", page.Items)
		}
	}
	assertActiveProjects(t, store, testNow, []string{"project_90"})
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	assertActiveProjects(t, store, testNow, []string{"project_90"})
	// Reopening restores the verified active pool even though the in-memory
	// discovery continuation is gone. The failed search still cannot retire it.
	collector = NewCollector(store, NewClient(upstream.URL, nil, ""), Config{
		QueryVersion: "bootstrap", Queries: []string{"topic:complete", "topic:partial"}, PageSize: 2,
	})
	now := testNow.Add(time.Hour)
	if err := collector.Refresh(ctx, now); !errors.Is(err, errIncompleteSearchResults) || errors.Is(err, errRequestBudget) {
		t.Fatalf("reopened collector must report only incomplete discovery, got %v", err)
	}
	checkpoints, err = store.Checkpoints(ctx)
	if err != nil || len(checkpoints) != 1 || checkpoints[0].Status.State != "stale" || checkpoints[0].Status.LastSuccessAt != testNow.Format(time.RFC3339) {
		t.Fatalf("failed resumed search changed success time or stale state: %+v error=%v", checkpoints, err)
	}
	assertActiveProjects(t, store, now, []string{"project_1", "project_90"})
	page, err := content.NewCatalog(store, func() time.Time { return now }).Feed(ctx, content.Query{Kind: "project", View: "new"})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != "project_90" || !page.Meta.Stale {
		t.Fatalf("verified new project with an observed growth window not served: page=%+v error=%v", page, err)
	}
}

func TestCompletedQueryBootstrapPreservesPausedIDAndPoolUntilCompleteReconcile(t *testing.T) {
	ctx := context.Background()
	old, paused := defaultCollectorFixture("octo/old", 42), defaultCollectorFixture("octo/paused", 99)
	a, b := defaultCollectorFixture("octo/a", 1), defaultCollectorFixture("octo/b", 2)
	paused.metadataStatus = http.StatusTooManyRequests
	paused.metadataHeaders.Set("Retry-After", "120")
	incomplete := true
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search/repositories" {
			items := []discoverFixtureItem{}
			if strings.Contains(r.URL.Query().Get("q"), "topic:complete") {
				items = []discoverFixtureItem{{ID: 1, FullName: "octo/a"}, {ID: 2, FullName: "octo/b"}}
			}
			writeSearchFixture(w, incomplete && strings.Contains(r.URL.Query().Get("q"), "topic:partial"), items)
			return
		}
		collectorFixtureHandler{old, paused, a, b}.ServeHTTP(w, r)
	}))
	defer upstream.Close()
	path := filepath.Join(t.TempDir(), "catalog.db")
	store, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	for _, entry := range []content.Entry{cachedProject(42, "octo/old"), cachedProject(99, "octo/paused")} {
		if err := store.Upsert(ctx, entry); err != nil {
			t.Fatal(err)
		}
	}
	collector := NewCollector(store, NewClient(upstream.URL, nil, ""), Config{
		QueryVersion: "bootstrap", Queries: []string{"topic:complete", "topic:partial"}, MaxCandidates: 3,
	})
	collector.candidates = []repositoryCandidate{{ID: 42, FullName: "octo/old"}, {ID: 99, FullName: "octo/paused"}}
	collector.cursor = 1
	if err := collector.Refresh(ctx, testNow); err == nil {
		t.Fatal("expected incomplete search and rate pause")
	}
	if len(collector.candidates) != 3 || collector.candidates[collector.cursor].ID != 99 || paused.metadataRequests != 1 || old.metadataRequests != 0 || a.metadataRequests != 0 {
		t.Fatalf("append starved paused ID or exceeded pool bound: candidates=%+v cursor=%d metadata=%d/%d/%d", collector.candidates, collector.cursor, paused.metadataRequests, old.metadataRequests, a.metadataRequests)
	}
	assertActiveProjects(t, store, testNow, []string{"project_42", "project_99"})
	checkpoints, err := store.Checkpoints(ctx)
	if err != nil || len(checkpoints) != 1 || checkpoints[0].Status.LastSuccessAt != "" {
		t.Fatalf("no verified update must not create a data-success time: %+v error=%v", checkpoints, err)
	}
	incomplete = false
	if err := collector.Refresh(ctx, testNow.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if collector.search != nil || collector.lastDiscovery.IsZero() || collector.discoveryCycle != 1 {
		t.Fatalf("completed resume did not reconcile pool: %+v", collector)
	}
	assertActiveProjects(t, store, testNow.Add(3*time.Minute), []string{"project_1", "project_2"})
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	assertActiveProjects(t, store, testNow.Add(3*time.Minute), []string{"project_1", "project_2"})
}

func TestProjectMembershipPoolChurnPersistsAndCanReenter(t *testing.T) {
	ctx := context.Background()
	a, b := defaultCollectorFixture("octo/a", 41), defaultCollectorFixture("octo/b", 42)
	a.files["SKILL.md"] = validSkillFile("a")
	b.files["SKILL.md"] = validSkillFile("b")
	a.history, b.history = completeHistoryAt(testNow), completeHistoryAt(testNow)
	pool := []*collectorHTTPFixture{a}
	incomplete := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search/repositories" {
			items := []searchItem{}
			for _, row := range pool {
				items = append(items, searchItem{ID: row.metadata.ID, FullName: row.metadata.FullName})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "incomplete_results": incomplete})
			return
		}
		collectorFixtureHandler{a, b}.ServeHTTP(w, r)
	}))
	defer upstream.Close()
	path := filepath.Join(t.TempDir(), "catalog.db")
	store, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	collector := NewCollector(store, NewClient(upstream.URL, nil, ""), Config{QueryVersion: "membership", Queries: []string{"topic:fixture"}, DiscoveryInterval: time.Minute})
	for cycle, active := range []string{"project_41", "project_42"} {
		if cycle == 1 {
			pool = []*collectorHTTPFixture{b}
		}
		now := testNow.Add(time.Duration(cycle) * 2 * time.Minute)
		if err := collector.Refresh(ctx, now); err != nil {
			t.Fatal(err)
		}
		assertActiveProjects(t, store, now, []string{active})
	}
	if cached, err := store.Get(ctx, "project_41"); err != nil || len(cached.Project.Skills) != 1 {
		t.Fatalf("pool exit must retain internal cache, got %+v %v", cached, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	assertActiveProjects(t, store, testNow.Add(2*time.Minute), []string{"project_42"})
	// A partial discovery cannot evict the previous usable pool after restart.
	incomplete = true
	collector = NewCollector(store, NewClient(upstream.URL, nil, ""), Config{QueryVersion: "membership", Queries: []string{"topic:fixture"}})
	if err := collector.Refresh(ctx, testNow.Add(4*time.Minute)); err == nil {
		t.Fatal("incomplete search should fail")
	}
	assertActiveProjects(t, store, testNow.Add(4*time.Minute), []string{"project_42"})
	incomplete = false
	pool = []*collectorHTTPFixture{a}
	if err := collector.Refresh(ctx, testNow.Add(6*time.Minute)); err != nil {
		t.Fatal(err)
	}
	assertActiveProjects(t, store, testNow.Add(6*time.Minute), []string{"project_41"})
}

func TestProjectMembershipCompleteUnrelatedRetiresButTemporaryEvidencePreserves(t *testing.T) {
	for _, failure := range []string{"unrelated", "truncated", "rate", "temporary"} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			repo := defaultCollectorFixture("octo/was-skill", 42)
			repo.files["SKILL.md"] = validSkillFile("demo")
			repo.history = completeHistoryAt(testNow)
			upstream := newCollectorServer(repo)
			defer upstream.Close()
			path := filepath.Join(t.TempDir(), "catalog.db")
			store, err := sqlite.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = store.Close() }()
			collector := NewCollector(store, NewClient(upstream.URL, nil, ""), Config{QueryVersion: "membership", Queries: []string{"topic:fixture"}})
			collector.candidates = []repositoryCandidate{{ID: 42, FullName: repo.metadata.FullName}}
			collector.lastDiscovery = testNow
			if err := collector.Refresh(ctx, testNow); err != nil {
				t.Fatal(err)
			}
			assertActiveProjects(t, store, testNow, []string{"project_42"})
			repo.files = map[string]string{}
			repo.metadata.Description, repo.metadata.Topics = "Unrelated repository", []string{"art"}
			switch failure {
			case "truncated":
				repo.treeTruncated = true
			case "rate":
				repo.metadataStatus = http.StatusTooManyRequests
			case "temporary":
				repo.treeStatus = http.StatusServiceUnavailable
			}
			err = collector.Refresh(ctx, testNow.Add(time.Minute))
			if (failure == "rate" || failure == "temporary") && err == nil {
				t.Fatal("expected temporary error")
			}
			want := []string{"project_42"}
			if failure == "unrelated" {
				want = nil
			}
			assertActiveProjects(t, store, testNow.Add(time.Minute), want)
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = sqlite.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			assertActiveProjects(t, store, testNow.Add(time.Minute), want)
		})
	}
}

func assertActiveProjects(t *testing.T, store *sqlite.Store, now time.Time, want []string) {
	t.Helper()
	ctx := context.Background()
	catalog := content.NewCatalog(store, func() time.Time { return now })
	page, err := catalog.Feed(ctx, content.Query{Kind: "project", View: "scenarios"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != len(want) {
		t.Fatalf("active feed=%+v, want IDs %v", page.Items, want)
	}
	expected := map[string]bool{}
	for _, id := range want {
		expected[id] = true
	}
	for _, item := range page.Items {
		if !expected[item.ID] {
			t.Fatalf("unexpected active project %s", item.ID)
		}
	}
	entries, err := store.All(ctx, "project")
	if err != nil {
		t.Fatal(err)
	}
	complete := 0
	for _, entry := range entries {
		_, err := catalog.Entry(ctx, entry.ID)
		response := httptest.NewRecorder()
		contentservice.NewHandler(catalog).ServeHTTP(response, httptest.NewRequest("GET", "/v1/entries/"+entry.ID, nil))
		wantStatus := http.StatusNotFound
		if expected[entry.ID] {
			wantStatus = http.StatusOK
		}
		if response.Code != wantStatus {
			t.Fatalf("authoritative HTTP detail %s: %d %s", entry.ID, response.Code, response.Body.String())
		}

		if expected[entry.ID] {
			if err != nil {
				t.Fatalf("active authoritative detail %s: %v", entry.ID, err)
			}
			if entry.Project.Growth != nil && entry.Project.Growth.Metric == "stars_added" && entry.Project.Growth.Complete {
				complete++
			}
		} else if !errors.Is(err, content.ErrNotFound) || errors.Is(err, content.ErrWithdrawn) {
			t.Fatalf("inactive authoritative detail %s error=%v, want recoverable 404", entry.ID, err)
		}
	}
	coverage, err := store.LoadCoverage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if coverage.CompleteGrowth != complete || coverage.CompleteGrowth > coverage.Candidates {
		t.Fatalf("coverage=%+v active complete=%d", coverage, complete)
	}
}
