package contentgithub

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestSkillFrontmatter(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
		ok   bool
	}{
		{name: "valid frontmatter with CRLF", body: "---\r\nname: release-notes\r\ndescription: Writes release notes\r\n---\r\n# Skill\r\n", want: "release-notes", ok: true},
		{name: "missing opening delimiter", body: "name: release-notes\ndescription: Writes release notes\n", ok: false},
		{name: "missing name", body: "---\ndescription: Writes release notes\n---\n", ok: false},
		{name: "missing description", body: "---\nname: release-notes\n---\n", ok: false},
		{name: "malformed YAML", body: "---\nname: [broken\ndescription: value\n---\n", ok: false},
		{name: "unsafe name", body: "---\nname: ../run\ndescription: unsafe\n---\n", ok: false},
		{name: "dot name", body: "---\nname: .\ndescription: unsafe\n---\n", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ref, err := parseSkillFile(42, "octo/repo", "nested/SKILL.md", []byte(tt.body))
			if tt.ok {
				if err != nil {
					t.Fatalf("parseSkillFile: %v", err)
				}
				if !ref.Verified || ref.Name != tt.want {
					t.Fatalf("skill ref = %+v", ref)
				}
				return
			}
			if err == nil {
				t.Fatalf("parseSkillFile accepted frontmatter and returned %+v", ref)
			}
			if ref.Verified {
				t.Fatalf("invalid frontmatter produced verified ref %+v", ref)
			}
		})
	}
}

func TestSkillFrontmatterRejectsInvalidPathIdentity(t *testing.T) {
	body := []byte("---\nname: valid\ndescription: valid\n---\n")
	for _, path := range []string{"", "/SKILL.md", "./SKILL.md", "../SKILL.md", "skills//SKILL.md", "skills/../SKILL.md", `skills\\SKILL.md`, "skills/skill.md"} {
		t.Run(path, func(t *testing.T) {
			ref, err := parseSkillFile(42, "octo/repo", path, body)
			if err == nil || ref.Verified {
				t.Fatalf("invalid path %q produced ref %+v, err %v", path, ref, err)
			}
		})
	}
}

func TestDecodeSkillBase64IsBounded(t *testing.T) {
	wrapped := base64.StdEncoding.EncodeToString([]byte("frontmatter"))
	wrapped = wrapped[:4] + "\n" + wrapped[4:]
	got, err := decodeSkillBase64(wrapped)
	if err != nil || string(got) != "frontmatter" {
		t.Fatalf("wrapped base64 decode = %q, %v", got, err)
	}
	oversized := base64.StdEncoding.EncodeToString(make([]byte, maxSkillFileBytes+1))
	if _, err := decodeSkillBase64(oversized); err == nil {
		t.Fatal("decodeSkillBase64 accepted more than the 1 MiB limit")
	}
}

func TestDuplicateSkillNames(t *testing.T) {
	server := newSkillInspectionServer(t, "octo/repo", false, map[string]string{
		"a/SKILL.md": "---\nname: Release-Notes\ndescription: First skill\n---\n",
		"b/SKILL.md": "---\nname: release-notes\ndescription: Second skill\n---\n",
	})
	defer server.Close()
	client := NewClient(server.URL, nil, "")
	got, err := client.inspectRepository(context.Background(), testNow, repositoryCandidate{ID: 42, FullName: "octo/repo"})
	if err != nil {
		t.Fatalf("inspectRepository: %v", err)
	}
	if got.Complete {
		t.Fatal("duplicate skill names were treated as complete evidence")
	}
	if len(got.Skills) != 2 {
		t.Fatalf("skill refs = %+v, want two refs", got.Skills)
	}
	for _, ref := range got.Skills {
		if ref.Verified {
			t.Fatalf("duplicate name remained verified: %+v", ref)
		}
	}
}

func TestInspectRepositoryKeepsTruncatedEvidenceUnverified(t *testing.T) {
	server := newSkillInspectionServer(t, "octo/repo", true, map[string]string{
		"nested/SKILL.md": "---\nname: nested\ndescription: A nested skill\n---\n",
	})
	defer server.Close()
	got, err := NewClient(server.URL, nil, "").inspectRepository(context.Background(), testNow, repositoryCandidate{ID: 42, FullName: "octo/repo"})
	if err != nil {
		t.Fatalf("inspectRepository: %v", err)
	}
	if got.Complete || len(got.Skills) != 1 || got.Skills[0].Verified {
		t.Fatalf("truncated tree inspection = %+v", got)
	}
}

func TestInspectRepositoryKeepsInvalidFrontmatterUnverified(t *testing.T) {
	server := newSkillInspectionServer(t, "octo/repo", false, map[string]string{
		"nested/SKILL.md": "---\nname: [broken\ndescription: missing\n---\n",
	})
	defer server.Close()
	got, err := NewClient(server.URL, nil, "").inspectRepository(context.Background(), testNow, repositoryCandidate{ID: 42, FullName: "octo/repo"})
	if err != nil {
		t.Fatalf("inspectRepository: %v", err)
	}
	if got.Complete || len(got.Skills) != 1 || got.Skills[0].Verified {
		t.Fatalf("invalid frontmatter inspection = %+v", got)
	}
}

func TestInspectRepositoryRejectsRepositoryIdentityMismatch(t *testing.T) {
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = fmt.Fprint(w, `{"id":99,"full_name":"octo/repo","html_url":"https://github.com/octo/repo","default_branch":"main"}`)
	}))
	defer server.Close()
	_, err := NewClient(server.URL, nil, "").inspectRepository(context.Background(), testNow, repositoryCandidate{ID: 42, FullName: "octo/repo"})
	if err == nil {
		t.Fatal("inspectRepository accepted metadata with a different repository id")
	}
	if hits != 1 {
		t.Fatalf("metadata mismatch made %d requests, want only the metadata request", hits)
	}
}

func TestInspectRepositoryEscapesBranchAndSkillPath(t *testing.T) {
	requests := make([]string, 0, 3)
	var unexpectedHosts []string
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.RequestURI)
		switch {
		case r.URL.Path == "/repos/octo/repo":
			_, _ = fmt.Fprint(w, `{"id":42,"full_name":"octo/repo","html_url":"https://github.com/octo/repo","description":"MCP server","stargazers_count":3,"created_at":"2024-01-02T03:04:05Z","default_branch":"release/2026-10","archived":false,"fork":false,"topics":["mcp-server"]}`)
		case r.URL.Path == "/repos/octo/repo/git/trees/release/2026-10":
			if r.URL.Query().Get("recursive") != "1" {
				t.Errorf("tree query = %q, want recursive=1", r.URL.RawQuery)
			}
			_, _ = fmt.Fprint(w, `{"truncated":false,"tree":[{"path":"skills/tool kit/SKILL.md","type":"blob"}]}`)
		case r.URL.Path == "/repos/octo/repo/contents/skills/tool kit/SKILL.md":
			if r.URL.Query().Get("ref") != "release/2026-10" {
				t.Errorf("contents ref = %q", r.URL.Query().Get("ref"))
			}
			body := base64.StdEncoding.EncodeToString([]byte("---\nname: release-tool\ndescription: A tool\n---\n"))
			_ = json.NewEncoder(w).Encode(map[string]string{
				"type": "file", "path": "skills/tool kit/SKILL.md", "encoding": "base64", "content": body,
				"download_url": "https://example.invalid/attacker-controlled",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	transport := &guardedTransport{allowedHost: strings.TrimPrefix(server.URL, "http://"), delegate: http.DefaultTransport, unexpectedHosts: &unexpectedHosts}
	client := NewClient(server.URL, &http.Client{Transport: transport}, "")
	got, err := client.inspectRepository(context.Background(), testNow, repositoryCandidate{ID: 42, FullName: "octo/repo"})
	if err != nil {
		t.Fatalf("inspectRepository: %v", err)
	}
	wantRequests := []string{
		"/repos/octo/repo",
		"/repos/octo/repo/git/trees/release%2F2026-10?recursive=1",
		"/repos/octo/repo/contents/skills/tool%20kit/SKILL.md?ref=release%2F2026-10",
	}
	if !reflect.DeepEqual(requests, wantRequests) {
		t.Fatalf("request URIs = %v, want %v", requests, wantRequests)
	}
	if len(unexpectedHosts) != 0 {
		t.Fatalf("client contacted arbitrary download_url hosts: %v", unexpectedHosts)
	}
	if !got.Complete || len(got.Skills) != 1 || !got.Skills[0].Verified {
		t.Fatalf("inspection = %+v", got)
	}
}

func TestInspectRepositoryCapsSkillReadsAtTwenty(t *testing.T) {
	files := make(map[string]string, 21)
	for i := 0; i < 21; i++ {
		files[fmt.Sprintf("skills/%02d/SKILL.md", i)] = fmt.Sprintf("---\nname: skill-%02d\ndescription: skill\n---\n", i)
	}
	server := newSkillInspectionServer(t, "octo/repo", false, files)
	defer server.Close()
	got, err := NewClient(server.URL, nil, "").inspectRepository(context.Background(), testNow, repositoryCandidate{ID: 42, FullName: "octo/repo"})
	if err != nil {
		t.Fatalf("inspectRepository: %v", err)
	}
	if got.Complete || len(got.Skills) != 20 {
		t.Fatalf("over-limit inspection complete=%v refs=%d, want incomplete and 20 refs", got.Complete, len(got.Skills))
	}
	for _, ref := range got.Skills {
		if ref.Verified {
			t.Fatalf("over-limit tree produced verified ref %+v", ref)
		}
	}
}

func TestInspectRepositoryClassifiesOnlyFiniteClues(t *testing.T) {
	server := newSkillInspectionServer(t, "octo/repo", false, nil)
	defer server.Close()
	got, err := NewClient(server.URL, nil, "").inspectRepository(context.Background(), testNow, repositoryCandidate{ID: 42, FullName: "octo/repo"})
	if err != nil {
		t.Fatalf("inspectRepository: %v", err)
	}
	if got.Type != "tool" {
		t.Fatalf("repository type = %q, want tool from the finite MCP server clue", got.Type)
	}
	if !reflect.DeepEqual(got.Scenes, []string{"development", "testing"}) {
		t.Fatalf("scenes = %v, want [development testing]", got.Scenes)
	}
	unknown := repositoryMetadata{Description: "The latest AI platform", Topics: []string{"artificial-intelligence"}}
	typeName, scenes := classifyRepository(unknown, nil)
	if typeName != "" || len(scenes) != 0 {
		t.Fatalf("generic AI was classified as type=%q scenes=%v", typeName, scenes)
	}
}

func newSkillInspectionServer(t *testing.T, repo string, truncated bool, files map[string]string) *httptest.Server {
	t.Helper()
	const branch = "main"
	parts := strings.Split(repo, "/")
	if len(parts) != 2 {
		t.Fatalf("invalid test repo %q", repo)
	}
	metadata := repositoryMetadata{
		ID: 42, FullName: repo, HTMLURL: "https://github.com/" + repo,
		Description: "MCP server for development testing", Stars: 10,
		CreatedAt: "2024-01-02T03:04:05Z", DefaultBranch: branch,
		Topics: []string{"mcp-server"},
	}
	tree := make([]map[string]string, 0, len(files))
	for path := range files {
		tree = append(tree, map[string]string{"path": path, "type": "blob"})
	}
	treeJSON, err := json.Marshal(map[string]any{"truncated": truncated, "tree": tree})
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/"+repo:
			_ = json.NewEncoder(w).Encode(metadata)
		case r.URL.Path == "/repos/"+repo+"/git/trees/"+branch:
			_, _ = w.Write(treeJSON)
		case strings.HasPrefix(r.URL.Path, "/repos/"+repo+"/contents/"):
			path := strings.TrimPrefix(r.URL.Path, "/repos/"+repo+"/contents/")
			body, ok := files[path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{
				"type": "file", "path": path, "encoding": "base64",
				"content": base64.StdEncoding.EncodeToString([]byte(body)),
			})
		default:
			http.NotFound(w, r)
		}
	}))
}

type guardedTransport struct {
	allowedHost     string
	delegate        http.RoundTripper
	unexpectedHosts *[]string
}

func (transport *guardedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Host != transport.allowedHost {
		*transport.unexpectedHosts = append(*transport.unexpectedHosts, request.URL.Host)
		return nil, errors.New("blocked unexpected host")
	}
	return transport.delegate.RoundTrip(request)
}
