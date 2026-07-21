package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// Adding a plain existing folder with no .claude/.codex/.agents home must NOT be
// silently added as an "unknown" target — it must ask the user (needsChoice).
func TestAddTargetPlainFolderNeedsChoice(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	s := newTestServer(t)
	h := s.Handler()
	dir := t.TempDir() // a real, plain folder — no agent homes inside
	os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("x"), 0o644) // a file, not a .agents dir

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req("POST", "/api/targets", s.token, map[string]string{"dir": dir}))
	if w.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["needsChoice"] != true {
		t.Fatalf("plain folder must return needsChoice, got %v", resp)
	}
	// and it must NOT have been added as a target
	cfg, _, _ := loadCfgForTest(s)
	for _, tg := range cfg {
		if tg == dir {
			t.Errorf("plain folder must not be added verbatim, targets=%v", cfg)
		}
	}
}

// With a create choice, it scaffolds and adds the chosen harness dirs.
func TestAddTargetPlainFolderCreate(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	s := newTestServer(t)
	h := s.Handler()
	dir := t.TempDir()

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req("POST", "/api/targets", s.token, map[string]any{"dir": dir, "create": []string{"cc", "codex"}}))
	if w.Code != http.StatusCreated && w.Code != http.StatusOK {
		t.Fatalf("create got %d body=%s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude", "skills")); err != nil {
		t.Errorf(".claude/skills should be scaffolded: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".agents", "skills")); err != nil {
		t.Errorf(".agents/skills should be scaffolded: %v", err)
	}
}

func loadCfgForTest(s *Server) ([]string, error, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.cfg.Targets...), nil, nil
}
