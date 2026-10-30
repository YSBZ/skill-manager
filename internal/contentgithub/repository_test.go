package contentgithub

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestRepositoryMetadataDecode(t *testing.T) {
	data := []byte(`{"id":42,"full_name":"octo/agent-tools","html_url":"https://github.com/octo/agent-tools","description":"MCP server","stargazers_count":73,"created_at":"2024-01-02T03:04:05Z","default_branch":"main","archived":false,"fork":false,"topics":["mcp-server","developer-tools"]}`)
	got, err := decodeRepositoryMetadata(data)
	if err != nil {
		t.Fatalf("decodeRepositoryMetadata: %v", err)
	}
	want := repositoryMetadata{
		ID: 42, FullName: "octo/agent-tools", HTMLURL: "https://github.com/octo/agent-tools",
		Description: "MCP server", Stars: 73, CreatedAt: "2024-01-02T03:04:05Z",
		DefaultBranch: "main", Archived: false, Fork: false,
		Topics: []string{"mcp-server", "developer-tools"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("metadata = %+v, want %+v", got, want)
	}
}

func TestRepositoryMetadataRejectsInvalidFullName(t *testing.T) {
	for _, name := range []string{"", "owner", "owner/repo/extra", "owner/../repo", "owner/repo\\child"} {
		t.Run(name, func(t *testing.T) {
			data, err := json.Marshal(map[string]any{"id": 1, "full_name": name})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeRepositoryMetadata(data); err == nil {
				t.Fatalf("decodeRepositoryMetadata accepted full_name %q", name)
			}
		})
	}
	if _, err := decodeRepositoryMetadata([]byte(`{"id":0,"full_name":"owner/repo"}`)); err == nil {
		t.Fatal("decodeRepositoryMetadata accepted a nonpositive id")
	}
}

func TestTreeEvidence(t *testing.T) {
	t.Run("truncated tree retains skill paths but stays incomplete", func(t *testing.T) {
		got, err := decodeTree([]byte(`{"truncated":true,"tree":[{"path":"z/SKILL.md","type":"blob"},{"path":"a/SKILL.md","type":"blob"},{"path":"ignored/SKILL.md","type":"tree"},{"path":"README.md","type":"blob"}]}`))
		if err != nil {
			t.Fatalf("decodeTree: %v", err)
		}
		if got.Complete {
			t.Fatal("truncated tree was marked complete")
		}
		if want := []string{"a/SKILL.md", "z/SKILL.md"}; !reflect.DeepEqual(got.SkillPaths, want) {
			t.Fatalf("skill paths = %v, want %v", got.SkillPaths, want)
		}
	})

	t.Run("empty truncated tree cannot prove there is no skill", func(t *testing.T) {
		got, err := decodeTree([]byte(`{"truncated":true,"tree":[]}`))
		if err != nil {
			t.Fatalf("decodeTree: %v", err)
		}
		if got.Complete || len(got.SkillPaths) != 0 {
			t.Fatalf("empty truncated tree evidence = %+v", got)
		}
	})

	t.Run("missing tree completeness fields are incomplete evidence", func(t *testing.T) {
		for _, data := range []string{`{"truncated":false}`, `{"tree":[]}`, `{"truncated":false,"tree":null}`} {
			if _, err := decodeTree([]byte(data)); err == nil {
				t.Errorf("decodeTree accepted incomplete response %s", data)
			}
		}
	})

	t.Run("complete tree requires an exact SKILL.md blob path", func(t *testing.T) {
		got, err := decodeTree([]byte(`{"truncated":false,"tree":[{"path":"SKILL.md","type":"blob"},{"path":"nested/SKILL.md","type":"blob"},{"path":"nested/skill.md","type":"blob"},{"path":"nested/SKILL.md/child","type":"blob"}]}`))
		if err != nil {
			t.Fatalf("decodeTree: %v", err)
		}
		if !got.Complete {
			t.Fatal("complete tree was marked incomplete")
		}
		if want := []string{"SKILL.md", "nested/SKILL.md"}; !reflect.DeepEqual(got.SkillPaths, want) {
			t.Fatalf("skill paths = %v, want %v", got.SkillPaths, want)
		}
	})
}
