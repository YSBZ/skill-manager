package server

import (
	"os"
	"path/filepath"
	"testing"

	"skillmanage/internal/gitsync"
)

// classifySkillChanges maps a sync's changed files to skill-level add/update/remove,
// using the repo's CURRENT skills (dirs with SKILL.md) to attribute each path.
func TestClassifySkillChanges(t *testing.T) {
	repo := t.TempDir()
	// Current skills present after the sync: alpha, beta. (gamma was removed.)
	for _, name := range []string{"alpha", "beta"} {
		d := filepath.Join(repo, name)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(d, "SKILL.md"), []byte("---\nname: "+name+"\n---\n"), 0o644)
	}
	files := []gitsync.FileChange{
		{Path: "alpha/SKILL.md", Status: "M"},       // alpha updated
		{Path: "alpha/reference/x.md", Status: "A"}, // still just alpha updated
		{Path: "beta/SKILL.md", Status: "A"},        // beta is brand new → added
		{Path: "gamma/SKILL.md", Status: "D"},       // gamma's SKILL.md gone, dir absent → removed
		{Path: "README.md", Status: "M"},            // repo-level file, under no skill → ignored
	}

	got := classifySkillChanges(repo, files)

	want := map[string]string{"alpha": "updated", "beta": "added", "gamma": "removed"}
	if len(got) != len(want) {
		t.Fatalf("got %d changes %v, want %d", len(got), got, len(want))
	}
	for _, c := range got {
		if want[c.Name] != c.Kind {
			t.Errorf("skill %q: got kind %q, want %q", c.Name, c.Kind, want[c.Name])
		}
	}
	// README.md must not surface as a change.
	for _, c := range got {
		if c.Name == "README" || c.Name == "README.md" {
			t.Errorf("repo-level file leaked as a skill change: %v", c)
		}
	}
}

// No changed files → no skill changes (a no-op sync shows nothing).
func TestClassifySkillChangesEmpty(t *testing.T) {
	if got := classifySkillChanges(t.TempDir(), nil); got != nil {
		t.Errorf("empty input should yield nil, got %v", got)
	}
}
