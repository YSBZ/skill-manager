package skillcheck

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func writeSkill(t *testing.T, dir, content string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func hasFinding(report Report, code string) bool {
	for _, finding := range report.Findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

func dependencyByName(report Report, name string) (Dependency, bool) {
	for _, dep := range report.Dependencies {
		if dep.Name == name {
			return dep, true
		}
	}
	return Dependency{}, false
}

func TestInspectResolvesMultiHopSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}

	root := t.TempDir()
	real := writeSkill(t, filepath.Join(root, "real", "skill"), "---\nname: linked\n---\n")
	second := filepath.Join(root, "second")
	first := filepath.Join(root, "first")
	if err := os.Symlink(real, second); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(second, first); err != nil {
		t.Fatal(err)
	}

	got := Inspect(first, "cc", nil)
	if got.State != "checked" || got.Path.State != "ok" {
		t.Fatalf("expected healthy skill path, got state=%q path=%+v findings=%+v", got.State, got.Path, got.Findings)
	}
	if got.Path.Kind != "symlink" {
		t.Errorf("kind = %q, want symlink", got.Path.Kind)
	}
	resolved, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	if got.Path.Resolved != resolved {
		t.Errorf("resolved = %q, want %q", got.Path.Resolved, resolved)
	}
	if got.Path.LinkTarget == "" {
		t.Error("multi-hop symlink should report its entry link target")
	}
}

func TestInspectDoesNotMisclassifyDirectoryBelowSymlinkParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}
	root := t.TempDir()
	parent := filepath.Join(root, "real-parent")
	entry := writeSkill(t, filepath.Join(parent, "skill"), "---\nname: skill\n---\n")
	alias := filepath.Join(root, "parent-alias")
	if err := os.Symlink(parent, alias); err != nil {
		t.Fatal(err)
	}

	got := Inspect(filepath.Join(alias, "skill"), "cc", nil)
	if got.Path.Kind != "directory" {
		t.Errorf("entry below a symlink parent has kind %q, want directory", got.Path.Kind)
	}
	if got.Path.LinkTarget != "" {
		t.Errorf("ordinary directory reported link target %q", got.Path.LinkTarget)
	}
	resolved, err := filepath.EvalSymlinks(entry)
	if err != nil {
		t.Fatal(err)
	}
	if got.Path.Resolved != resolved {
		t.Errorf("resolved = %q, want %q", got.Path.Resolved, resolved)
	}
}

func TestInspectReportsBrokenLoopMissingEntryAndMissingSkill(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}

	root := t.TempDir()
	broken := filepath.Join(root, "broken")
	if err := os.Symlink(filepath.Join(root, "not-there"), broken); err != nil {
		t.Fatal(err)
	}
	loopA, loopB := filepath.Join(root, "loop-a"), filepath.Join(root, "loop-b")
	if err := os.Symlink(loopB, loopA); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(loopA, loopB); err != nil {
		t.Fatal(err)
	}
	plainDir := filepath.Join(root, "plain")
	if err := os.Mkdir(plainDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name      string
		path      string
		pathState string
		code      string
	}{
		{name: "broken symlink", path: broken, pathState: "broken", code: "broken_path"},
		{name: "symlink loop", path: loopA, pathState: "broken", code: "broken_path"},
		{name: "missing entry", path: filepath.Join(root, "missing"), pathState: "missing_skill", code: "missing_skill"},
		{name: "directory without skill file", path: plainDir, pathState: "missing_skill", code: "missing_skill"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Inspect(tc.path, "cc", nil)
			if got.Path.State != tc.pathState {
				t.Errorf("path state = %q, want %q", got.Path.State, tc.pathState)
			}
			if tc.name == "broken symlink" && got.Path.Kind != "symlink" {
				t.Errorf("broken link kind = %q, want symlink", got.Path.Kind)
			}
			if tc.name == "broken symlink" && got.Path.LinkTarget != filepath.Join(root, "not-there") {
				t.Errorf("broken link target = %q", got.Path.LinkTarget)
			}
			if !hasFinding(got, tc.code) {
				t.Errorf("findings %+v do not include %q", got.Findings, tc.code)
			}
			if got.State != "issues" {
				t.Errorf("state = %q, want issues", got.State)
			}
		})
	}
}

func TestInspectFindsGrillingDependencyWhenMissingOrAvailable(t *testing.T) {
	root := t.TempDir()
	grillMe := writeSkill(t, filepath.Join(root, "grill-me"), "---\nname: grill-me\n---\nCall the Skill tool with \"grilling\".\nREQUIRED SUB-SKILL: Use grilling\n")
	grilling := writeSkill(t, filepath.Join(root, "grilling"), "---\nname: grilling\n---\n")

	missing := Inspect(grillMe, "cc", map[string]string{})
	dep, ok := dependencyByName(missing, "grilling")
	if !ok || dep.State != "missing" {
		t.Fatalf("grilling dependency = %+v, found=%t; want missing", dep, ok)
	}
	if !hasFinding(missing, "dependency_missing") || missing.State != "issues" {
		t.Errorf("missing grilling was not reported: %+v", missing)
	}
	for _, finding := range missing.Findings {
		if finding.Code == "dependency_missing" && finding.Level != "error" {
			t.Errorf("dependency finding level = %q, want error", finding.Level)
		}
	}

	present := Inspect(grillMe, "cc", map[string]string{"grilling": grilling})
	dep, ok = dependencyByName(present, "grilling")
	if !ok || dep.State != "present" {
		t.Fatalf("grilling dependency = %+v, found=%t; want present", dep, ok)
	}
	if present.State != "checked" {
		t.Errorf("available compatible skill should be checked, got %+v", present)
	}
}

func TestInspectTraversesDependenciesAndReportsCycle(t *testing.T) {
	root := t.TempDir()
	a := writeSkill(t, filepath.Join(root, "a"), "---\nname: a\n---\nREQUIRED SUB-SKILL: Use tools:b\n")
	b := writeSkill(t, filepath.Join(root, "b"), "---\nname: b\n---\nREQUIRED SUB-SKILL: Use tools:c\n")
	c := writeSkill(t, filepath.Join(root, "c"), "---\nname: c\n---\n")
	available := map[string]string{"tools:b": b, "tools:c": c}
	chain := Inspect(a, "codex", available)
	for _, name := range []string{"tools:b", "tools:c"} {
		if dep, ok := dependencyByName(chain, name); !ok || dep.State != "present" {
			t.Errorf("transitive dependency %q = %+v, found=%t", name, dep, ok)
		}
	}
	if chain.State != "checked" {
		t.Errorf("valid dependency chain state = %q, want checked", chain.State)
	}

	cycle := writeSkill(t, filepath.Join(root, "cycle"), "---\nname: cycle\n---\nREQUIRED SUB-SKILL: Use tools:back\n")
	back := writeSkill(t, filepath.Join(root, "back"), "---\nname: back\n---\nREQUIRED SUB-SKILL: Use tools:cycle\n")
	cyclic := Inspect(cycle, "cc", map[string]string{"tools:back": back, "tools:cycle": cycle})
	if !hasFinding(cyclic, "dependency_cycle") {
		t.Errorf("cycle not reported: %+v", cyclic.Findings)
	}
}

func TestInspectIgnoresFencedExamplesAndRequiresExactNamespace(t *testing.T) {
	root := t.TempDir()
	entry := writeSkill(t, filepath.Join(root, "entry"), "---\nname: entry\n---\n```md\nCall the Skill tool with `hidden-skill`.\nREQUIRED SUB-SKILL: Use tools:hidden\n```\nREQUIRED SUB-SKILL: Use tools:lint\n")
	lint := writeSkill(t, filepath.Join(root, "lint"), "---\nname: lint\n---\n")
	got := Inspect(entry, "cc", map[string]string{"lint": lint, "tools:lint": lint, "hidden-skill": lint})
	if _, ok := dependencyByName(got, "hidden-skill"); ok {
		t.Error("fenced example was treated as a dependency")
	}
	if _, ok := dependencyByName(got, "tools:hidden"); ok {
		t.Error("fenced namespaced example was treated as a dependency")
	}
	if dep, ok := dependencyByName(got, "tools:lint"); !ok || dep.State != "present" {
		t.Errorf("exact namespaced dependency = %+v, found=%t", dep, ok)
	}
	if hasFinding(got, "dependency_missing") {
		t.Errorf("matching tools:lint must not fall back to bare lint: %+v", got.Findings)
	}

	missing := Inspect(entry, "cc", map[string]string{"lint": lint})
	if dep, ok := dependencyByName(missing, "tools:lint"); !ok || dep.State != "missing" {
		t.Errorf("namespaced dependency incorrectly matched bare name: %+v, found=%t", dep, ok)
	}
}

func TestInspectIgnoresCommonMarkExampleBlocks(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"ordered list fence", "1. Example:\n\n    ```text\n    Call the Skill tool with \"example-only\".\n    ```\n"},
		{"unordered list fence", "- Example:\n\n  ~~~text\n  REQUIRED SUB-SKILL: Use example:probe\n  ~~~\n"},
		{"nested list fence", "1. Example:\n   - Nested:\n\n     ```text\n     Call the Skill tool with \"example-only\".\n     ```\n"},
		{"indented code", "    Call the Skill tool with \"example-only\".\n"},
		{"quoted lazy continuation", "> Example:\nCall the Skill tool with \"example-only\".\n"},
		{"quoted list", "> - REQUIRED SUB-SKILL: Use example:probe\n"},
		{"HTML block", "<div>\nCall the Skill tool with \"example-only\".\n</div>\n"},
		{"HTML comment", "<!--\nREQUIRED SUB-SKILL: Use example:probe\n-->\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry := writeSkill(t, filepath.Join(t.TempDir(), "entry"), "---\nname: entry\n---\n"+tc.body)
			got := Inspect(entry, "codex", nil)
			if len(got.Dependencies) != 0 || hasFinding(got, "skill_tool_review") || got.State != "checked" {
				t.Fatalf("example block became a directive: %+v", got)
			}
		})
	}
}

func TestDependencyRefsPreservesListBodyEvidenceAndPositions(t *testing.T) {
	body := "1. **REQUIRED SUB-SKILL:** Use example:first\n\n   - Nested instruction:\n\n     Call the Skill tool with \"example:second\".\n"
	got := dependencyRefs(body)
	if len(got) != 2 {
		t.Fatalf("real list instructions must survive Markdown block parsing: %+v", got)
	}
	for i, want := range []struct{ name, evidence string }{
		{"example:first", "1. **REQUIRED SUB-SKILL:** Use example:first"},
		{"example:second", `Call the Skill tool with "example:second".`},
	} {
		if got[i].name != want.name || got[i].evidence != want.evidence {
			t.Errorf("instruction %d lost original source evidence: %+v", i, got[i])
		}
		if got[i].position != strings.Index(body, want.evidence) {
			t.Errorf("instruction %d position=%d want %d", i, got[i].position, strings.Index(body, want.evidence))
		}
	}
}

func TestInspectRecognizesBareAndEmphasizedRequiredSubSkillLines(t *testing.T) {
	root := t.TempDir()
	entry := writeSkill(t, filepath.Join(root, "entry"), "---\nname: entry\n---\n**REQUIRED SUB-SKILL:** Use tools:foo\n- **REQUIRED SUB-SKILL:** Use grilling\nREQUIRED SUB-SKILL: Use my__skill\n")
	foo := writeSkill(t, filepath.Join(root, "foo"), "---\nname: foo\n---\n")
	grilling := writeSkill(t, filepath.Join(root, "grilling"), "---\nname: grilling\n---\n")
	mySkill := writeSkill(t, filepath.Join(root, "my__skill"), "---\nname: my__skill\n---\n")
	got := Inspect(entry, "cc", map[string]string{"tools:foo": foo, "grilling": grilling, "my__skill": mySkill})
	for _, name := range []string{"tools:foo", "grilling", "my__skill"} {
		if dep, ok := dependencyByName(got, name); !ok || dep.State != "present" {
			t.Errorf("required dependency %q = %+v, found=%t", name, dep, ok)
		}
	}
	if got.State != "checked" {
		t.Errorf("state = %q, want checked; findings=%+v", got.State, got.Findings)
	}
}

func TestInspectIgnoresQuotedAndNegatedSkillToolCallLines(t *testing.T) {
	root := t.TempDir()
	entry := writeSkill(t, filepath.Join(root, "entry"), "---\nname: entry\n---\nThe old file said `Call the Skill tool with \"grilling\"`.\nDo not call the Skill tool with \"grilling\".\n")
	grilling := writeSkill(t, filepath.Join(root, "grilling"), "---\nname: grilling\n---\n")
	got := Inspect(entry, "codex", map[string]string{"grilling": grilling})
	if len(got.Dependencies) != 0 {
		t.Errorf("quoted/negated mentions became dependencies: %+v", got.Dependencies)
	}
	if hasFinding(got, "skill_tool_review") {
		t.Errorf("quoted/negated mentions triggered tool review: %+v", got.Findings)
	}
}

func TestInspectShowsSkillToolAndCompatibilityAsReviewForOtherClient(t *testing.T) {
	root := t.TempDir()
	entry := writeSkill(t, filepath.Join(root, "entry"), "---\nname: entry\ncompatibility: This skill works with Claude Code only.\n---\n- **Call the Skill tool with** 'grilling'.\nCall the Skill tool with `grilling`.\n")
	grilling := writeSkill(t, filepath.Join(root, "grilling"), "---\nname: grilling\n---\n")
	for _, client := range []string{"codex", "pi", "unknown"} {
		got := Inspect(entry, client, map[string]string{"grilling": grilling})
		if got.Compatibility != "This skill works with Claude Code only." {
			t.Errorf("%s compatibility = %q", client, got.Compatibility)
		}
		if !hasFinding(got, "skill_tool_review") || !hasFinding(got, "compatibility_review") {
			t.Errorf("%s expected review findings, got %+v", client, got.Findings)
		}
		for _, finding := range got.Findings {
			if finding.Code == "skill_tool_review" || finding.Code == "compatibility_review" {
				if finding.Level != "warning" {
					t.Errorf("%s finding %s level = %q, want warning", client, finding.Code, finding.Level)
				}
				if strings.Contains(finding.Message, "skill") || strings.Contains(finding.Message, "current") {
					t.Errorf("user-facing finding message is not localized: %q", finding.Message)
				}
			}
		}
		if got.State != "review" {
			t.Errorf("%s state = %q, want review", client, got.State)
		}
	}

	cc := Inspect(entry, "cc", map[string]string{"grilling": grilling})
	if hasFinding(cc, "skill_tool_review") || hasFinding(cc, "compatibility_review") {
		t.Errorf("Claude Code target should not be warned about its own skill tool/compatibility: %+v", cc.Findings)
	}
}

func TestInspectDoesNotInferExclusivityFromUnrelatedOnlyClause(t *testing.T) {
	root := t.TempDir()
	entry := writeSkill(t, filepath.Join(root, "entry"), "---\nname: entry\ncompatibility: Claude Code only reports errors after startup.\n---\n")
	got := Inspect(entry, "codex", nil)
	if hasFinding(got, "compatibility_review") || got.State != "checked" {
		t.Errorf("unrelated only clause was treated as exclusive compatibility: %+v", got)
	}
}

func TestInspectBoundsDependencyTraversal(t *testing.T) {
	root := t.TempDir()
	var body strings.Builder
	body.WriteString("---\nname: root\n---\n")
	available := make(map[string]string)
	for i := 1; i <= 140; i++ {
		name := "node-" + strconv.Itoa(i)
		body.WriteString("REQUIRED SUB-SKILL: Use ns:" + name + "\n")
		available["ns:"+name] = writeSkill(t, filepath.Join(root, name), "---\nname: "+name+"\n---\n")
	}
	entry := writeSkill(t, filepath.Join(root, "root"), body.String())
	got := Inspect(entry, "cc", available)
	if !hasFinding(got, "dependency_truncated") {
		t.Errorf("expected traversal limit finding, got %d dependencies and findings %+v", len(got.Dependencies), got.Findings)
	}
	if got.State != "review" {
		t.Errorf("truncated report state = %q, want review", got.State)
	}
}

func TestInspectReportsDependencyDepthLimit(t *testing.T) {
	root := t.TempDir()
	available := make(map[string]string)
	for i := 0; i <= 34; i++ {
		name := "depth-" + strconv.Itoa(i)
		body := "---\nname: " + name + "\n---\n"
		if i < 34 {
			body += "REQUIRED SUB-SKILL: Use ns:depth-" + strconv.Itoa(i+1) + "\n"
		}
		dir := writeSkill(t, filepath.Join(root, name), body)
		if i > 0 {
			available["ns:"+name] = dir
		}
	}
	got := Inspect(filepath.Join(root, "depth-0"), "cc", available)
	if !hasFinding(got, "dependency_truncated") || got.State != "review" {
		t.Errorf("depth-limited chain was not reported for review: state=%q findings=%+v", got.State, got.Findings)
	}
}

func TestInspectHandlesInvalidOrOversizedYAMLAndAllowedToolsWithoutMissingToolFindings(t *testing.T) {
	root := t.TempDir()
	invalid := writeSkill(t, filepath.Join(root, "invalid"), "---\nallowed-tools: [\n---\nBody\n")
	bad := Inspect(invalid, "cc", nil)
	if !hasFinding(bad, "invalid_frontmatter") || bad.State != "issues" {
		t.Errorf("invalid YAML should be an issue: %+v", bad)
	}

	large := writeSkill(t, filepath.Join(root, "large"), "---\nname: large\n---\n"+strings.Repeat("x", (1<<20)+1))
	tooLarge := Inspect(large, "cc", nil)
	if tooLarge.Path.State != "unreadable" || !hasFinding(tooLarge, "skill_too_large") {
		t.Errorf("oversized Skill.md should be unreadable: path=%+v findings=%+v", tooLarge.Path, tooLarge.Findings)
	}

	allowed := writeSkill(t, filepath.Join(root, "allowed"), "---\nname: allowed\nallowed-tools: Bash(git diff:*)\n---\n")
	stringTools := Inspect(allowed, "cc", nil)
	if len(stringTools.AllowedTools) != 1 || stringTools.AllowedTools[0] != "Bash(git diff:*)" {
		t.Errorf("string allowed-tools = %#v", stringTools.AllowedTools)
	}
	if stringTools.State != "checked" || hasFinding(stringTools, "missing_tool") {
		t.Errorf("allowed-tools must not be reported as missing requirements: %+v", stringTools)
	}

	list := writeSkill(t, filepath.Join(root, "list"), "---\nname: list\nallowed-tools:\n  - Read\n  - Grep\n---\n")
	listTools := Inspect(list, "cc", nil)
	if strings.Join(listTools.AllowedTools, ",") != "Read,Grep" {
		t.Errorf("list allowed-tools = %#v", listTools.AllowedTools)
	}
	if listTools.State != "checked" {
		t.Errorf("declared tools should not create missing dependency findings: %+v", listTools)
	}
}

func TestInspectJSONUsesSmallCamelCaseFields(t *testing.T) {
	root := t.TempDir()
	entry := writeSkill(t, filepath.Join(root, "entry"), "---\nname: entry\nallowed-tools: Read\n---\n")
	got := Inspect(entry, "cc", nil)
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var encoded map[string]any
	if err := json.Unmarshal(b, &encoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"state", "path", "dependencies", "compatibility", "allowedTools", "findings"} {
		if _, ok := encoded[key]; !ok {
			t.Errorf("missing JSON field %q in %s", key, b)
		}
	}
	path, ok := encoded["path"].(map[string]any)
	if !ok {
		t.Fatalf("path JSON is %T", encoded["path"])
	}
	for _, key := range []string{"entry", "linkTarget", "resolved", "kind", "state"} {
		if _, ok := path[key]; !ok {
			t.Errorf("missing path JSON field %q in %s", key, b)
		}
	}
}

func TestReadContentAcceptsOnlyRegularFilesWithinLimit(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "SKILL.md")
	if err := os.WriteFile(file, []byte("skill"), 0o644); err != nil {
		t.Fatal(err)
	}
	content, err := ReadContent(file)
	if err != nil || content != "skill" {
		t.Fatalf("ReadContent regular file = %q, %v", content, err)
	}
	if _, err := ReadContent(root); err == nil {
		t.Error("ReadContent should reject a directory")
	}
	if err := os.WriteFile(file, []byte(strings.Repeat("x", (1<<20)+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadContent(file); err == nil {
		t.Error("ReadContent should reject files larger than 1 MiB")
	}
}
