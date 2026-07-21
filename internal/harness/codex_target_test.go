package harness

import (
	"os"
	"path/filepath"
	"testing"
)

// Codex reads global skills from ~/.codex/skills and project skills from
// <repo>/.agents/skills. The user-level ~/.agents/skills is npx's store, NOT a
// Codex target, even though it shares the /.agents/skills suffix.
func TestIsCodexTargetScopeAware(t *testing.T) {
	t.Setenv("CODEX_HOME", "") // default ~/.codex
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		dir  string
		want bool
	}{
		{filepath.Join(home, ".codex", "skills"), true},   // global Codex
		{filepath.Join(home, ".agents", "skills"), false}, // user npx store — NOT Codex
		{"/work/proj/.agents/skills", true},               // project Codex
		{"/work/proj/.codex/skills", true},                // legacy project Codex, still recognized
		{filepath.Join(home, ".claude", "skills"), false}, // CC, not Codex
	}
	for _, c := range cases {
		if got := IsCodexTarget(c.dir); got != c.want {
			t.Errorf("IsCodexTarget(%q) = %v, want %v", c.dir, got, c.want)
		}
	}
	// Consequently the user npx dir classifies as NOT codex.
	if Classify(filepath.Join(home, ".agents", "skills")) == HarnessCodex {
		t.Errorf("user ~/.agents/skills must not classify as codex")
	}
}

// A project holding both .agents/skills and .codex/skills must resolve its Codex
// target to .agents/skills (where Codex reads project skills), not .codex/skills.
func TestSkillDirsForProjectPrefersAgents(t *testing.T) {
	proj := t.TempDir()
	for _, sub := range [][]string{{".claude", "skills"}, {".agents", "skills"}, {".codex", "skills"}} {
		if err := os.MkdirAll(filepath.Join(proj, filepath.Join(sub...)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got := SkillDirsFor(proj)
	agents := filepath.Join(proj, ".agents", "skills")
	codex := filepath.Join(proj, ".codex", "skills")
	has := func(want string) bool {
		for _, g := range got {
			if g == want {
				return true
			}
		}
		return false
	}
	if !has(agents) {
		t.Errorf("Codex target should be .agents/skills, got %v", got)
	}
	if has(codex) {
		t.Errorf(".codex/skills must NOT be chosen when .agents/skills exists (one codex target), got %v", got)
	}
}

// CreateSkillDirs scaffolds the chosen harness skill dirs under a plain folder:
// cc → .claude/skills, codex → .agents/skills (project-level Codex).
func TestCreateSkillDirs(t *testing.T) {
	base := t.TempDir()
	got, err := CreateSkillDirs(base, []string{"cc", "codex"})
	if err != nil {
		t.Fatalf("CreateSkillDirs: %v", err)
	}
	cc := filepath.Join(base, ".claude", "skills")
	cx := filepath.Join(base, ".agents", "skills")
	for _, want := range []string{cc, cx} {
		if fi, err := os.Stat(want); err != nil || !fi.IsDir() {
			t.Errorf("expected dir %q created, err=%v", want, err)
		}
		found := false
		for _, g := range got {
			if g == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%q not in returned dirs %v", want, got)
		}
	}
	// codex must scaffold .agents/skills, never a project-level .codex/skills.
	if fi, err := os.Stat(filepath.Join(base, ".codex")); err == nil && fi.IsDir() {
		t.Errorf(".codex must NOT be created for a codex choice (project Codex reads .agents/skills)")
	}
	// The scaffolded dirs classify as expected.
	if Classify(cc) != HarnessClaudeCode {
		t.Errorf("scaffolded cc dir should classify cc")
	}
	if Classify(cx) != HarnessCodex {
		t.Errorf("scaffolded codex dir should classify codex")
	}
}

func TestCreateSkillDirsCodexOnly(t *testing.T) {
	base := t.TempDir()
	got, err := CreateSkillDirs(base, []string{"codex"})
	if err != nil || len(got) != 1 || got[0] != filepath.Join(base, ".agents", "skills") {
		t.Fatalf("codex-only should create just .agents/skills, got %v err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(base, ".claude")); err == nil {
		t.Errorf(".claude must not be created for a codex-only choice")
	}
}

func hasStr(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// A Codex project target inherits from parent dirs' .agents/skills (Codex walks
// up) and the user-global ~/.codex(CODEX_HOME)/skills — never from itself.
func TestAncestorSkillDirsCodex(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	userCodex := filepath.Join(codexHome, "skills")
	os.MkdirAll(userCodex, 0o755)

	base := t.TempDir()
	parent := filepath.Join(base, ".agents", "skills")
	os.MkdirAll(parent, 0o755)
	target := filepath.Join(base, "proj", ".agents", "skills")
	os.MkdirAll(target, 0o755)

	got := AncestorSkillDirs(target)
	if !hasStr(got, parent) {
		t.Errorf("should inherit parent .agents/skills %q, got %v", parent, got)
	}
	if !hasStr(got, userCodex) {
		t.Errorf("should inherit user-global codex %q, got %v", userCodex, got)
	}
	if hasStr(got, target) {
		t.Errorf("must not include the target itself, got %v", got)
	}
}

// CC does NOT walk parent project dirs (unlike Codex) — only the user-global
// ~/.claude/skills. A parent project's .claude/skills must not appear.
func TestAncestorSkillDirsCCNoParentWalk(t *testing.T) {
	base := t.TempDir()
	parentCC := filepath.Join(base, ".claude", "skills")
	os.MkdirAll(parentCC, 0o755)
	target := filepath.Join(base, "proj", ".claude", "skills")
	os.MkdirAll(target, 0o755)

	got := AncestorSkillDirs(target)
	if hasStr(got, parentCC) {
		t.Errorf("CC must not inherit from a parent project's .claude/skills, got %v", got)
	}
	if hasStr(got, target) {
		t.Errorf("must not include the target itself, got %v", got)
	}
}
