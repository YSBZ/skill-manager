package contentservice

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"skillmanage/internal/content"
	"skillmanage/internal/content/sqlite"
)

func TestWeeklyGrowthFallbackCatalogHTTP(t *testing.T) {
	for _, tc := range []struct {
		name     string
		metrics  []string
		complete []bool
		want     []string
	}{
		{"net only", []string{"stars_net", "stars_net"}, []bool{true, true}, []string{"project_2", "project_1"}},
		{"short samples", []string{"stars_net", "stars_added"}, []bool{false, false}, []string{}},
		{"added takes priority", []string{"stars_net", "stars_added"}, []bool{true, true}, []string{"project_2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := sqlite.Open(filepath.Join(t.TempDir(), "content.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
			for i, metric := range tc.metrics {
				days := 7
				if !tc.complete[i] {
					days = 2
				}
				entry := content.Entry{ID: fmt.Sprintf("project_%d", i+1), Kind: "project", Title: "fixture", Paragraphs: []content.Paragraph{}, Project: &content.Project{RepositoryID: int64(i + 1), Repo: fmt.Sprintf("org/repo%d", i+1), Type: "tool", Growth: &content.Growth{Metric: metric, Value: i + 1, ObservedDays: days, Complete: tc.complete[i], WindowStart: "2026-10-20T06:30:00Z", WindowEnd: "2026-10-27T06:30:00Z"}}}
				if err := store.Upsert(context.Background(), entry); err != nil {
					t.Fatal(err)
				}
			}
			w := httptest.NewRecorder()
			NewHandler(content.NewCatalog(store, func() time.Time { return now })).ServeHTTP(w, httptest.NewRequest("GET", "/v1/feed?kind=project&view=weekly", nil))
			if w.Code != 200 {
				t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
			}
			var page content.Page
			if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
				t.Fatal(err)
			}
			if len(page.Items) != len(tc.want) {
				t.Fatalf("items=%#v want=%v", page.Items, tc.want)
			}
			for i, id := range tc.want {
				if page.Items[i].ID != id {
					t.Fatalf("item %d=%s want %s", i, page.Items[i].ID, id)
				}
			}
		})
	}
}
