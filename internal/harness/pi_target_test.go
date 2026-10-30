package harness

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsPiTargetScopeAware(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "")
	home, _ := os.UserHomeDir()
	cases := []struct {
		dir  string
		want bool
	}{
		{filepath.Join(home, ".pi", "agent", "skills"), true}, // 全局 pi
		{"/work/proj/.pi/skills", true},                       // 项目 pi
		{filepath.Join(home, ".agents", "skills"), false},     // npx 库，非 pi
		{filepath.Join(home, ".codex", "skills"), false},      // codex
		{filepath.Join(home, ".claude", "skills"), false},     // cc
		{"/work/proj/.agents/skills", false},                  // 项目 codex，非 pi
	}
	for _, c := range cases {
		if got := isPiTarget(c.dir); got != c.want {
			t.Errorf("isPiTarget(%q) = %v, want %v", c.dir, got, c.want)
		}
	}
}

func TestClassifyPi(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "")
	home, _ := os.UserHomeDir()
	if got := Classify(filepath.Join(home, ".pi", "agent", "skills")); got != HarnessPi {
		t.Errorf("global pi classify = %v, want pi", got)
	}
	if got := Classify("/work/proj/.pi/skills"); got != HarnessPi {
		t.Errorf("project pi classify = %v, want pi", got)
	}
}

func TestPiAgentDirEnvOverride(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "/custom/piroot")
	if got := piSkillsRoot(); got != filepath.Join("/custom/piroot", "skills") {
		t.Errorf("piSkillsRoot override = %q", got)
	}
	if !isPiTarget("/custom/piroot/skills") {
		t.Errorf("overridden pi root skills should be a pi target")
	}
}

func TestScopePi(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "")
	home, _ := os.UserHomeDir()
	if got := Scope(filepath.Join(home, ".pi", "agent", "skills")); got != ScopeUser {
		t.Errorf("global pi scope = %v, want user", got)
	}
	if got := Scope("/work/proj/.pi/skills"); got != ScopeProject {
		t.Errorf("project pi scope = %v, want project", got)
	}
}

func TestCreateSkillDirsPi(t *testing.T) {
	base := t.TempDir()
	got, err := CreateSkillDirs(base, []string{"pi"})
	if err != nil || len(got) != 1 || got[0] != filepath.Join(base, ".pi", "skills") {
		t.Fatalf("pi create = %v err=%v", got, err)
	}
	if Classify(got[0]) != HarnessPi {
		t.Errorf("scaffolded pi dir should classify pi")
	}
}

func TestScaffoldSkillDirsPi(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, ".pi"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := ScaffoldSkillDirs(base)
	want := filepath.Join(base, ".pi", "skills")
	if !contains(got, want) {
		t.Errorf("scaffold should create %q, got %v", want, got)
	}
}

func TestSkillDirsForPi(t *testing.T) {
	proj := t.TempDir()
	pi := filepath.Join(proj, ".pi", "skills")
	if err := os.MkdirAll(pi, 0o755); err != nil {
		t.Fatal(err)
	}
	if !contains(SkillDirsFor(proj), pi) {
		t.Errorf("SkillDirsFor should include %q, got %v", pi, SkillDirsFor(proj))
	}
}

func TestDiscoverDefaultTargetsPi(t *testing.T) {
	piRoot := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", piRoot)
	if err := os.MkdirAll(filepath.Join(piRoot, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(piRoot, "skills")
	if !contains(DiscoverDefaultTargets(), want) {
		t.Errorf("discover should include overridden pi skills %q, got %v", want, DiscoverDefaultTargets())
	}
}

func TestAncestorSkillDirsPi(t *testing.T) {
	piRoot := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", piRoot)
	globalSkills := filepath.Join(piRoot, "skills")
	if err := os.MkdirAll(globalSkills, 0o755); err != nil {
		t.Fatal(err)
	}
	proj := t.TempDir()
	projSkills := filepath.Join(proj, ".pi", "skills")
	if err := os.MkdirAll(projSkills, 0o755); err != nil {
		t.Fatal(err)
	}
	anc := AncestorSkillDirs(projSkills)
	if !contains(anc, globalSkills) {
		t.Errorf("pi project target should inherit global pi skills %q, got %v", globalSkills, anc)
	}
}

// contains is a small test helper.
func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
