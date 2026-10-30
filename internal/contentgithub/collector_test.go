package contentgithub

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"skillmanage/internal/content"
	"skillmanage/internal/contentservice"
)

var _ contentservice.Refresher = (*Collector)(nil)

func TestRefreshDeletesOnlyArchivedOrMetadataNotFound(t *testing.T) {
	for _, tc := range []struct {
		name           string
		metadataStatus int
		archived       bool
		wantTrees      int
	}{
		{name: "archived", archived: true},
		{name: "metadata 404", metadataStatus: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := defaultCollectorFixture("octo/repo", 42)
			repo.metadataStatus, repo.metadata.Archived = tc.metadataStatus, tc.archived
			server := newCollectorServer(repo)
			defer server.Close()
			old := cachedProject(42, "octo/repo")
			store := newCollectorStore(old)
			collector := collectorForFixture(store, server, []repositoryCandidate{{ID: 42, FullName: "octo/repo"}})
			if err := collector.Refresh(context.Background(), testNow); err != nil {
				t.Fatalf("Refresh: %v", err)
			}
			if _, ok := store.entries[old.ID]; ok || !reflect.DeepEqual(store.deleted, []string{old.ID}) {
				t.Fatalf("deleted=%v cached=%v; expected only %q deleted", store.deleted, store.entries[old.ID], old.ID)
			}
			if repo.treeRequests != tc.wantTrees {
				t.Fatalf("tree requests=%d, want %d", repo.treeRequests, tc.wantTrees)
			}
		})
	}
}

func TestRefreshKeepsCachedProjectOnMetadataTemporaryFailure(t *testing.T) {
	for _, tc := range []struct {
		name        string
		status      int
		wantState   string
		wantRetryAt string
	}{
		{name: "rate limited", status: http.StatusTooManyRequests, wantState: "rate_limited", wantRetryAt: testNow.Add(2 * time.Minute).Format(time.RFC3339)},
		{name: "server error", status: http.StatusServiceUnavailable, wantState: "stale"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := defaultCollectorFixture("octo/repo", 42)
			repo.metadataStatus = tc.status
			if tc.status == http.StatusTooManyRequests {
				repo.metadataHeaders.Set("Retry-After", "120")
			}
			server := newCollectorServer(repo)
			defer server.Close()
			old := cachedProject(42, "octo/repo")
			store := newCollectorStore(old)
			store.checkpoints["github"] = content.SourceCheckpoint{
				Name: "github", FailureCount: 3,
				Status: content.SourceStatus{Name: "github", State: "fresh", LastSuccessAt: "2026-01-01T00:00:00Z"},
			}
			collector := collectorForFixture(store, server, []repositoryCandidate{{ID: 42, FullName: "octo/repo"}})
			if err := collector.Refresh(context.Background(), testNow); err == nil {
				t.Fatal("Refresh unexpectedly succeeded")
			}
			if !reflect.DeepEqual(store.entries[old.ID], old) || len(store.deleted) != 0 {
				t.Fatalf("temporary failure altered cache: entry=%#v deleted=%v", store.entries[old.ID], store.deleted)
			}
			checkpoint := store.checkpoints["github"]
			if checkpoint.Status.State != tc.wantState || checkpoint.Status.LastSuccessAt != "2026-01-01T00:00:00Z" ||
				checkpoint.FailureCount != 4 || checkpoint.Status.RetryAt != tc.wantRetryAt {
				t.Fatalf("failure checkpoint=%+v", checkpoint)
			}
			if store.coverage.Updated != 0 {
				t.Fatalf("coverage Updated=%d, want 0", store.coverage.Updated)
			}
		})
	}
}

func TestRefreshAggregateErrorsKeepRateLimitFromCandidateAfterDiscoveryFailure(t *testing.T) {
	repo := defaultCollectorFixture("octo/repo", 42)
	repo.searchStatus = http.StatusServiceUnavailable
	repo.metadataStatus = http.StatusTooManyRequests
	repo.metadataHeaders.Set("Retry-After", "45")
	server := newCollectorServer(repo)
	defer server.Close()
	store := newCollectorStore(cachedProject(42, "octo/repo"))
	collector := collectorForFixture(store, server, []repositoryCandidate{{ID: 42, FullName: "octo/repo"}})
	collector.lastDiscovery = time.Time{}
	if err := collector.Refresh(context.Background(), testNow); err == nil {
		t.Fatal("Refresh unexpectedly succeeded")
	}
	checkpoint := store.checkpoints["github"]
	wantRetryAt := testNow.Add(45 * time.Second).Format(time.RFC3339)
	if checkpoint.Status.State != "rate_limited" || checkpoint.Status.RetryAt != wantRetryAt {
		t.Fatalf("aggregate checkpoint=%+v, want rate_limited RetryAt=%q", checkpoint, wantRetryAt)
	}
}

func TestRefreshAggregateRateLimitsUseLatestRetryDeadline(t *testing.T) {
	repo := defaultCollectorFixture("octo/repo", 42)
	repo.searchStatus = http.StatusTooManyRequests
	repo.searchHeaders.Set("Retry-After", "300")
	repo.metadataStatus = http.StatusTooManyRequests
	repo.metadataHeaders.Set("Retry-After", "900")
	server := newCollectorServer(repo)
	defer server.Close()
	store := newCollectorStore(cachedProject(42, "octo/repo"))
	collector := collectorForFixture(store, server, []repositoryCandidate{{ID: 42, FullName: "octo/repo"}})
	collector.lastDiscovery = time.Time{}
	if err := collector.Refresh(context.Background(), testNow); err == nil {
		t.Fatal("Refresh unexpectedly succeeded")
	}
	checkpoint := store.checkpoints["github"]
	wantRetryAt := testNow.Add(15 * time.Minute).Format(time.RFC3339)
	if checkpoint.Status.State != "rate_limited" || checkpoint.Status.RetryAt != wantRetryAt || !checkpoint.NextFetchAt.Equal(testNow.Add(15*time.Minute)) {
		t.Fatalf("aggregate checkpoint=%+v, want latest RetryAt=%q", checkpoint, wantRetryAt)
	}
}

func TestRefreshPreservesCachedProjectWhenSkillContentsAreNotFound(t *testing.T) {
	repo := defaultCollectorFixture("octo/repo", 42)
	repo.files["skills/demo/SKILL.md"] = validSkillFile("demo")
	repo.contentStatus["skills/demo/SKILL.md"] = http.StatusNotFound
	server := newCollectorServer(repo)
	defer server.Close()
	old := cachedProject(42, "octo/repo")
	store := newCollectorStore(old)
	collector := collectorForFixture(store, server, []repositoryCandidate{{ID: 42, FullName: "octo/repo"}})
	if err := collector.Refresh(context.Background(), testNow); err == nil {
		t.Fatal("contents 404 unexpectedly succeeded")
	}
	if !reflect.DeepEqual(store.entries[old.ID], old) || len(store.deleted) != 0 {
		t.Fatalf("contents 404 altered cache: entry=%#v deleted=%v", store.entries[old.ID], store.deleted)
	}
}

func TestRefreshPreservesOldVerifiedSkillsWhenTreeExceedsLimit(t *testing.T) {
	repo := defaultCollectorFixture("octo/repo", 42)
	for i := 0; i < maxSkillFilesPerRepository+1; i++ {
		name := fmt.Sprintf("skill-%02d", i)
		repo.files[fmt.Sprintf("skills/%02d/SKILL.md", i)] = validSkillFile(name)
	}
	server := newCollectorServer(repo)
	defer server.Close()
	old := cachedProject(42, "octo/repo")
	store := newCollectorStore(old)
	collector := collectorForFixture(store, server, []repositoryCandidate{{ID: 42, FullName: "octo/repo"}})
	if err := collector.Refresh(context.Background(), testNow); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if !reflect.DeepEqual(store.entries[old.ID], old) {
		t.Fatalf("over-limit tree changed cached verified skills: got %#v, want %#v", store.entries[old.ID], old)
	}
}

func TestRefreshDoesNotCreateUnclassifiedRepository(t *testing.T) {
	repo := defaultCollectorFixture("octo/unknown", 42)
	repo.metadata.Description = "An unrelated research repository"
	repo.metadata.Topics = []string{"artificial-intelligence"}
	server := newCollectorServer(repo)
	defer server.Close()
	store := newCollectorStore()
	collector := collectorForFixture(store, server, []repositoryCandidate{{ID: 42, FullName: "octo/unknown"}})
	if err := collector.Refresh(context.Background(), testNow); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if _, ok := store.entries[projectID(42)]; ok || store.coverage.Updated != 0 {
		t.Fatalf("unclassified repository was created or counted: entry=%+v coverage=%+v", store.entries[projectID(42)], store.coverage)
	}
}

func TestRefreshPreservesOldEntryForTruncatedTreeAndCountsSuccessfulUpserts(t *testing.T) {
	truncated := defaultCollectorFixture("octo/truncated", 42)
	truncated.treeTruncated = true
	truncated.files["skills/partial/SKILL.md"] = validSkillFile("partial")
	newRepo := defaultCollectorFixture("octo/new", 99)
	server := newCollectorServer(truncated, newRepo)
	defer server.Close()
	old := cachedProject(42, "octo/truncated")
	store := newCollectorStore(old)
	collector := collectorForFixture(store, server, []repositoryCandidate{
		{ID: 42, FullName: "octo/truncated"}, {ID: 99, FullName: "octo/new"},
	})
	if err := collector.Refresh(context.Background(), testNow); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if !reflect.DeepEqual(store.entries[old.ID], old) {
		t.Fatalf("truncated tree changed cached project: got %#v, want %#v", store.entries[old.ID], old)
	}
	entry, ok := store.entries[projectID(99)]
	if !ok || entry.Project == nil || entry.Project.Type != "tool" || entry.Project.Growth != nil {
		t.Fatalf("complete repository entry=%+v; expected tool project with no unsupported growth", entry)
	}
	if entry.DiscoveredAt != testNow.Format(time.RFC3339) {
		t.Fatalf("new project DiscoveredAt=%q, want %q", entry.DiscoveredAt, testNow.Format(time.RFC3339))
	}
	if store.coverage.Updated != 1 || store.coverage.Candidates != 2 || store.coverage.CompleteGrowth != 0 {
		t.Fatalf("coverage=%+v, want candidates=2 updated=1 completeGrowth=0", store.coverage)
	}
}

func TestNormalizeProjectUsesRepositoryIdentityAndOneGrowthValue(t *testing.T) {
	created := "2024-02-03T04:05:06Z"
	metadata := repositoryMetadata{
		ID: 42, FullName: "octo/repo", HTMLURL: "https://github.com/octo/repo",
		Stars: 7, CreatedAt: created,
	}
	growth := &content.Growth{Metric: "stars_net", Value: 6, WindowStart: "2026-01-01T00:00:00Z", WindowEnd: "2026-01-02T00:00:00Z", ObservedDays: 1}
	entry := normalizeProject(metadata, []content.SkillRef{
		{RepositoryID: 42, Repo: metadata.FullName, Path: "a/SKILL.md", Name: "a", Verified: true},
		{RepositoryID: 42, Repo: metadata.FullName, Path: "b/SKILL.md", Name: "b", Verified: true},
	}, growth)
	if entry.ID != "project_42" || entry.Kind != "project" || entry.Title != metadata.FullName ||
		entry.CanonicalURL != "https://github.com/octo/repo" || entry.Summary != "GitHub repository" {
		t.Fatalf("normalized identity/source=%+v", entry)
	}
	if entry.Project == nil || entry.Project.RepositoryID != 42 || entry.Project.CreatedAt != created || len(entry.Project.Skills) != 2 {
		t.Fatalf("normalized project=%+v", entry.Project)
	}
	if entry.Project.Growth == nil || !reflect.DeepEqual(entry.Project.Growth, growth) {
		t.Fatalf("growth was not stored once on project: %+v", entry.Project.Growth)
	}
	if entry.Source.Name != "GitHub" || entry.Attribution.Name != "GitHub" || entry.DiscoveredAt != "" {
		t.Fatalf("normalized source/discovery=%+v / %+v / %q", entry.Source, entry.Attribution, entry.DiscoveredAt)
	}
}

func TestRefreshPreservesFirstDiscoveredAtAndCountsOnlyCompleteGrowth(t *testing.T) {
	repo := defaultCollectorFixture("octo/repo", 42)
	repo.files["skills/one/SKILL.md"] = validSkillFile("one")
	repo.files["skills/two/SKILL.md"] = validSkillFile("two")
	repo.history = completeHistoryAt(testNow)
	server := newCollectorServer(repo)
	defer server.Close()
	old := cachedProject(42, "octo/repo")
	old.DiscoveredAt = "2025-02-01T00:00:00Z"
	store := newCollectorStore(old)
	collector := collectorForFixture(store, server, []repositoryCandidate{{ID: 42, FullName: "octo/repo"}})
	if err := collector.Refresh(context.Background(), testNow); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	got := store.entries[old.ID]
	if got.DiscoveredAt != old.DiscoveredAt {
		t.Fatalf("DiscoveredAt=%q, want preserved %q", got.DiscoveredAt, old.DiscoveredAt)
	}
	if got.Project == nil || got.Project.Growth == nil || got.Project.Growth.Metric != "stars_added" || !got.Project.Growth.Complete {
		t.Fatalf("complete seven-day history not preferred: %+v", got.Project)
	}
	if got.Project.Stars != repo.metadata.Stars || got.Project.CreatedAt != repo.metadata.CreatedAt {
		t.Fatalf("metadata stars/createdAt=%+v", got.Project)
	}
	if len(got.Project.Skills) != 2 || !got.Project.Skills[0].Verified || !got.Project.Skills[1].Verified {
		t.Fatalf("complete verified skills were not preserved: %+v", got.Project.Skills)
	}
	if store.coverage.Updated != 1 || store.coverage.CompleteGrowth != 1 {
		t.Fatalf("coverage=%+v, want one updated entry and one complete growth", store.coverage)
	}
	checkpoint := store.checkpoints["github"]
	if checkpoint.Status.State != "fresh" || checkpoint.Status.LastSuccessAt != testNow.Format(time.RFC3339) || checkpoint.FailureCount != 0 {
		t.Fatalf("success checkpoint=%+v", checkpoint)
	}
	if got := store.checkpoints["aihot:weekly"].Status.LastSuccessAt; got != "2025-01-01T00:00:00Z" {
		t.Fatalf("GitHub refresh changed AIHOT LastSuccessAt: %q", got)
	}
}

func TestRefreshUsesSnapshotFallbackOnlyForOrdinaryHistoryAbsence(t *testing.T) {
	for _, tc := range []struct {
		name          string
		historyStatus int
		rateLimited   bool
		historyRaw    string
		wantSuccess   bool
	}{
		{name: "unauthorized history falls back", historyStatus: http.StatusUnauthorized, wantSuccess: true},
		{name: "permission denied history falls back", historyStatus: http.StatusForbidden, wantSuccess: true},
		{name: "history 404 falls back", historyStatus: http.StatusNotFound, wantSuccess: true},
		{name: "history server error preserves cache", historyStatus: http.StatusServiceUnavailable},
		{name: "history rate limit preserves cache", historyStatus: http.StatusTooManyRequests},
		{name: "rate limited forbidden preserves cache", historyStatus: http.StatusForbidden, rateLimited: true},
		{name: "malformed history falls back", historyRaw: "{ invalid private-history-body", wantSuccess: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := defaultCollectorFixture("octo/repo", 42)
			repo.historyStatus = tc.historyStatus
			if tc.rateLimited {
				repo.historyHeaders.Set("X-RateLimit-Remaining", "0")
			}
			repo.historyRaw = tc.historyRaw
			server := newCollectorServer(repo)
			defer server.Close()
			old := cachedProject(42, "octo/repo")
			store := newCollectorStore(old)
			store.snapshots[42] = []content.Snapshot{{RepositoryID: 42, At: testNow.Add(-8 * 24 * time.Hour), Stars: 50}}
			collector := collectorForFixture(store, server, []repositoryCandidate{{ID: 42, FullName: "octo/repo"}})
			err := collector.Refresh(context.Background(), testNow)
			if tc.wantSuccess {
				if err != nil {
					t.Fatalf("Refresh: %v", err)
				}
				got := store.entries[old.ID]
				if got.Project == nil || got.Project.Growth == nil || got.Project.Growth.Metric != "stars_net" {
					t.Fatalf("snapshot fallback growth=%+v", got.Project)
				}
				return
			}
			if err == nil || !reflect.DeepEqual(store.entries[old.ID], old) {
				t.Fatalf("history server error should preserve old entry: err=%v entry=%#v", err, store.entries[old.ID])
			}
		})
	}
}

func TestRefreshDoesNotFallbackToSnapshotsForHistoryNetworkFailure(t *testing.T) {
	repo := defaultCollectorFixture("octo/repo", 42)
	server := newCollectorServer(repo)
	defer server.Close()
	old := cachedProject(42, "octo/repo")
	store := newCollectorStore(old)
	store.snapshots[42] = []content.Snapshot{{RepositoryID: 42, At: testNow.Add(-8 * 24 * time.Hour), Stars: 50}}
	transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if strings.HasSuffix(request.URL.Path, "/stargazers/history") {
			return nil, errors.New("simulated network failure")
		}
		return http.DefaultTransport.RoundTrip(request)
	})
	collector := collectorForFixture(store, server, []repositoryCandidate{{ID: 42, FullName: "octo/repo"}})
	collector.client = NewClient(server.URL, &http.Client{Transport: transport}, "")
	if err := collector.Refresh(context.Background(), testNow); err == nil {
		t.Fatal("network failure unexpectedly used snapshot fallback")
	}
	if !reflect.DeepEqual(store.entries[old.ID], old) {
		t.Fatalf("network failure changed old project: got %#v, want %#v", store.entries[old.ID], old)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (roundTripper roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTripper(request)
}

func TestRefreshRetainsCandidateCursorWhenBudgetPauses(t *testing.T) {
	one := defaultCollectorFixture("octo/one", 1)
	two := defaultCollectorFixture("octo/two", 2)
	server := newCollectorServer(one, two)
	defer server.Close()
	store := newCollectorStore()
	collector := NewCollector(store, NewClient(server.URL, nil, ""), Config{
		QueryVersion: "fixture", Queries: []string{"topic:none"}, CoreRequestsPerHour: 1,
	})
	collector.lastDiscovery = testNow
	collector.candidates = []repositoryCandidate{{ID: 1, FullName: "octo/one"}, {ID: 2, FullName: "octo/two"}}
	if err := collector.Refresh(context.Background(), testNow); !errors.Is(err, errRequestBudget) {
		t.Fatalf("first Refresh error=%v, want request budget pause", err)
	}
	if one.metadataRequests != 1 || two.metadataRequests != 0 {
		t.Fatalf("first candidate request counts: one=%d two=%d", one.metadataRequests, two.metadataRequests)
	}
	if collector.cursor != 0 {
		t.Fatalf("cursor=%d after partial budget pause, want current candidate 0", collector.cursor)
	}
	if err := collector.Refresh(context.Background(), testNow.Add(time.Hour)); !errors.Is(err, errRequestBudget) {
		t.Fatalf("second Refresh error=%v, want request budget pause", err)
	}
	if one.metadataRequests != 2 || two.metadataRequests != 0 || collector.cursor != 0 {
		t.Fatalf("budget retry changed candidates: first metadata=%d second metadata=%d cursor=%d; want retry candidate 0 only", one.metadataRequests, two.metadataRequests, collector.cursor)
	}
}

func TestRefreshBudgetExhaustedTicksDoNotStarveDenseRepositories(t *testing.T) {
	fixtures := make([]*collectorHTTPFixture, 0, 7)
	candidates := make([]repositoryCandidate, 0, 7)
	for i := 0; i < 7; i++ {
		id := int64(100 + i)
		repo := fmt.Sprintf("octo/dense-%d", i)
		fixture := defaultCollectorFixture(repo, id)
		for skill := 0; skill < 20; skill++ {
			name := fmt.Sprintf("s-%02d", skill)
			fixture.files[fmt.Sprintf("skills/%s/SKILL.md", name)] = validSkillFile(name)
		}
		fixtures = append(fixtures, fixture)
		candidates = append(candidates, repositoryCandidate{ID: id, FullName: repo})
	}
	server := newCollectorServer(fixtures...)
	defer server.Close()
	store := newCollectorStore()
	collector := collectorForFixture(store, server, candidates)
	for tick := 0; tick < 42; tick++ {
		_ = collector.Refresh(context.Background(), testNow.Add(time.Duration(tick)*10*time.Minute))
	}
	if len(store.entries) != len(candidates) {
		t.Fatalf("refreshed %d of %d dense candidates under 40/hour budget across seven hourly resets; budget-paused ticks must retain the incomplete candidate", len(store.entries), len(candidates))
	}
}

func TestRefreshRetainsCandidateCursorWhenGitHubRateLimited(t *testing.T) {
	limited := defaultCollectorFixture("octo/limited", 1)
	limited.metadataStatus = http.StatusTooManyRequests
	limited.metadataHeaders.Set("Retry-After", "120")
	ready := defaultCollectorFixture("octo/ready", 2)
	server := newCollectorServer(limited, ready)
	defer server.Close()
	store := newCollectorStore()
	collector := collectorForFixture(store, server, []repositoryCandidate{
		{ID: 1, FullName: "octo/limited"},
		{ID: 2, FullName: "octo/ready"},
	})
	if err := collector.Refresh(context.Background(), testNow); err == nil {
		t.Fatal("Refresh unexpectedly succeeded after GitHub rate limit")
	}
	if collector.cursor != 0 || ready.metadataRequests != 0 {
		t.Fatalf("cursor=%d ready metadata requests=%d after rate limit; want candidate 0 retained", collector.cursor, ready.metadataRequests)
	}
	if err := collector.Refresh(context.Background(), testNow.Add(time.Minute)); !errors.Is(err, errRequestBudget) {
		t.Fatalf("Refresh during Retry-After error=%v, want budget pause", err)
	}
	if collector.cursor != 0 || ready.metadataRequests != 0 {
		t.Fatalf("cursor=%d ready metadata requests=%d during Retry-After; want candidate 0 retained", collector.cursor, ready.metadataRequests)
	}
}

func TestRefreshAdvancesCandidateCursorAfterOtherFailure(t *testing.T) {
	failed := defaultCollectorFixture("octo/failed", 1)
	failed.metadataStatus = http.StatusServiceUnavailable
	ready := defaultCollectorFixture("octo/ready", 2)
	server := newCollectorServer(failed, ready)
	defer server.Close()
	store := newCollectorStore()
	collector := collectorForFixture(store, server, []repositoryCandidate{
		{ID: 1, FullName: "octo/failed"},
		{ID: 2, FullName: "octo/ready"},
	})
	if err := collector.Refresh(context.Background(), testNow); err == nil {
		t.Fatal("Refresh unexpectedly succeeded after metadata failure")
	}
	if collector.cursor != 1 || ready.metadataRequests != 0 {
		t.Fatalf("cursor=%d ready metadata requests=%d after failure; want cursor to advance to candidate 1 without processing it in the same tick", collector.cursor, ready.metadataRequests)
	}
	failed.metadataStatus = 0
	if err := collector.Refresh(context.Background(), testNow.Add(10*time.Minute)); err != nil {
		t.Fatalf("Refresh next candidate: %v", err)
	}
	if ready.metadataRequests != 1 {
		t.Fatalf("next candidate metadata requests=%d, want 1", ready.metadataRequests)
	}
}

func TestRefreshSurfacesStorageErrorsAndCoverageCountsSuccessfulUpserts(t *testing.T) {
	one := defaultCollectorFixture("octo/one", 1)
	two := defaultCollectorFixture("octo/two", 2)
	server := newCollectorServer(one, two)
	defer server.Close()
	store := newCollectorStore()
	store.upsertErrors[projectID(2)] = errors.New("disk full")
	collector := NewCollector(store, NewClient(server.URL, nil, ""), Config{QueryVersion: "fixture", Queries: []string{"topic:none"}})
	collector.lastDiscovery = testNow
	collector.candidates = []repositoryCandidate{{ID: 1, FullName: "octo/one"}, {ID: 2, FullName: "octo/two"}}
	err := collector.Refresh(context.Background(), testNow)
	if err == nil || !strings.Contains(err.Error(), "storage") {
		t.Fatalf("Refresh error=%v, want surfaced storage failure", err)
	}
	if _, ok := store.entries[projectID(1)]; !ok {
		t.Fatal("first successful candidate was not upserted")
	}
	if store.coverage.Updated != 1 || store.coverage.Candidates != 2 {
		t.Fatalf("coverage=%+v, want candidates=2 updated=1", store.coverage)
	}
	if got := store.checkpoints["github"].Status.State; got != "stale" {
		t.Fatalf("checkpoint state=%q, want stale after storage failure", got)
	}
}

func TestRefreshResumesKnownPoolWhenSearchDiscoveryFails(t *testing.T) {
	repo := defaultCollectorFixture("octo/known", 42)
	repo.searchStatus = http.StatusServiceUnavailable
	server := newCollectorServer(repo)
	defer server.Close()
	store := newCollectorStore()
	collector := collectorForFixture(store, server, []repositoryCandidate{{ID: 42, FullName: "octo/known"}})
	collector.lastDiscovery = time.Time{}
	if err := collector.Refresh(context.Background(), testNow); err == nil {
		t.Fatal("Refresh should report discovery failure after processing known candidates")
	}
	if _, ok := store.entries[projectID(42)]; !ok {
		t.Fatal("failed search replaced or skipped known candidates")
	}
	if len(collector.candidates) != 1 || collector.candidates[0].ID != 42 || collector.search == nil {
		t.Fatalf("failed discovery did not preserve pool and resumable search: pool=%+v search=%+v", collector.candidates, collector.search)
	}
}

func TestRepositoryRenameKeepsIDAndUpdatesCanonicalName(t *testing.T) {
	repo := defaultCollectorFixture("octo/renamed", 42)
	repo.requestName = "octo/old-name"
	server := newCollectorServer(repo)
	defer server.Close()
	store := newCollectorStore(cachedProject(42, "octo/old-name"))
	collector := collectorForFixture(store, server, []repositoryCandidate{{ID: 42, FullName: "octo/old-name"}})
	if err := collector.Refresh(context.Background(), testNow); err != nil {
		t.Fatalf("Refresh renamed repository: %v", err)
	}
	got := store.entries[projectID(42)]
	if got.ID != "project_42" || got.Title != "octo/renamed" || got.Project == nil || got.Project.Repo != "octo/renamed" {
		t.Fatalf("rename changed identity or failed to update canonical name: %+v", got)
	}
}

func TestInspectRepositoryRejectsDifferentRepositoryIDAfterRename(t *testing.T) {
	repo := defaultCollectorFixture("octo/renamed", 99)
	repo.requestName = "octo/old-name"
	server := newCollectorServer(repo)
	defer server.Close()
	_, err := NewClient(server.URL, nil, "").inspectRepository(context.Background(), testNow, repositoryCandidate{ID: 42, FullName: "octo/old-name"})
	if err == nil || !strings.Contains(err.Error(), "id") {
		t.Fatalf("inspectRepository accepted a different repository ID after rename: %v", err)
	}
	if repo.treeRequests != 0 {
		t.Fatalf("ID mismatch made %d tree requests, want none", repo.treeRequests)
	}
}

type collectorHTTPFixture struct {
	requestName      string
	metadata         repositoryMetadata
	metadataStatus   int
	treeStatus       int
	historyStatus    int
	searchStatus     int
	historyRaw       string
	treeTruncated    bool
	files            map[string]string
	contentStatus    map[string]int
	metadataHeaders  http.Header
	searchHeaders    http.Header
	historyHeaders   http.Header
	history          []starHistoryWeek
	metadataRequests int
	treeRequests     int
}

func defaultCollectorFixture(repo string, id int64) *collectorHTTPFixture {
	return &collectorHTTPFixture{
		requestName: repo,
		metadata: repositoryMetadata{
			ID: id, FullName: repo, HTMLURL: "https://github.com/" + repo,
			Description: "MCP server for development tools", Stars: 73,
			CreatedAt: "2024-01-02T03:04:05Z", DefaultBranch: "main", Topics: []string{"mcp-server"},
		},
		files: map[string]string{}, contentStatus: map[string]int{}, metadataHeaders: make(http.Header),
		searchHeaders: make(http.Header), historyHeaders: make(http.Header),
	}
}

func newCollectorServer(fixtures ...*collectorHTTPFixture) *httptest.Server {
	return httptest.NewServer(collectorFixtureHandler(fixtures))
}

type collectorFixtureHandler []*collectorHTTPFixture

func (fixtures collectorFixtureHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/search/repositories" {
		for _, fixture := range fixtures {
			for name, values := range fixture.searchHeaders {
				for _, value := range values {
					w.Header().Add(name, value)
				}
			}
			if fixture.searchStatus > 0 {
				http.Error(w, "search unavailable", fixture.searchStatus)
				return
			}
		}
		_, _ = fmt.Fprint(w, "{\"items\":[],\"incomplete_results\":false}")
		return
	}
	for _, fixture := range fixtures {
		requestName := fixture.requestName
		if requestName == "" {
			requestName = fixture.metadata.FullName
		}
		requestPath := "/repos/" + requestName
		canonicalPath := "/repos/" + fixture.metadata.FullName
		if r.URL.Path == requestPath {
			fixture.metadataRequests++
			for name, values := range fixture.metadataHeaders {
				for _, value := range values {
					w.Header().Add(name, value)
				}
			}
			if fixture.metadataStatus > 0 {
				http.Error(w, "metadata unavailable", fixture.metadataStatus)
				return
			}
			_ = json.NewEncoder(w).Encode(fixture.metadata)
			return
		}
		if r.URL.Path == canonicalPath+"/git/trees/"+fixture.metadata.DefaultBranch {
			fixture.treeRequests++
			if fixture.treeStatus > 0 {
				http.Error(w, "tree unavailable", fixture.treeStatus)
				return
			}
			items := make([]map[string]string, 0, len(fixture.files))
			for path := range fixture.files {
				items = append(items, map[string]string{"path": path, "type": "blob"})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"truncated": fixture.treeTruncated, "tree": items})
			return
		}
		if r.URL.Path == canonicalPath+"/stargazers/history" {
			for name, values := range fixture.historyHeaders {
				for _, value := range values {
					w.Header().Add(name, value)
				}
			}
			if fixture.historyStatus > 0 {
				http.Error(w, "history unavailable", fixture.historyStatus)
				return
			}
			if fixture.historyRaw != "" {
				_, _ = fmt.Fprint(w, fixture.historyRaw)
				return
			}
			if fixture.history == nil {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(fixture.history)
			return
		}
		contentsPrefix := canonicalPath + "/contents/"
		if strings.HasPrefix(r.URL.Path, contentsPrefix) {
			path := strings.TrimPrefix(r.URL.Path, contentsPrefix)
			if status := fixture.contentStatus[path]; status > 0 {
				http.Error(w, "content unavailable", status)
				return
			}
			body, ok := fixture.files[path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{
				"type": "file", "path": path, "encoding": "base64",
				"content": base64.StdEncoding.EncodeToString([]byte(body)),
			})
			return
		}
	}
	http.NotFound(w, r)
}

func collectorForFixture(store *collectorMemoryStore, server *httptest.Server, candidates []repositoryCandidate) *Collector {
	collector := NewCollector(store, NewClient(server.URL, nil, ""), Config{QueryVersion: "fixture", Queries: []string{"topic:none"}})
	collector.lastDiscovery = testNow
	collector.candidates = append([]repositoryCandidate(nil), candidates...)
	return collector
}

func cachedProject(id int64, repo string) content.Entry {
	return content.Entry{
		ID: projectID(id), Kind: "project", Title: repo, Summary: "cached summary",
		Source:       content.Source{Name: "GitHub", URL: "https://github.com/" + repo},
		Attribution:  content.Source{Name: "GitHub", URL: "https://github.com/" + repo},
		CanonicalURL: "https://github.com/" + repo, DiscoveredAt: "2025-01-01T00:00:00Z", Paragraphs: []content.Paragraph{},
		Project: &content.Project{
			RepositoryID: id, Repo: repo, Type: "skill", Stars: 12, CreatedAt: "2024-01-02T03:04:05Z",
			Scenes: []string{"development"},
			Growth: &content.Growth{Metric: "stars_net", Value: 12, WindowStart: "2025-01-01T00:00:00Z", WindowEnd: "2025-01-02T00:00:00Z", ObservedDays: 1},
			Skills: []content.SkillRef{{RepositoryID: id, Repo: repo, Path: "SKILL.md", Name: "old-skill", Verified: true}},
		},
	}
}

func validSkillFile(name string) string {
	return fmt.Sprintf("---\nname: %s\ndescription: Skill %s\n---\n", name, name)
}

func completeHistoryAt(now time.Time) []starHistoryWeek {
	anchor := now.UTC().Truncate(24 * time.Hour).Add(-7 * 24 * time.Hour).Unix()
	return []starHistoryWeek{{Week: anchor, Total: 28, Days: []int{1, 2, 3, 4, 5, 6, 7}}}
}

type collectorMemoryStore struct {
	entries           map[string]content.Entry
	deleted           []string
	snapshots         map[int64][]content.Snapshot
	coverage          content.Coverage
	checkpoints       map[string]content.SourceCheckpoint
	upsertErrors      map[string]error
	coverageErr       error
	checkpointSaveErr error
}

func newCollectorStore(entries ...content.Entry) *collectorMemoryStore {
	store := &collectorMemoryStore{
		entries: make(map[string]content.Entry), snapshots: make(map[int64][]content.Snapshot),
		checkpoints: make(map[string]content.SourceCheckpoint), upsertErrors: make(map[string]error),
	}
	for _, entry := range entries {
		store.entries[entry.ID] = entry
	}
	store.checkpoints["aihot:weekly"] = content.SourceCheckpoint{
		Name: "aihot:weekly", Status: content.SourceStatus{Name: "aihot:weekly", State: "fresh", LastSuccessAt: "2025-01-01T00:00:00Z"},
	}
	return store
}

func (store *collectorMemoryStore) Upsert(_ context.Context, entry content.Entry) error {
	if err := store.upsertErrors[entry.ID]; err != nil {
		return err
	}
	store.entries[entry.ID] = entry
	return nil
}

func (store *collectorMemoryStore) Delete(_ context.Context, id string) error {
	store.deleted = append(store.deleted, id)
	delete(store.entries, id)
	return nil
}

func (store *collectorMemoryStore) Get(_ context.Context, id string) (content.Entry, error) {
	entry, ok := store.entries[id]
	if !ok {
		return content.Entry{}, content.ErrNotFound
	}
	return entry, nil
}

func (store *collectorMemoryStore) All(_ context.Context, kind string) ([]content.Entry, error) {
	ids := make([]string, 0, len(store.entries))
	for id, entry := range store.entries {
		if kind == "" || kind == entry.Kind {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	entries := make([]content.Entry, 0, len(ids))
	for _, id := range ids {
		entries = append(entries, store.entries[id])
	}
	return entries, nil
}

func (store *collectorMemoryStore) SaveSnapshot(_ context.Context, id int64, at time.Time, stars int) error {
	for i, row := range store.snapshots[id] {
		if row.At.Equal(at) {
			store.snapshots[id][i].Stars = stars
			return nil
		}
	}
	store.snapshots[id] = append(store.snapshots[id], content.Snapshot{RepositoryID: id, At: at, Stars: stars})
	return nil
}

func (store *collectorMemoryStore) Snapshots(_ context.Context, id int64) ([]content.Snapshot, error) {
	return append([]content.Snapshot(nil), store.snapshots[id]...), nil
}

func (store *collectorMemoryStore) SaveCoverage(_ context.Context, coverage content.Coverage) error {
	if store.coverageErr != nil {
		return store.coverageErr
	}
	store.coverage = coverage
	return nil
}

func (store *collectorMemoryStore) SaveCheckpoint(_ context.Context, name string, checkpoint content.SourceCheckpoint) error {
	if store.checkpointSaveErr != nil {
		return store.checkpointSaveErr
	}
	checkpoint.Name = name
	store.checkpoints[name] = checkpoint
	return nil
}

func (store *collectorMemoryStore) Checkpoints(context.Context) ([]content.SourceCheckpoint, error) {
	names := make([]string, 0, len(store.checkpoints))
	for name := range store.checkpoints {
		names = append(names, name)
	}
	sort.Strings(names)
	rows := make([]content.SourceCheckpoint, 0, len(names))
	for _, name := range names {
		rows = append(rows, store.checkpoints[name])
	}
	return rows, nil
}
