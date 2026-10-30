package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"skillmanage/internal/config"
	"skillmanage/internal/scanner"
)

func testContentIdentity(t *testing.T) contentInstallIdentity {
	t.Helper()
	return contentInstallIdentity{
		RepositoryID: 42,
		Path:         "skills/lint-review",
		CanonicalDir: filepath.Join(t.TempDir(), "lint-review"),
		SourceRepo:   "acme/tools",
		Name:         "lint-review",
	}
}

func TestReadContentIdentitiesMissingMalformedAndInvalid(t *testing.T) {
	dir := t.TempDir()
	records, err := readContentIdentities(dir)
	if err != nil || records == nil || len(records) != 0 {
		t.Fatalf("missing sidecar = (%v, %v), want empty non-nil records and no error", records, err)
	}

	path := filepath.Join(dir, "content-install-identities.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readContentIdentities(dir); err == nil {
		t.Fatal("malformed sidecar was accepted")
	}

	if err := os.WriteFile(path, []byte(`[{"repositoryId":42,"path":"../escape","canonicalDir":"/tmp/skill","sourceRepo":"acme/tools","name":"lint-review"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readContentIdentities(dir); err == nil {
		t.Fatal("invalid identity record was accepted")
	}
}

func TestContentIdentityStateAndConflictPreservesFile(t *testing.T) {
	dir := t.TempDir()
	actual := filepath.Join(dir, "skill")
	skill := scanner.Skill{FrontmatterName: "lint-review", Dir: actual}
	record := contentInstallIdentity{
		RepositoryID: 42,
		Path:         "skills/lint-review",
		CanonicalDir: actual,
		SourceRepo:   "acme/tools",
		Name:         "lint-review",
	}

	if got := contentIdentityState(skill, actual, "acme/tools", nil, &record); got != "source_same_pending" {
		t.Fatalf("legacy state=%q", got)
	}
	if got := contentIdentityState(skill, actual, "acme/tools", []contentInstallIdentity{record}, &record); got != "exact" {
		t.Fatalf("exact state=%q", got)
	}
	if got := contentIdentityState(skill, actual, "other/tools", nil, &record); got != "conflict" {
		t.Fatalf("source state=%q", got)
	}
	if got := contentIdentityState(skill, actual, "acme/tools", []contentInstallIdentity{record}, nil); got != "exact" {
		t.Fatalf("current scan state=%q", got)
	}
	wrongName := skill
	wrongName.FrontmatterName = "changed-name"
	if got := contentIdentityState(wrongName, actual, "acme/tools", []contentInstallIdentity{record}, &record); got != "not_match" {
		t.Fatalf("frontmatter mismatch state=%q", got)
	}
	wrongPath := record
	wrongPath.Path = "legacy/lint-review"
	if got := contentIdentityState(skill, actual, "acme/tools", []contentInstallIdentity{record}, &wrongPath); got != "conflict" {
		t.Fatalf("path mismatch state=%q", got)
	}
	wrongRepositoryID := record
	wrongRepositoryID.RepositoryID++
	if got := contentIdentityState(skill, actual, "acme/tools", []contentInstallIdentity{record}, &wrongRepositoryID); got != "conflict" {
		t.Fatalf("repository ID mismatch state=%q", got)
	}

	lockPath := filepath.Join(dir, ".skill-lock.json")
	lockBefore := []byte(`{"skills":{"lint-review":{"source":"acme/tools"}}}`)
	if err := os.WriteFile(lockPath, lockBefore, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeContentIdentities(dir, []contentInstallIdentity{record}); err != nil {
		t.Fatal(err)
	}
	sidecar := filepath.Join(dir, "content-install-identities.json")
	before, err := os.ReadFile(sidecar)
	if err != nil {
		t.Fatal(err)
	}
	if err := upsertContentIdentity(dir, record); err != nil {
		t.Fatalf("identical upsert: %v", err)
	}
	conflict := record
	conflict.Path = "legacy/lint-review"
	if err := upsertContentIdentity(dir, conflict); !errors.Is(err, errContentIdentityConflict) {
		t.Fatalf("conflicting upsert error=%v", err)
	}
	conflictRepositoryID := record
	conflictRepositoryID.RepositoryID++
	if err := upsertContentIdentity(dir, conflictRepositoryID); !errors.Is(err, errContentIdentityConflict) {
		t.Fatalf("changed repository ID upsert error=%v", err)
	}
	after, err := os.ReadFile(sidecar)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("idempotent or conflicting upsert changed provenance bytes")
	}
	lockAfter, err := os.ReadFile(lockPath)
	if err != nil || !bytes.Equal(lockBefore, lockAfter) {
		t.Fatalf("CLI lock changed: (%q, %v)", lockAfter, err)
	}
	assertContentIdentityFilePrivate(t, sidecar)
}

func TestRestrictContentIdentityFilePermissions(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), ".content-identity-test-*.tmp")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(file.Name())
	if err := restrictContentIdentityFile(file); err != nil {
		t.Fatal(err)
	}
	assertContentIdentityFilePrivate(t, file.Name())
}

func TestContentIdentityPathAndCandidateValidation(t *testing.T) {
	for _, path := range []string{"", ".", "..", "../escape", "skills/../escape", "/absolute", `skills\name`} {
		if validContentSkillPath(path) {
			t.Errorf("validContentSkillPath(%q) = true", path)
		}
	}
	if !validContentSkillPath("skills/lint-review/SKILL.md") {
		t.Fatal("valid slash-relative skill file path was rejected")
	}

	valid := testContentIdentity(t)
	invalid := valid
	invalid.RepositoryID = 0
	dir := t.TempDir()
	if err := writeContentIdentities(dir, []contentInstallIdentity{invalid}); err == nil {
		t.Fatal("invalid candidate was written")
	}
	if _, err := os.Stat(filepath.Join(dir, "content-install-identities.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid candidate created a sidecar: %v", err)
	}
}

func TestSkillsShAPIOnlyExposesIdentityAfterCurrentCanonicalProof(t *testing.T) {
	s := newTestServer(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	agents := filepath.Join(home, ".agents")
	root := filepath.Join(agents, "skills")
	skillDir := filepath.Join(root, "lint-review")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSkill := func(name string) {
		t.Helper()
		body := "---\nname: " + name + "\ndescription: Review code\n---\n"
		if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeLock := func(source string) {
		t.Helper()
		lock := `{"skills":{"lint-review":{"source":"` + source + `","sourceUrl":"https://github.com/` + source + `.git"}}}`
		if err := os.WriteFile(filepath.Join(agents, ".skill-lock.json"), []byte(lock), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeSkill("lint-review")
	writeLock("acme/tools")
	s.mu.Lock()
	s.cfg.DirectorySources = []config.DirectorySource{{Path: root}}
	s.mu.Unlock()

	type apiItem struct {
		FrontmatterName string                  `json:"frontmatterName"`
		IdentityStatus  string                  `json:"identityStatus"`
		ContentIdentity *contentInstallIdentity `json:"contentIdentity"`
	}
	list := func() apiItem {
		t.Helper()
		w := httptest.NewRecorder()
		s.handleListSkillsSh(w, req("GET", "/api/skillssh", s.token, nil))
		if w.Code != 200 {
			t.Fatalf("GET /api/skillssh: got %d, body=%s", w.Code, w.Body.String())
		}
		var items []apiItem
		if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil {
			t.Fatal(err)
		}
		if len(items) != 1 {
			t.Fatalf("GET /api/skillssh returned %d items, want 1: %s", len(items), w.Body.String())
		}
		return items[0]
	}

	if got := list(); got.IdentityStatus != "source_same_pending" || got.ContentIdentity != nil || got.FrontmatterName != "lint-review" {
		t.Fatalf("legacy listing exposed identity prematurely: %+v", got)
	}

	canonicalDir, err := filepath.EvalSymlinks(skillDir)
	if err != nil {
		t.Fatal(err)
	}
	record := contentInstallIdentity{
		RepositoryID: 42,
		Path:         "skills/lint-review/SKILL.md",
		CanonicalDir: canonicalDir,
		SourceRepo:   "acme/tools",
		Name:         "lint-review",
	}
	if err := writeContentIdentities(s.centralDir, []contentInstallIdentity{record}); err != nil {
		t.Fatal(err)
	}
	if got := list(); got.IdentityStatus != "exact" || got.ContentIdentity == nil || got.ContentIdentity.RepositoryID != record.RepositoryID || got.ContentIdentity.Path != record.Path {
		t.Fatalf("exact listing did not expose matched identity: %+v", got)
	}

	writeLock("other/repo")
	if got := list(); got.IdentityStatus == "exact" || got.ContentIdentity != nil {
		t.Fatalf("wrong current lock source still exposed exact identity: %+v", got)
	}
	writeLock("acme/tools")
	writeSkill("different-frontmatter-name")
	if got := list(); got.IdentityStatus == "exact" || got.ContentIdentity != nil {
		t.Fatalf("changed frontmatter still exposed exact identity: %+v", got)
	}

	if strings.Contains(string(mustReadFile(t, filepath.Join(agents, ".skill-lock.json"))), "repositoryId") {
		t.Fatal("identity metadata leaked into the third-party lockfile")
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
