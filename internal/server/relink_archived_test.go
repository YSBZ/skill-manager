package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"skillmanage/internal/config"
	"skillmanage/internal/reconcile"
)

// The archive (@local managed store) holds the skill, but the sync target has a
// REAL dir instead of our symlink (diverged). handleRelinkArchived must delete
// the real dir and recreate the symlink → archive; the archive stays intact.
func TestRelinkArchivedRemovesRealDirAndRelinks(t *testing.T) {
	s := newTestServer(t)
	target := mkdirT(t, filepath.Join(t.TempDir(), "skills"))
	archive := mkdirT(t, filepath.Join(s.personalStore, "docs"))
	os.WriteFile(filepath.Join(archive, "SKILL.md"), []byte("---\nname: docs\n---\n"), 0o644)
	// a real dir occupies the target (as if another tool replaced our link)
	real := mkdirT(t, filepath.Join(target, "docs"))
	os.WriteFile(filepath.Join(real, "SKILL.md"), []byte("---\nname: docs\n---\n"), 0o644)
	s.mu.Lock()
	s.cfg.Targets = []string{target}
	s.cfg.Enabled = []config.EnabledEntry{{Skill: reconcile.LocalNamespace + "/docs", Target: target, Mode: config.ModeSnapshot}}
	s.mu.Unlock()

	w := httptest.NewRecorder()
	s.handleRelinkArchived(w, req("POST", "/api/relink-archived", s.token, map[string]string{"target": target, "name": "docs"}))
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, body=%s", w.Code, w.Body.String())
	}
	link := filepath.Join(target, "docs")
	lst, err := os.Lstat(link)
	if err != nil || lst.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("target should be a symlink now, mode=%v err=%v", lst.Mode(), err)
	}
	// content is reachable through the link (points at a valid skill)…
	if _, err := os.Stat(filepath.Join(link, "SKILL.md")); err != nil {
		t.Errorf("symlink should resolve to the archived skill, err=%v", err)
	}
	// …and the archive itself is untouched.
	if _, err := os.Stat(filepath.Join(archive, "SKILL.md")); err != nil {
		t.Errorf("archive must survive, err=%v", err)
	}
}

// Without an archive/enabled entry there is no desired link → refuse, and the
// real dir must survive (never destroy the only copy).
func TestRelinkArchivedRefusesWithoutArchive(t *testing.T) {
	s := newTestServer(t)
	target := mkdirT(t, filepath.Join(t.TempDir(), "skills"))
	real := mkdirT(t, filepath.Join(target, "docs"))
	os.WriteFile(filepath.Join(real, "SKILL.md"), []byte("---\nname: docs\n---\n"), 0o644)
	s.mu.Lock()
	s.cfg.Targets = []string{target}
	s.mu.Unlock()

	w := httptest.NewRecorder()
	s.handleRelinkArchived(w, req("POST", "/api/relink-archived", s.token, map[string]string{"target": target, "name": "docs"}))
	if w.Code != http.StatusBadRequest {
		t.Errorf("must refuse without an archive/enabled entry, got %d", w.Code)
	}
	if _, err := os.Stat(real); err != nil {
		t.Errorf("real dir must survive when refused, err=%v", err)
	}
}

func TestRelinkArchivedRejectsTraversalAndBadTarget(t *testing.T) {
	s := newTestServer(t)
	target := mkdirT(t, filepath.Join(t.TempDir(), "skills"))
	s.mu.Lock()
	s.cfg.Targets = []string{target}
	s.mu.Unlock()
	for _, name := range []string{"../evil", "a/b", "..", ""} {
		w := httptest.NewRecorder()
		s.handleRelinkArchived(w, req("POST", "/api/relink-archived", s.token, map[string]string{"target": target, "name": name}))
		if w.Code != http.StatusBadRequest {
			t.Errorf("name %q must be rejected, got %d", name, w.Code)
		}
	}
	w := httptest.NewRecorder()
	s.handleRelinkArchived(w, req("POST", "/api/relink-archived", s.token, map[string]string{"target": "/not/configured", "name": "docs"}))
	if w.Code != http.StatusBadRequest {
		t.Errorf("unknown target must be rejected, got %d", w.Code)
	}
}
