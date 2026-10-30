package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"skillmanage/internal/config"
	"skillmanage/internal/linker"
	"skillmanage/internal/source"
)

type contentInstallTestRunner struct {
	fakeRunner
	mu        sync.Mutex
	calls     int
	repoURL   string
	skill     string
	onInstall func(repoURL, skill string)
}

func (f *contentInstallTestRunner) SkillsAddURL(_ context.Context, _ string, repoURL, skill string) (string, string, error) {
	f.mu.Lock()
	f.calls++
	f.repoURL, f.skill = repoURL, skill
	onInstall := f.onInstall
	f.mu.Unlock()
	if onInstall != nil {
		onInstall(repoURL, skill)
	}
	return "✓ " + skill + " (copied)\nInstalled 1 skill", "", nil
}

func (f *contentInstallTestRunner) snapshot() (int, string, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.repoURL, f.skill
}

func contentInstallRequestBody() map[string]any {
	return map[string]any{"contentIdentity": map[string]any{
		"entryId": "project_42", "repositoryId": 42,
		"path": "skills/review/SKILL.md", "name": "review-helper",
	}}
}

func contentInstallEntry(refs ...map[string]any) map[string]any {
	return map[string]any{"entry": map[string]any{
		"id": "project_42", "kind": "project",
		"project": map[string]any{
			"type": "skill", "repositoryId": 42, "repo": "acme/tools", "skills": refs,
		},
	}}
}

func contentInstallRef(path string, overrides map[string]any) map[string]any {
	ref := map[string]any{"repositoryId": 42, "repo": "acme/tools", "path": path, "name": "review-helper", "verified": true}
	for key, value := range overrides {
		ref[key] = value
	}
	return ref
}

func contentInstallTestServer(t *testing.T, handler http.HandlerFunc, runner *contentInstallTestRunner) (*Server, *httptest.Server) {
	t.Helper()
	s := newTestServer(t)
	service := httptest.NewServer(handler)
	baseURL, err := url.Parse(service.URL)
	if err != nil {
		t.Fatal(err)
	}
	s.contentBaseURL = baseURL
	s.contentHTTPClient = service.Client()
	s.npxPath = "/fake/npx"
	s.runner = runner
	return s, service
}

func TestContentInstallRefetchFailurePreventsCLI(t *testing.T) {
	s := newTestServer(t)
	runner := &fakeRunner{}
	s.npxPath = "/fake/npx"
	s.runner = runner

	serviceCalls := 0
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serviceCalls++
		if r.Method != http.MethodGet || r.URL.Path != "/v1/entries/project_42" {
			t.Errorf("unexpected authoritative lookup: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer service.Close()
	s.contentBaseURL, _ = url.Parse(service.URL)
	s.contentHTTPClient = service.Client()

	body := map[string]any{"contentIdentity": map[string]any{
		"entryId": "project_42", "repositoryId": 42,
		"path": "skills/review/SKILL.md", "name": "review-helper",
	}}
	w := httptest.NewRecorder()
	s.handleSkillsShAdd(w, req("POST", "/api/skillssh/add", s.token, body))

	if w.Code != http.StatusBadGateway {
		t.Fatalf("authoritative service outage: got %d body=%s, want 502", w.Code, w.Body.String())
	}
	if serviceCalls != 1 {
		t.Fatalf("authoritative lookup calls=%d, want 1", serviceCalls)
	}
	if runner.calls != 0 {
		t.Fatalf("runner calls=%d, want 0 after authoritative lookup failure", runner.calls)
	}
}

func TestContentInstallUsesAuthoritativeVerifiedSkill(t *testing.T) {
	var lookedUp string
	runner := &contentInstallTestRunner{}
	s, service := contentInstallTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		lookedUp = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(contentInstallEntry(contentInstallRef("skills/review/SKILL.md", nil)))
	}, runner)
	defer service.Close()

	w := httptest.NewRecorder()
	s.handleSkillsShAdd(w, req("POST", "/api/skillssh/add", s.token, contentInstallRequestBody()))
	if w.Code != http.StatusOK {
		t.Fatalf("install request: got %d body=%s", w.Code, w.Body.String())
	}
	if lookedUp != "/v1/entries/project_42" {
		t.Fatalf("service lookup path=%q", lookedUp)
	}
	calls, repoURL, skill := runner.snapshot()
	if calls != 1 || repoURL != "https://github.com/acme/tools" || skill != "review-helper" {
		t.Fatalf("runner got calls=%d repo=%q skill=%q", calls, repoURL, skill)
	}
	if !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("successful authoritative install response=%s", w.Body.String())
	}
}

func TestContentInstallRejectsUntrustedBrowserFields(t *testing.T) {
	serviceCalls := 0
	runner := &contentInstallTestRunner{}
	s, service := contentInstallTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		serviceCalls++
		_ = json.NewEncoder(w).Encode(contentInstallEntry(contentInstallRef("skills/review/SKILL.md", nil)))
	}, runner)
	defer service.Close()
	body := contentInstallRequestBody()
	identity := body["contentIdentity"].(map[string]any)
	identity["verified"] = true
	identity["repoUrl"] = "https://attacker.invalid/other/repo"

	w := httptest.NewRecorder()
	s.handleSkillsShAdd(w, req("POST", "/api/skillssh/add", s.token, body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("untrusted content fields: got %d body=%s, want 400", w.Code, w.Body.String())
	}
	if serviceCalls != 0 {
		t.Fatalf("service calls=%d, want 0 for malformed identity", serviceCalls)
	}
	if calls, _, _ := runner.snapshot(); calls != 0 {
		t.Fatalf("runner calls=%d, want 0 for malformed identity", calls)
	}
}

func TestContentInstallRejectsUnverifiedAmbiguousAndNonSkillRefs(t *testing.T) {
	cases := []struct {
		name  string
		entry map[string]any
	}{
		{name: "unverified", entry: contentInstallEntry(contentInstallRef("skills/review/SKILL.md", map[string]any{"verified": false}))},
		{name: "duplicate name", entry: contentInstallEntry(contentInstallRef("skills/review/SKILL.md", nil), contentInstallRef("old/review/SKILL.md", nil))},
		{name: "path mismatch", entry: contentInstallEntry(contentInstallRef("different/review/SKILL.md", nil))},
		{name: "agent project", entry: map[string]any{"entry": map[string]any{"id": "project_42", "kind": "project", "project": map[string]any{"type": "agent", "repositoryId": 42, "repo": "acme/tools", "skills": []any{contentInstallRef("skills/review/SKILL.md", nil)}}}}},
		{name: "wrong entry", entry: map[string]any{"entry": map[string]any{"id": "another_entry", "kind": "project", "project": map[string]any{"type": "skill", "repositoryId": 42, "repo": "acme/tools", "skills": []any{contentInstallRef("skills/review/SKILL.md", nil)}}}}},
		{name: "wrong repository ID", entry: map[string]any{"entry": map[string]any{"id": "project_42", "kind": "project", "project": map[string]any{"type": "skill", "repositoryId": 43, "repo": "acme/tools", "skills": []any{contentInstallRef("skills/review/SKILL.md", map[string]any{"repositoryId": 43})}}}}},
		{name: "wrong entry kind", entry: map[string]any{"entry": map[string]any{"id": "project_42", "kind": "news", "project": map[string]any{"type": "skill", "repositoryId": 42, "repo": "acme/tools", "skills": []any{contentInstallRef("skills/review/SKILL.md", nil)}}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := &contentInstallTestRunner{}
			s, service := contentInstallTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(tc.entry)
			}, runner)
			defer service.Close()
			w := httptest.NewRecorder()
			s.handleSkillsShAdd(w, req("POST", "/api/skillssh/add", s.token, contentInstallRequestBody()))
			if w.Code == http.StatusOK {
				t.Fatalf("invalid authoritative identity accepted: %s", w.Body.String())
			}
			if calls, _, _ := runner.snapshot(); calls != 0 {
				t.Fatalf("runner calls=%d for rejected identity", calls)
			}
		})
	}
}

func TestContentInstallRejectsLegacyOverridesBeforeServiceLookup(t *testing.T) {
	serviceCalls := 0
	runner := &contentInstallTestRunner{}
	s, service := contentInstallTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		serviceCalls++
		_ = json.NewEncoder(w).Encode(contentInstallEntry(contentInstallRef("skills/review/SKILL.md", nil)))
	}, runner)
	defer service.Close()
	body := contentInstallRequestBody()
	body["pkg"] = "attacker/repo@skill"
	body["url"] = "https://attacker.invalid/repo"
	body["skill"] = "review-helper"
	w := httptest.NewRecorder()
	s.handleSkillsShAdd(w, req("POST", "/api/skillssh/add", s.token, body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("mixed content identity overrides: got %d body=%s", w.Code, w.Body.String())
	}
	if serviceCalls != 0 {
		t.Fatalf("service calls=%d, want 0 for mixed identity override", serviceCalls)
	}
	if calls, _, _ := runner.snapshot(); calls != 0 {
		t.Fatalf("runner calls=%d, want 0 for mixed identity override", calls)
	}
}

func TestExistingContentInstallStateClassifiesCanonicalLocalMatches(t *testing.T) {
	root := t.TempDir()
	writeSkillFixture(t, root, "review-helper", "review-helper")
	skillDir, err := filepath.EvalSymlinks(filepath.Join(root, "review-helper"))
	if err != nil {
		t.Fatal(err)
	}
	ref := resolvedContentSkill{Ref: contentSkillRef{
		RepositoryID: 42, Repo: "acme/tools", Path: "skills/review/SKILL.md", Name: "review-helper", Verified: true,
	}}
	lock := source.SkillLock{Skills: map[string]source.LockEntry{
		"review-helper": {Source: "acme/tools"},
	}}
	record := contentInstallIdentity{
		RepositoryID: 42, Path: "skills/review/SKILL.md", CanonicalDir: skillDir,
		SourceRepo: "acme/tools", Name: "review-helper",
	}

	cases := []struct {
		name    string
		lock    source.SkillLock
		records []contentInstallIdentity
		want    string
	}{
		{name: "exact", lock: lock, records: []contentInstallIdentity{record}, want: "exact"},
		{name: "same source legacy pending", lock: lock, want: "source_same_pending"},
		{name: "different source conflict", lock: source.SkillLock{Skills: map[string]source.LockEntry{"review-helper": {Source: "other/repo"}}}, want: "conflict"},
		{name: "missing lock conflict", lock: source.SkillLock{Skills: map[string]source.LockEntry{}}, want: "conflict"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := existingContentInstallState(root, tc.lock, tc.records, ref)
			if err != nil {
				t.Fatalf("state error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("state=%q, want %q", got, tc.want)
			}
		})
	}

	writeSkillFixture(t, root, "legacy-copy", "review-helper")
	if got, err := existingContentInstallState(root, lock, nil, ref); err != nil || got != "conflict" {
		t.Fatalf("multiple name matches = (%q, %v), want conflict", got, err)
	}
}

func TestContentInstallPreflightUsesCanonicalHomeNotForeignDirectorySource(t *testing.T) {
	home := t.TempDir()
	globalRoot := filepath.Join(home, ".agents", "skills")
	writeSkillFixture(t, globalRoot, "review-helper", "review-helper")
	writeSkillLockFixture(t, filepath.Join(home, ".agents"), "review-helper", "other/repo")

	foreignRoot := filepath.Join(t.TempDir(), ".agents", "skills")
	runner := &contentInstallTestRunner{}
	s, service := contentInstallTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(contentInstallEntry(contentInstallRef("skills/review/SKILL.md", nil)))
	}, runner)
	defer service.Close()
	s.contentInstallHomeDir = func() (string, error) { return home, nil }
	s.mu.Lock()
	s.cfg.DirectorySources = []config.DirectorySource{{Path: foreignRoot}}
	s.mu.Unlock()

	w := httptest.NewRecorder()
	s.handleSkillsShAdd(w, req("POST", "/api/skillssh/add", s.token, contentInstallRequestBody()))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"identityStatus":"conflict"`) {
		t.Fatalf("canonical destination conflict: got %d body=%s", w.Code, w.Body.String())
	}
	if calls, _, _ := runner.snapshot(); calls != 0 {
		t.Fatalf("runner calls=%d, want 0 for canonical collision", calls)
	}
}

func TestContentInstallPostflightStoresCanonicalProofThroughRootSymlink(t *testing.T) {
	home := t.TempDir()
	agentsDir := filepath.Join(home, ".agents")
	canonicalRoot := filepath.Join(t.TempDir(), "skills-store")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(canonicalRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(canonicalRoot, filepath.Join(agentsDir, "skills")); err != nil {
		t.Fatal(err)
	}
	runner := &contentInstallTestRunner{}
	runner.onInstall = func(_, _ string) {
		writeSkillFixture(t, filepath.Join(agentsDir, "skills"), "review-helper", "review-helper")
		writeSkillLockFixture(t, agentsDir, "review-helper", "acme/tools")
	}
	s, service := contentInstallTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(contentInstallEntry(contentInstallRef("skills/review/SKILL.md", nil)))
	}, runner)
	defer service.Close()
	s.contentInstallHomeDir = func() (string, error) { return home, nil }

	w := httptest.NewRecorder()
	s.handleSkillsShAdd(w, req("POST", "/api/skillssh/add", s.token, contentInstallRequestBody()))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"identityStatus":"exact"`) {
		t.Fatalf("verified install: got %d body=%s", w.Code, w.Body.String())
	}
	calls, repoURL, skill := runner.snapshot()
	if calls != 1 || repoURL != "https://github.com/acme/tools" || skill != "review-helper" {
		t.Fatalf("runner got calls=%d repo=%q skill=%q", calls, repoURL, skill)
	}
	records, err := readContentIdentities(s.centralDir)
	if err != nil || len(records) != 1 {
		t.Fatalf("provenance records=%+v err=%v", records, err)
	}
	actualDir, err := filepath.EvalSymlinks(filepath.Join(canonicalRoot, "review-helper"))
	if err != nil {
		t.Fatal(err)
	}
	want := contentInstallIdentity{RepositoryID: 42, Path: "skills/review/SKILL.md", CanonicalDir: actualDir, SourceRepo: "acme/tools", Name: "review-helper"}
	if !sameContentIdentity(records[0], want) {
		t.Fatalf("record=%+v want=%+v", records[0], want)
	}
}

func TestContentInstallRootSymlinkFlowsThroughLiveListingAndEnableApply(t *testing.T) {
	home := t.TempDir()
	agentsDir := filepath.Join(home, ".agents")
	canonicalRoot := filepath.Join(t.TempDir(), "skills-store")
	target := filepath.Join(t.TempDir(), ".claude", "skills")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(canonicalRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(canonicalRoot, filepath.Join(agentsDir, "skills")); err != nil {
		t.Fatal(err)
	}
	runner := &contentInstallTestRunner{}
	runner.onInstall = func(_, _ string) {
		writeSkillFixture(t, filepath.Join(agentsDir, "skills"), "review-helper", "review-helper")
		writeSkillLockFixture(t, agentsDir, "review-helper", "acme/tools")
	}
	s, service := contentInstallTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(contentInstallEntry(contentInstallRef("skills/review/SKILL.md", nil)))
	}, runner)
	defer service.Close()
	s.contentInstallHomeDir = func() (string, error) { return home, nil }
	lexicalRoot := filepath.Join(agentsDir, "skills")
	s.mu.Lock()
	s.cfg.DirectorySources = []config.DirectorySource{{Path: lexicalRoot}}
	s.cfg.Targets = []string{target}
	s.mu.Unlock()

	handler := s.Handler()
	install := httptest.NewRecorder()
	handler.ServeHTTP(install, req("POST", "/api/skillssh/add", s.token, contentInstallRequestBody()))
	if install.Code != http.StatusOK || !strings.Contains(install.Body.String(), `"identityStatus":"exact"`) {
		t.Fatalf("verified install: got %d body=%s", install.Code, install.Body.String())
	}

	listing := httptest.NewRecorder()
	handler.ServeHTTP(listing, req("GET", "/api/skillssh", s.token, nil))
	if listing.Code != http.StatusOK {
		t.Fatalf("live skills.sh listing: got %d body=%s", listing.Code, listing.Body.String())
	}
	var listed []map[string]any
	if err := json.Unmarshal(listing.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode live listing: %v body=%s", err, listing.Body.String())
	}
	if len(listed) != 1 || listed[0]["identityStatus"] != "exact" || listed[0]["linkName"] != "review-helper" {
		t.Fatalf("live listing must expose the exact installed identity through root symlink: %+v", listed)
	}

	enable := httptest.NewRecorder()
	handler.ServeHTTP(enable, req("POST", "/api/enabled", s.token, map[string]string{
		"skill": "@agents/review-helper", "target": target, "mode": "snapshot",
	}))
	if enable.Code != http.StatusCreated && enable.Code != http.StatusOK {
		t.Fatalf("enable selection: got %d body=%s", enable.Code, enable.Body.String())
	}
	apply := httptest.NewRecorder()
	handler.ServeHTTP(apply, req("POST", "/api/apply", s.token, nil))
	if apply.Code != http.StatusOK {
		t.Fatalf("apply: got %d body=%s", apply.Code, apply.Body.String())
	}
	var summary map[string]any
	if err := json.Unmarshal(apply.Body.Bytes(), &summary); err != nil {
		t.Fatalf("decode apply summary: %v", err)
	}
	if got := summary["errors"]; got != nil && len(got.([]any)) != 0 {
		t.Fatalf("apply errors through root symlink: %+v", got)
	}
	materialized := filepath.Join(target, "review-helper")
	actualTarget, err := filepath.EvalSymlinks(materialized)
	if err != nil {
		t.Fatalf("enabled @agents skill was not materialized: %v", err)
	}
	actualCanonical, err := filepath.EvalSymlinks(filepath.Join(canonicalRoot, "review-helper"))
	if err != nil {
		t.Fatal(err)
	}
	if actualTarget != actualCanonical {
		t.Fatalf("materialized target resolves to %q, want actual skills root %q", actualTarget, actualCanonical)
	}

	inventory := httptest.NewRecorder()
	handler.ServeHTTP(inventory, req("GET", "/api/inventory?target="+url.QueryEscape(target), s.token, nil))
	if inventory.Code != http.StatusOK {
		t.Fatalf("inventory: got %d body=%s", inventory.Code, inventory.Body.String())
	}
	var inventoryBody inventoryResp
	if err := json.Unmarshal(inventory.Body.Bytes(), &inventoryBody); err != nil {
		t.Fatalf("decode inventory: %v body=%s", err, inventory.Body.String())
	}
	if len(inventoryBody.Items) != 1 || inventoryBody.Items[0].Selector != "@agents/review-helper" || inventoryBody.Items[0].LinkTarget != actualCanonical || !inventoryBody.Items[0].Managed || !inventoryBody.Items[0].Enabled {
		t.Fatalf("inventory must classify the real @agents link as enabled: %+v", inventoryBody.Items)
	}
}

func TestContentInventoryCanonicalProofRequiresCompatibleCurrentPrimitive(t *testing.T) {
	cases := []struct {
		name      string
		linkType  config.LinkType
		primitive string
		mismatch  bool
		wantProof bool
	}{
		{name: "stale symlink replaced by unrelated directory", linkType: config.LinkSymlink, primitive: "directory"},
		{name: "stale junction replaced by unrelated directory", linkType: config.LinkJunction, primitive: "directory"},
		{name: "matching managed link", primitive: "managed-link", wantProof: true},
		{name: "matching copy fallback", linkType: config.LinkCopy, primitive: "copy", wantProof: true},
		{name: "stale copy with different content", linkType: config.LinkCopy, primitive: "copy", mismatch: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(t)
			agentsDir := filepath.Join(t.TempDir(), ".agents")
			sourceRoot := filepath.Join(agentsDir, "skills")
			target := filepath.Join(t.TempDir(), "target")
			writeSkillFixture(t, sourceRoot, "review-helper", "review-helper")
			writeSkillLockFixture(t, agentsDir, "review-helper", "acme/tools")
			canonical, err := filepath.EvalSymlinks(filepath.Join(sourceRoot, "review-helper"))
			if err != nil {
				t.Fatal(err)
			}
			materialized := filepath.Join(target, "review-helper")
			linkType := tc.linkType
			switch tc.primitive {
			case "directory":
				name := "review-helper"
				if tc.name == "stale symlink replaced by unrelated directory" || tc.name == "stale junction replaced by unrelated directory" {
					name = "impostor"
				}
				writeSkillFixture(t, target, "review-helper", name)
			case "managed-link":
				manifest := config.Manifest{}
				manager := linker.NewManager("", "")
				if _, err := manager.Link(linker.DesiredLink{LinkName: "review-helper", Target: target, Source: canonical}, &manifest); err != nil {
					t.Fatal(err)
				}
				linkType = manifest.Links[0].LinkType
			case "copy":
				if err := linker.CopyTree(canonical, materialized); err != nil {
					t.Fatal(err)
				}
				if tc.mismatch {
					writeSkillFixture(t, target, "review-helper", "impostor")
				}
			}

			s.mu.Lock()
			s.cfg.DirectorySources = []config.DirectorySource{{Path: sourceRoot}}
			s.cfg.Targets = []string{target}
			s.manifest.Links = []config.LinkRecord{{Name: "review-helper", Target: target, Source: canonical, LinkType: linkType}}
			s.mu.Unlock()
			w := httptest.NewRecorder()
			s.handleInventory(w, req("GET", "/api/inventory?target="+url.QueryEscape(target), s.token, nil))
			if w.Code != http.StatusOK {
				t.Fatalf("inventory: got %d body=%s", w.Code, w.Body.String())
			}
			var body inventoryResp
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode inventory: %v body=%s", err, w.Body.String())
			}
			if len(body.Items) != 1 || body.Items[0].Name != "review-helper" {
				t.Fatalf("inventory item missing: %+v", body.Items)
			}
			gotProof := body.Items[0].LinkTarget == canonical
			if gotProof != tc.wantProof {
				t.Fatalf("canonical proof=%v target=%q, want proof=%v; item=%+v", gotProof, body.Items[0].LinkTarget, tc.wantProof, body.Items[0])
			}
		})
	}
}

func TestContentInstallExactIdentityReturnsWithoutCLI(t *testing.T) {
	home := t.TempDir()
	agentsDir := filepath.Join(home, ".agents")
	root := filepath.Join(agentsDir, "skills")
	writeSkillFixture(t, root, "review-helper", "review-helper")
	writeSkillLockFixture(t, agentsDir, "review-helper", "acme/tools")
	canonicalDir, err := filepath.EvalSymlinks(filepath.Join(root, "review-helper"))
	if err != nil {
		t.Fatal(err)
	}
	runner := &contentInstallTestRunner{}
	s, service := contentInstallTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(contentInstallEntry(contentInstallRef("skills/review/SKILL.md", nil)))
	}, runner)
	defer service.Close()
	s.contentInstallHomeDir = func() (string, error) { return home, nil }
	if err := writeContentIdentities(s.centralDir, []contentInstallIdentity{{
		RepositoryID: 42, Path: "skills/review/SKILL.md", CanonicalDir: canonicalDir, SourceRepo: "acme/tools", Name: "review-helper",
	}}); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	s.handleSkillsShAdd(w, req("POST", "/api/skillssh/add", s.token, contentInstallRequestBody()))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"already-installed"`) || !strings.Contains(w.Body.String(), `"identityStatus":"exact"`) {
		t.Fatalf("exact identity result: got %d body=%s", w.Code, w.Body.String())
	}
	if calls, _, _ := runner.snapshot(); calls != 0 {
		t.Fatalf("runner calls=%d, want 0 for exact identity", calls)
	}
}

func TestContentInstallPendingAndMalformedSidecarBlockCLI(t *testing.T) {
	cases := []struct {
		name      string
		setup     func(t *testing.T, home, centralDir string) []byte
		wantState string
	}{
		{
			name: "same source legacy pending",
			setup: func(t *testing.T, home, _ string) []byte {
				root := filepath.Join(home, ".agents", "skills")
				writeSkillFixture(t, root, "review-helper", "review-helper")
				writeSkillLockFixture(t, filepath.Join(home, ".agents"), "review-helper", "acme/tools")
				return nil
			},
			wantState: "source_same_pending",
		},
		{
			name: "malformed sidecar",
			setup: func(t *testing.T, _, centralDir string) []byte {
				before := []byte("{malformed")
				if err := os.WriteFile(filepath.Join(centralDir, contentIdentityFilename), before, 0o600); err != nil {
					t.Fatal(err)
				}
				return before
			},
			wantState: "conflict",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			runner := &contentInstallTestRunner{}
			s, service := contentInstallTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(contentInstallEntry(contentInstallRef("skills/review/SKILL.md", nil)))
			}, runner)
			defer service.Close()
			s.contentInstallHomeDir = func() (string, error) { return home, nil }
			before := tc.setup(t, home, s.centralDir)
			w := httptest.NewRecorder()
			s.handleSkillsShAdd(w, req("POST", "/api/skillssh/add", s.token, contentInstallRequestBody()))
			if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"identityStatus":"`+tc.wantState+`"`) {
				t.Fatalf("preflight result: got %d body=%s", w.Code, w.Body.String())
			}
			if calls, _, _ := runner.snapshot(); calls != 0 {
				t.Fatalf("runner calls=%d, want 0", calls)
			}
			if before != nil {
				after, err := os.ReadFile(filepath.Join(s.centralDir, contentIdentityFilename))
				if err != nil || !bytes.Equal(before, after) {
					t.Fatalf("malformed sidecar changed: got=%q err=%v", after, err)
				}
			}
		})
	}
}

func TestContentInstallPostflightMismatchStaysPendingWithoutProof(t *testing.T) {
	cases := []struct {
		name      string
		install   func(t *testing.T, agentsDir string)
		wantState string
	}{
		{
			name: "wrong lock source",
			install: func(t *testing.T, agentsDir string) {
				writeSkillFixture(t, filepath.Join(agentsDir, "skills"), "review-helper", "review-helper")
				writeSkillLockFixture(t, agentsDir, "review-helper", "wrong/repo")
			},
			wantState: "not_match",
		},
		{
			name:      "missing installed name",
			install:   func(t *testing.T, _ string) {},
			wantState: "not_match",
		},
		{
			name: "duplicate installed names",
			install: func(t *testing.T, agentsDir string) {
				root := filepath.Join(agentsDir, "skills")
				writeSkillFixture(t, root, "review-helper", "review-helper")
				writeSkillFixture(t, root, "review-helper-copy", "review-helper")
				writeSkillLockFixture(t, agentsDir, "review-helper", "acme/tools")
			},
			wantState: "not_match",
		},
		{
			name: "canonical root changed during CLI",
			install: func(t *testing.T, agentsDir string) {
				root := filepath.Join(agentsDir, "skills")
				if err := os.MkdirAll(agentsDir, 0o755); err != nil {
					t.Fatal(err)
				}
				otherRoot := filepath.Join(t.TempDir(), "skills")
				if err := os.MkdirAll(otherRoot, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(otherRoot, root); err != nil {
					t.Fatal(err)
				}
				writeSkillFixture(t, root, "review-helper", "review-helper")
				writeSkillLockFixture(t, agentsDir, "review-helper", "acme/tools")
			},
			wantState: "conflict",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			agentsDir := filepath.Join(home, ".agents")
			runner := &contentInstallTestRunner{}
			runner.onInstall = func(_, _ string) { tc.install(t, agentsDir) }
			s, service := contentInstallTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(contentInstallEntry(contentInstallRef("skills/review/SKILL.md", nil)))
			}, runner)
			defer service.Close()
			s.contentInstallHomeDir = func() (string, error) { return home, nil }
			w := httptest.NewRecorder()
			s.handleSkillsShAdd(w, req("POST", "/api/skillssh/add", s.token, contentInstallRequestBody()))
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"installed_pending"`) || !strings.Contains(w.Body.String(), `"identityStatus":"`+tc.wantState+`"`) {
				t.Fatalf("postflight mismatch: got %d body=%s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), `"identityStatus":"exact"`) {
				t.Fatalf("mismatched install was claimed exact: %s", w.Body.String())
			}
			if _, err := os.Stat(filepath.Join(s.centralDir, contentIdentityFilename)); !os.IsNotExist(err) {
				t.Fatalf("mismatched install wrote provenance, stat error=%v", err)
			}
		})
	}
}

func TestContentInstallConcurrentRequestsAreIdempotent(t *testing.T) {
	home := t.TempDir()
	agentsDir := filepath.Join(home, ".agents")
	runner := &contentInstallTestRunner{}
	runner.onInstall = func(_, _ string) {
		writeSkillFixture(t, filepath.Join(agentsDir, "skills"), "review-helper", "review-helper")
		writeSkillLockFixture(t, agentsDir, "review-helper", "acme/tools")
	}
	s, service := contentInstallTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(contentInstallEntry(contentInstallRef("skills/review/SKILL.md", nil)))
	}, runner)
	defer service.Close()
	s.contentInstallHomeDir = func() (string, error) { return home, nil }

	responses := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() {
			w := httptest.NewRecorder()
			s.handleSkillsShAdd(w, req("POST", "/api/skillssh/add", s.token, contentInstallRequestBody()))
			responses <- w
		}()
	}
	for range 2 {
		w := <-responses
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"identityStatus":"exact"`) {
			t.Fatalf("concurrent install response: got %d body=%s", w.Code, w.Body.String())
		}
	}
	if calls, _, _ := runner.snapshot(); calls != 1 {
		t.Fatalf("concurrent CLI calls=%d, want 1", calls)
	}
}

func TestContentInstallRejectsForeignAgentsSourceBeforeItCanBeShownExact(t *testing.T) {
	home := t.TempDir()
	foreignRoot := filepath.Join(t.TempDir(), ".agents", "skills")
	writeSkillFixture(t, foreignRoot, "review-helper", "review-helper")
	writeSkillLockFixture(t, filepath.Dir(foreignRoot), "review-helper", "acme/tools")
	agentsDir := filepath.Join(home, ".agents")
	runner := &contentInstallTestRunner{}
	runner.onInstall = func(_, _ string) {
		writeSkillFixture(t, filepath.Join(agentsDir, "skills"), "review-helper", "review-helper")
		writeSkillLockFixture(t, agentsDir, "review-helper", "acme/tools")
	}
	s, service := contentInstallTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(contentInstallEntry(contentInstallRef("skills/review/SKILL.md", nil)))
	}, runner)
	defer service.Close()
	s.contentInstallHomeDir = func() (string, error) { return home, nil }
	s.mu.Lock()
	s.cfg.DirectorySources = []config.DirectorySource{{Path: foreignRoot}}
	s.mu.Unlock()

	w := httptest.NewRecorder()
	s.handleSkillsShAdd(w, req("POST", "/api/skillssh/add", s.token, contentInstallRequestBody()))
	if w.Code != http.StatusConflict || strings.Contains(w.Body.String(), `"identityStatus":"exact"`) {
		t.Fatalf("foreign @agents mapping must fail closed: got %d body=%s", w.Code, w.Body.String())
	}
	if calls, _, _ := runner.snapshot(); calls != 0 {
		t.Fatalf("runner calls=%d, want 0 when /api/skillssh resolves another physical root", calls)
	}
}

func writeSkillFixture(t *testing.T, root, dirName, frontmatterName string) {
	t.Helper()
	dir := filepath.Join(root, dirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + frontmatterName + "\ndescription: test\n---\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeSkillLockFixture(t *testing.T, agentsDir, name, source string) {
	t.Helper()
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{"skills": map[string]any{name: map[string]string{"source": source}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, ".skill-lock.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
}

type contentRemoveTestRunner struct {
	*contentInstallTestRunner
	onRemove func(string) (string, string, error)
}

func (f *contentRemoveTestRunner) SkillsRemove(_ context.Context, _, name string) (string, string, error) {
	return f.onRemove(name)
}

func contentRemovalFixture(t *testing.T) (*Server, *contentRemoveTestRunner, string) {
	t.Helper()
	home := t.TempDir()
	agentsDir := filepath.Join(home, ".agents")
	if err := os.MkdirAll(filepath.Join(agentsDir, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &contentInstallTestRunner{}
	runner.onInstall = func(_, _ string) {
		writeSkillFixture(t, filepath.Join(agentsDir, "skills"), "review-helper", "review-helper")
		writeSkillLockFixture(t, agentsDir, "review-helper", "acme/tools")
	}
	s, service := contentInstallTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(contentInstallEntry(contentInstallRef("skills/review/SKILL.md", nil)))
	}, runner)
	t.Cleanup(service.Close)
	s.contentInstallHomeDir = func() (string, error) { return home, nil }
	s.cfg.DirectorySources = []config.DirectorySource{{Path: filepath.Join(agentsDir, "skills")}}
	removeRunner := &contentRemoveTestRunner{contentInstallTestRunner: runner}
	removeRunner.onRemove = func(name string) (string, string, error) {
		if err := os.RemoveAll(filepath.Join(agentsDir, "skills", name)); err != nil {
			return "", "", err
		}
		if err := os.WriteFile(filepath.Join(agentsDir, ".skill-lock.json"), []byte(`{"skills":{}}`), 0o600); err != nil {
			return "", "", err
		}
		return "Removed " + name, "", nil
	}
	s.runner = removeRunner
	w := httptest.NewRecorder()
	s.handleSkillsShAdd(w, req("POST", "/api/skillssh/add", s.token, contentInstallRequestBody()))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"identityStatus":"exact"`) {
		t.Fatalf("fixture install: %d %s", w.Code, w.Body.String())
	}
	return s, removeRunner, agentsDir
}

func removeContentFixture(s *Server) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.handleSkillsShRemove(w, req("POST", "/api/skillssh/remove", s.token, map[string]string{"name": "review-helper"}))
	return w
}

func TestContentRemovalRetiresOnlyVerifiedIdentityAndAllowsReinstall(t *testing.T) {
	s, runner, _ := contentRemovalFixture(t)
	other := contentInstallIdentity{RepositoryID: 99, Path: "other/SKILL.md", CanonicalDir: filepath.Join(t.TempDir(), "other"), SourceRepo: "other/repo", Name: "other"}
	recordsBefore, err := readContentIdentities(s.centralDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeContentIdentities(s.centralDir, append(recordsBefore, other)); err != nil {
		t.Fatal(err)
	}
	w := removeContentFixture(s)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("remove: %d %s", w.Code, w.Body.String())
	}
	records, err := readContentIdentities(s.centralDir)
	if err != nil || len(records) != 1 || !sameContentIdentity(records[0], other) {
		t.Fatalf("verified removal must retire only exact provenance and preserve other identities: %+v %v", records, err)
	}
	w = httptest.NewRecorder()
	s.handleSkillsShAdd(w, req("POST", "/api/skillssh/add", s.token, contentInstallRequestBody()))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"installed"`) {
		t.Fatalf("reinstall: %d %s", w.Code, w.Body.String())
	}
	if calls, _, _ := runner.snapshot(); calls != 2 {
		t.Fatalf("install calls=%d want 2", calls)
	}
}

func TestContentRemovalUnprovenOutcomesRetainIdentity(t *testing.T) {
	for _, outcome := range []string{"failed", "marker-only", "root-removed", "lock-retained", "symlink-lock"} {
		t.Run(outcome, func(t *testing.T) {
			s, runner, agentsDir := contentRemovalFixture(t)
			runner.onRemove = func(name string) (string, string, error) {
				switch outcome {
				case "failed":
					return "", "failed", nil
				case "root-removed":
					_ = os.RemoveAll(filepath.Join(agentsDir, "skills"))
				case "lock-retained":
					_ = os.RemoveAll(filepath.Join(agentsDir, "skills", name))
				case "symlink-lock":
					_ = os.RemoveAll(filepath.Join(agentsDir, "skills", name))
					_ = os.Remove(filepath.Join(agentsDir, ".skill-lock.json"))
					target := filepath.Join(t.TempDir(), "empty-lock.json")
					_ = os.WriteFile(target, []byte(`{"skills":{}}`), 0o600)
					if err := os.Symlink(target, filepath.Join(agentsDir, ".skill-lock.json")); err != nil {
						t.Fatal(err)
					}
				}
				return "Removed " + name, "", nil
			}
			w := removeContentFixture(s)
			if strings.Contains(w.Body.String(), `"ok":true`) {
				t.Fatalf("unproven %s reported success: %s", outcome, w.Body.String())
			}
			records, err := readContentIdentities(s.centralDir)
			if err != nil || len(records) != 1 {
				t.Fatalf("unproven removal lost provenance: %+v %v", records, err)
			}
		})
	}
}

func TestContentRemovalConflictsPreserveProofWithoutCLI(t *testing.T) {
	for _, conflict := range []string{"malformed", "foreign-source", "ambiguous-name", "missing-canonical"} {
		t.Run(conflict, func(t *testing.T) {
			s, runner, agentsDir := contentRemovalFixture(t)
			before, _ := os.ReadFile(filepath.Join(s.centralDir, contentIdentityFilename))
			switch conflict {
			case "malformed":
				before = []byte(`[{"name":"review-helper"}]`)
				_ = os.WriteFile(filepath.Join(s.centralDir, contentIdentityFilename), before, 0o600)
			case "foreign-source":
				writeSkillLockFixture(t, agentsDir, "review-helper", "other/repo")
			case "ambiguous-name":
				writeSkillFixture(t, filepath.Join(agentsDir, "skills"), "collision", "review-helper")
			case "missing-canonical":
				_ = os.RemoveAll(filepath.Join(agentsDir, "skills", "review-helper"))
			}
			called := false
			runner.onRemove = func(string) (string, string, error) { called = true; return "Removed", "", nil }
			w := removeContentFixture(s)
			if called || w.Code < 400 {
				t.Fatalf("%s invoked CLI or accepted: called=%v %d %s", conflict, called, w.Code, w.Body.String())
			}
			after, _ := os.ReadFile(filepath.Join(s.centralDir, contentIdentityFilename))
			if !bytes.Equal(before, after) {
				t.Fatal("conflict changed identity proof")
			}
		})
	}
}

func TestContentRemovalSerializesWithReinstall(t *testing.T) {
	s, runner, _ := contentRemovalFixture(t)
	started, release := make(chan struct{}), make(chan struct{})
	remove := runner.onRemove
	runner.onRemove = func(name string) (string, string, error) {
		close(started)
		<-release
		return remove(name)
	}
	removed := make(chan *httptest.ResponseRecorder, 1)
	go func() { removed <- removeContentFixture(s) }()
	<-started
	installed := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		s.handleSkillsShAdd(w, req("POST", "/api/skillssh/add", s.token, contentInstallRequestBody()))
		installed <- w
	}()
	// Install must not claim the old proof while the canonical removal is pending.
	select {
	case w := <-installed:
		t.Fatalf("install escaped removal serialization: %d %s", w.Code, w.Body.String())
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	for _, w := range []*httptest.ResponseRecorder{<-removed, <-installed} {
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"ok":true`) {
			t.Fatalf("serialized operation: %d %s", w.Code, w.Body.String())
		}
	}
	records, err := readContentIdentities(s.centralDir)
	if err != nil || len(records) != 1 {
		t.Fatalf("reinstall proof: %+v %v", records, err)
	}
	if calls, _, _ := runner.snapshot(); calls != 2 {
		t.Fatalf("serialized install calls=%d want 2", calls)
	}
}
