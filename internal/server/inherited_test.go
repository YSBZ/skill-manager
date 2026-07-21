package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// handleInherited surfaces skills a project target inherits from higher levels
// (here: the user-global Codex dir) so the UI can flag duplicates.
func TestInheritedListsUserGlobalCodex(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	// a skill living at the user-global codex level
	userSkill := filepath.Join(codexHome, "skills", "foo")
	if err := os.MkdirAll(userSkill, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(userSkill, "SKILL.md"), []byte("---\nname: foo\n---\n"), 0o644)

	s := newTestServer(t)
	base := t.TempDir()
	target := filepath.Join(base, "proj", ".agents", "skills")
	os.MkdirAll(target, 0o755)

	w := httptest.NewRecorder()
	s.handleInherited(w, req("GET", "/api/inherited?target="+target, s.token, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Inherited []struct {
			Name, From, Scope string
		} `json:"inherited"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, i := range resp.Inherited {
		if i.Name == "foo" && i.Scope == "user" {
			found = true
		}
	}
	if !found {
		t.Errorf("inherited should list user-global 'foo', got %+v", resp.Inherited)
	}
}
