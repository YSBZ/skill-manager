package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"skillmanage/internal/config"
	"skillmanage/internal/skillcheck"
)

func checkSkillFixture(t *testing.T, root, name, body string) string {
	t.Helper()
	p := mkdirT(t, filepath.Join(root, name))
	if err := os.WriteFile(filepath.Join(p, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: fixture\n---\n"+body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func skillCheckGET(t *testing.T, s *Server, route string) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req("GET", route, s.token, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s: %d %s", route, w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("expected JSON report: %v", err)
	}
	return got
}

func TestSkillCheckRejectsUnauthorizedAndUnconfiguredPaths(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := newTestServer(t)
	target := mkdirT(t, filepath.Join(t.TempDir(), ".claude", "skills"))
	s.cfg.Targets = []string{target}
	for _, tc := range []struct {
		query, token string
		code         int
	}{
		{"?target=" + url.QueryEscape(target) + "&name=foo", "", http.StatusUnauthorized},
		{"?target=/unconfigured&name=foo", s.token, http.StatusBadRequest},
		{"?target=" + url.QueryEscape(target) + "&name=../outside", s.token, http.StatusBadRequest},
		{"?target=" + url.QueryEscape(target) + "&name=..%5Coutside", s.token, http.StatusBadRequest},
		{"?repo=untracked&name=foo", s.token, http.StatusBadRequest},
	} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req("GET", "/api/skill-check"+tc.query, tc.token, nil))
		if w.Code != tc.code {
			t.Errorf("%s: got %d want %d", tc.query, w.Code, tc.code)
		}
	}
}

func TestInventorySurfacesBrokenOwnedLinkWithoutClaimingEnabled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := newTestServer(t)
	target := mkdirT(t, filepath.Join(t.TempDir(), "skills"))
	dest := filepath.Join(s.personalStore, "gone")
	symlinkT(t, dest, filepath.Join(target, "gone"))
	s.cfg.Targets = []string{target}
	s.manifest.Links = []config.LinkRecord{{Name: "gone", Source: dest, Target: target, LinkType: config.LinkSymlink}}
	got := skillCheckGET(t, s, "/api/inventory?target="+url.QueryEscape(target))
	items := got["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("broken skill must stay visible: %+v", got)
	}
	i := items[0].(map[string]any)
	if i["enabled"] != false || i["selector"] != "@local/gone" || i["managed"] != true {
		t.Fatalf("broken link must remain manageable without claiming enabled: %+v", i)
	}
	c, ok := i["check"].(map[string]any)
	if !ok || c["state"] != "issues" || c["path"].(map[string]any)["entry"] != filepath.Join(target, "gone") {
		t.Fatalf("missing broken path evidence: %+v", i)
	}
	got = skillCheckGET(t, s, "/api/skill-check?target="+url.QueryEscape(target)+"&name=gone")
	if got["state"] != "issues" {
		t.Fatalf("detail should explain the broken entry: %+v", got)
	}
	// Opening an unavailable skill still returns its identity; its check explains
	// the unavailable content instead of losing the entry behind a 404.
	skillCheckGET(t, s, "/api/skill-at?target="+url.QueryEscape(target)+"&name=gone")
	if link, err := os.Readlink(filepath.Join(target, "gone")); err != nil || link != dest {
		t.Fatalf("checks must never rewrite links: %q %v", link, err)
	}
}

func TestSkillCheckDependencyMustBeVisibleToCurrentTarget(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := newTestServer(t)
	target := mkdirT(t, filepath.Join(t.TempDir(), ".claude", "skills"))
	other := mkdirT(t, filepath.Join(t.TempDir(), ".claude", "skills"))
	checkSkillFixture(t, target, "grill-me", `Call the Skill tool with "grilling".`)
	checkSkillFixture(t, other, "grilling", "Ask questions.")
	s.cfg.Targets = []string{target, other}
	query := "/api/skill-check?target=" + url.QueryEscape(target) + "&name=grill-me"
	got := skillCheckGET(t, s, query)
	deps, ok := got["dependencies"].([]any)
	if !ok || len(deps) != 1 || deps[0].(map[string]any)["state"] != "missing" {
		t.Fatalf("other target must not satisfy dependency: %+v", got)
	}
	checkSkillFixture(t, target, "grilling", "Ask questions.")
	got = skillCheckGET(t, s, query)
	if got["state"] != "checked" || got["dependencies"].([]any)[0].(map[string]any)["state"] != "present" {
		t.Fatalf("recheck must notice newly available dependency: %+v", got)
	}
}

func TestSkillCheckResolvesInheritedCodexAndNamespacedPlugins(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	codex := mkdirT(t, filepath.Join(home, "custom-agent-home"))
	t.Setenv("CODEX_HOME", codex)
	s := newTestServer(t)
	target := mkdirT(t, filepath.Join(t.TempDir(), "project", ".agents", "skills"))
	checkSkillFixture(t, target, "grill-me", "REQUIRED SUB-SKILL: Use grilling\nREQUIRED SUB-SKILL: Use interview:probe")
	checkSkillFixture(t, filepath.Join(codex, "skills"), "grilling", "Ask questions.")
	plugin := mkdirT(t, filepath.Join(t.TempDir(), "plugin"))
	checkSkillFixture(t, filepath.Join(plugin, "skills"), "probe", "Ask for evidence.")
	mkdirT(t, filepath.Join(codex, "plugins"))
	data, _ := json.Marshal(map[string]any{"plugins": map[string]any{"interview@market": []any{map[string]any{"installPath": plugin}}}})
	os.WriteFile(filepath.Join(codex, "plugins", "installed_plugins.json"), data, 0o644)
	s.cfg.Targets = []string{target}
	got := skillCheckGET(t, s, "/api/skill-check?target="+url.QueryEscape(target)+"&name=grill-me")
	deps := got["dependencies"].([]any)
	if len(deps) != 2 {
		t.Fatalf("expected inherited and plugin dependencies: %+v", got)
	}
	for _, dep := range deps {
		if dep.(map[string]any)["state"] != "present" {
			t.Fatalf("visible dependency missing: %+v", got)
		}
	}
	got = skillCheckGET(t, s, "/api/skill-check?target="+url.QueryEscape(target)+"&plugin=interview&name=probe&harness=codex")
	if got["state"] != "checked" {
		t.Fatalf("plugin detail must be checkable in custom CODEX_HOME: %+v", got)
	}
	got = skillCheckGET(t, s, "/api/plugin-skill?plugin=interview&name=probe&harness=codex")
	if got["content"] == "" {
		t.Fatal("plugin preview must agree with check about custom CODEX_HOME")
	}
}

func TestSkillCheckPluginInstallScope(t *testing.T) {
	for _, tc := range []struct {
		name, scope, project, home, namespace string
		present                               bool
	}{
		{"current project", "project", "current", ".claude", "example", true},
		{"ancestor project", "project", "ancestor", ".claude", "example", true},
		{"other project", "project", "other", ".claude", "example", false},
		{"local current project", "local", "current", ".claude", "example", true},
		{"local other project", "local", "other", ".claude", "example", false},
		{"project unknown path", "project", "", ".claude", "example", false},
		{"project relative path", "project", "relative", ".claude", "example", false},
		{"descendant project", "project", "descendant", ".claude", "example", false},
		{"local unknown path", "local", "", ".claude", "example", false},
		{"user", "user", "", ".claude", "example", true},
		{"system", "system", "", ".claude", "example", true},
		{"managed", "managed", "", ".claude", "example", true},
		{"legacy manifest", "", "", ".claude", "example", true},
		{"legacy project path", "", "current", ".claude", "example", true},
		{"legacy other project path", "", "other", ".claude", "example", false},
		{"unknown scope", "unexpected", "", ".claude", "example", false},
		{"other harness", "user", "", ".codex", "example", false},
		{"other namespace", "user", "", ".claude", "different", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
			s := newTestServer(t)
			ancestor := filepath.Join(t.TempDir(), "parent")
			project := filepath.Join(ancestor, "project-a")
			target := mkdirT(t, filepath.Join(project, ".claude", "skills"))
			wrapper := checkSkillFixture(t, target, "wrapper", "REQUIRED SUB-SKILL: Use example:probe")
			plugin := t.TempDir()
			checkSkillFixture(t, filepath.Join(plugin, "skills"), "probe", "Do work.")
			projectPath := map[string]string{"current": project, "ancestor": ancestor, "other": project + "-other", "relative": "project-a", "descendant": filepath.Join(project, "child")}[tc.project]
			manifestDir := mkdirT(t, filepath.Join(home, tc.home, "plugins"))
			data, _ := json.Marshal(map[string]any{"plugins": map[string]any{tc.namespace + "@market": []any{map[string]any{"scope": tc.scope, "projectPath": projectPath, "installPath": plugin}}}})
			if err := os.WriteFile(filepath.Join(manifestDir, "installed_plugins.json"), data, 0o644); err != nil {
				t.Fatal(err)
			}
			s.cfg.Targets = []string{target}
			ctx := s.checkContext(target)
			got := skillcheck.Inspect(wrapper, ctx.client, ctx.available)
			want := "missing"
			if tc.present {
				want = "present"
			}
			if len(got.Dependencies) != 1 || got.Dependencies[0].State != want {
				t.Errorf("dependency must respect install scope: want %s, got %+v", want, got)
			}
			w := httptest.NewRecorder()
			route := "/api/skill-check?target=" + url.QueryEscape(target) + "&plugin=example&name=probe"
			s.Handler().ServeHTTP(w, req("GET", route, s.token, nil))
			wantCode := http.StatusNotFound
			if tc.present {
				wantCode = http.StatusOK
			}
			if w.Code != wantCode {
				t.Errorf("plugin check must use same scope: got %d want %d: %s", w.Code, wantCode, w.Body.String())
			}
			w = httptest.NewRecorder()
			route = "/api/plugin-skill?target=" + url.QueryEscape(target) + "&plugin=example&name=probe&harness=cc"
			s.Handler().ServeHTTP(w, req("GET", route, s.token, nil))
			if w.Code != wantCode {
				t.Errorf("plugin content preview must use same scope: got %d want %d: %s", w.Code, wantCode, w.Body.String())
			}
		})
	}
}

func TestProjectPluginDoesNotApplyToUserTarget(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	s := newTestServer(t)
	target := mkdirT(t, filepath.Join(home, ".claude", "skills"))
	plugin := t.TempDir()
	checkSkillFixture(t, filepath.Join(plugin, "skills"), "probe", "Do work.")
	manifestDir := mkdirT(t, filepath.Join(home, ".claude", "plugins"))
	for _, scope := range []string{"project", "local"} {
		data, _ := json.Marshal(map[string]any{"plugins": map[string]any{"example@market": []any{map[string]any{"scope": scope, "projectPath": home, "installPath": plugin}}}})
		if err := os.WriteFile(filepath.Join(manifestDir, "installed_plugins.json"), data, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, ok := s.checkContext(target).available["example:probe"]; ok {
			t.Errorf("%s plugin must not become global through a user-level target", scope)
		}
	}
}

func TestSkillCheckSelectsPluginInstallAfterScopeFiltering(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	s := newTestServer(t)
	project := t.TempDir()
	target := mkdirT(t, filepath.Join(project, ".claude", "skills"))
	wrong, global, current := t.TempDir(), t.TempDir(), t.TempDir()
	for _, dir := range []string{wrong, global} {
		checkSkillFixture(t, filepath.Join(dir, "skills"), "probe", "REQUIRED SUB-SKILL: Use wrong-install")
	}
	correct := checkSkillFixture(t, filepath.Join(current, "skills"), "probe", "Do work.")
	manifestDir := mkdirT(t, filepath.Join(home, ".claude", "plugins"))
	data, _ := json.Marshal(map[string]any{"plugins": map[string]any{"example@market": []any{
		map[string]any{"scope": "project", "projectPath": project + "-other", "installPath": wrong},
		map[string]any{"scope": "user", "installPath": global},
		map[string]any{"scope": "project", "projectPath": project, "installPath": current},
	}}})
	if err := os.WriteFile(filepath.Join(manifestDir, "installed_plugins.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	s.cfg.Targets = []string{target}
	if got := s.checkContext(target).available["example:probe"]; got != correct {
		t.Errorf("nearest applicable install must win after scope filtering: got %q want %q", got, correct)
	}
	got := skillCheckGET(t, s, "/api/skill-check?target="+url.QueryEscape(target)+"&plugin=example&name=probe")
	if got["state"] != "checked" {
		t.Fatalf("plugin preview check must inspect selected install: %+v", got)
	}
	preview := skillCheckGET(t, s, "/api/plugin-skill?target="+url.QueryEscape(target)+"&plugin=example&name=probe&harness=cc")
	if content, _ := preview["content"].(string); content != "---\nname: probe\ndescription: fixture\n---\nDo work." {
		t.Fatalf("plugin content preview must use current install: %+v", preview)
	}
	if len(listPluginSkills()) != 1 {
		t.Fatal("installed inventory must retain one card per harness/plugin/skill")
	}
}

func TestPluginPreviewRejectsInvalidTargetAndHarness(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := newTestServer(t)
	target := mkdirT(t, filepath.Join(t.TempDir(), ".claude", "skills"))
	s.cfg.Targets = []string{target}
	for _, query := range []string{
		"target=/unconfigured&plugin=example&name=probe",
		"target=" + url.QueryEscape(target) + "&plugin=example&name=probe&harness=codex",
		"target=" + url.QueryEscape(target) + "&plugin=example&name=..%5Coutside",
	} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req("GET", "/api/plugin-skill?"+query, s.token, nil))
		if w.Code != http.StatusBadRequest {
			t.Errorf("invalid scoped preview must be rejected: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestSourceSkillCheckUsesSelectedTargetRatherThanCatalog(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := newTestServer(t)
	target := mkdirT(t, filepath.Join(t.TempDir(), ".claude", "skills"))
	checkSkillFixture(t, s.personalStore, "wrapper", `Call the Skill tool with "dependency".`)
	checkSkillFixture(t, s.personalStore, "dependency", "Do work.")
	s.cfg.Targets = []string{target}
	got := skillCheckGET(t, s, "/api/skill-check?target="+url.QueryEscape(target)+"&repo=%40local&name=wrapper")
	deps := got["dependencies"].([]any)
	if len(deps) != 1 || deps[0].(map[string]any)["state"] != "missing" {
		t.Fatalf("catalog-only dependency must not count as enabled: %+v", got)
	}
}

func TestSkillCheckAssetUsesProcessCacheVersion(t *testing.T) {
	got := string(bustAssetCache([]byte(`<script src="/skill-check.js"></script>`), "check-version"))
	if got != `<script src="/skill-check.js?v=check-version"></script>` {
		t.Fatalf("new UI helper must share process cache version: %s", got)
	}
}

func TestSkillHubSourceSkillCanBePreviewedAndChecked(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := newTestServer(t)
	target := mkdirT(t, filepath.Join(t.TempDir(), ".claude", "skills"))
	checkSkillFixture(t, s.skillHubStore(), "grilling", "Ask for evidence.")
	s.cfg.Targets = []string{target}
	d := skillCheckGET(t, s, "/api/skill?repo=%40skillhub&name=grilling")
	if d["content"] == "" {
		t.Fatal("SkillHub candidates must support preview before enabling a dependency")
	}
	got := skillCheckGET(t, s, "/api/skill-check?repo=%40skillhub&name=grilling&target="+url.QueryEscape(target))
	if got["state"] != "checked" {
		t.Fatalf("independent SkillHub candidate should pass local check: %+v", got)
	}
}
