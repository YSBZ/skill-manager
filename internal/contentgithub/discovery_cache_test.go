package contentgithub

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"
)

func TestGrowthRateLimitDoesNotDiscardVerifiedProjectDiscovery(t *testing.T) {
	for _, cached := range []bool{false, true} {
		t.Run(map[bool]string{false: "first discovery", true: "existing project"}[cached], func(t *testing.T) {
			repo := defaultCollectorFixture("octo/verified", 1)
			repo.files["SKILL.md"] = validSkillFile("verified")
			repo.historyStatus = http.StatusTooManyRequests
			repo.historyHeaders.Set("Retry-After", "120")
			server := newCollectorServer(repo)
			defer server.Close()
			store := newCollectorStore()
			old := cachedProject(1, "octo/verified")
			if cached {
				store.entries[old.ID] = old
			}
			collector := collectorForFixture(store, server, []repositoryCandidate{{ID: 1, FullName: "octo/verified"}})
			if err := collector.Refresh(context.Background(), testNow); !isRateLimitPause(err, testNow) {
				t.Fatalf("must still report GitHub rate limit: %v", err)
			}
			got, ok := store.entries[projectID(1)]
			if !ok || got.Project == nil || got.Summary != repo.metadata.Description {
				t.Fatalf("verified discovery disappeared behind growth request: %+v", got)
			}
			if len(got.Project.Skills) != 1 || !got.Project.Skills[0].Verified {
				t.Fatalf("existing Skill validation must remain required: %+v", got.Project)
			}
			if cached {
				if got.DiscoveredAt != old.DiscoveredAt || !reflect.DeepEqual(got.Project.Growth, old.Project.Growth) {
					t.Fatalf("refresh changed first discovery or fabricated growth: %+v", got)
				}
			} else if got.Project.Growth != nil || got.DiscoveredAt != testNow.UTC().Format(time.RFC3339) {
				t.Fatalf("new discovery must not fabricate a growth window: %+v", got)
			}
			if store.coverage.Updated != 1 || store.checkpoints["github"].Status.State != "rate_limited" {
				t.Fatalf("saved discovery and limited source must both be reflected: coverage=%+v checkpoint=%+v", store.coverage, store.checkpoints["github"])
			}
		})
	}
}

func TestGrowthBudgetPauseSavesDiscoveryAndLetsNextCandidateProgress(t *testing.T) {
	one := defaultCollectorFixture("octo/one", 1)
	two := defaultCollectorFixture("octo/two", 2)
	server := newCollectorServer(one, two)
	defer server.Close()
	store := newCollectorStore()
	collector := collectorForFixture(store, server, []repositoryCandidate{{ID: 1, FullName: "octo/one"}, {ID: 2, FullName: "octo/two"}})
	collector.client.coreBudget = 2 // repo metadata + complete tree; growth pauses
	if err := collector.Refresh(context.Background(), testNow); !errors.Is(err, errRequestBudget) {
		t.Fatalf("expected budget pause: %v", err)
	}
	if _, ok := store.entries[projectID(1)]; !ok || collector.cursor != 1 || store.coverage.Updated != 1 {
		t.Fatalf("completed discovery must progress despite unavailable growth: entries=%v cursor=%d coverage=%+v", store.entries, collector.cursor, store.coverage)
	}
	if err := collector.Refresh(context.Background(), testNow.Add(time.Hour)); !errors.Is(err, errRequestBudget) {
		t.Fatalf("expected next candidate growth pause: %v", err)
	}
	if _, ok := store.entries[projectID(2)]; !ok {
		t.Fatalf("growth-only retry starved the next discovery: entries=%v", store.entries)
	}
}

func TestNewProjectRemainsDiscoverableWhenSkillInstallEvidenceIsIncomplete(t *testing.T) {
	for _, variant := range []string{"too many skills", "duplicate names", "invalid frontmatter"} {
		t.Run(variant, func(t *testing.T) {
			repo := defaultCollectorFixture("octo/skill-library", 1)
			switch variant {
			case "too many skills":
				for i := 0; i < maxSkillFilesPerRepository+1; i++ {
					repo.files[fmt.Sprintf("skills/demo-%d/SKILL.md", i)] = validSkillFile(fmt.Sprintf("demo-%d", i))
				}
			case "duplicate names":
				repo.files["one/SKILL.md"] = validSkillFile("same")
				repo.files["two/SKILL.md"] = validSkillFile("same")
			case "invalid frontmatter":
				repo.files["SKILL.md"] = "# No frontmatter"
			}
			server := newCollectorServer(repo)
			defer server.Close()
			store := newCollectorStore()
			collector := collectorForFixture(store, server, []repositoryCandidate{{ID: 1, FullName: "octo/skill-library"}})
			if err := collector.Refresh(context.Background(), testNow); err != nil {
				t.Fatal(err)
			}
			entry, ok := store.entries[projectID(1)]
			if !ok || entry.Project == nil || entry.Project.Type != "skill" || entry.Project.Growth != nil || entry.Reason == "" {
				t.Fatalf("verified repository metadata should be discoverable with incomplete install evidence: %+v", entry)
			}
			for _, ref := range entry.Project.Skills {
				if ref.Verified {
					t.Fatalf("listing a project must not bypass installation checks: %+v", ref)
				}
			}
		})
	}
}
