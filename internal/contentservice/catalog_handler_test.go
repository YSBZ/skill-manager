package contentservice

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"skillmanage/internal/content/sqlite"
)

func TestNewCatalogHandlerServesStoreBackedStatus(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "content.db"))
	if err != nil {
		t.Fatalf("sqlite.Open(): %v", err)
	}
	defer store.Close()

	handler := NewCatalogHandler(store, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/status", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("GET /v1/status status=%d body=%q, want 200", response.Code, response.Body.String())
	}
}
