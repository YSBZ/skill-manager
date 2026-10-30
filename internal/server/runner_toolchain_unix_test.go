//go:build !windows

package server

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestUnixNpxRunnerMethodsPreserveLegacyArguments(t *testing.T) {
	capture := filepath.Join(t.TempDir(), "argv")
	fakeNpx := filepath.Join(t.TempDir(), "npx")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$RUNNER_CAPTURE_ARGS\"\n"
	if err := os.WriteFile(fakeNpx, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RUNNER_CAPTURE_ARGS", capture)
	runner := unixRunner{}
	skillHubStore := filepath.Join(t.TempDir(), "store")
	cases := []struct {
		name string
		run  func() error
		want []string
	}{
		{"update one", func() error { _, _, err := runner.UpdateSkill(context.Background(), fakeNpx, "review"); return err }, []string{"skills", "update", "review", "-g", "--yes"}},
		{"update all", func() error { _, _, err := runner.UpdateAll(context.Background(), fakeNpx); return err }, []string{"skills", "update", "-g", "--yes"}},
		{"add package", func() error {
			_, _, err := runner.SkillsAdd(context.Background(), fakeNpx, "owner/repo@skill")
			return err
		}, []string{"skills", "add", "owner/repo@skill", "-g", "-y", "-a", "universal"}},
		{"add URL", func() error {
			_, _, err := runner.SkillsAddURL(context.Background(), fakeNpx, "https://github.com/owner/repo", "skill")
			return err
		}, []string{"skills", "add", "https://github.com/owner/repo", "--skill", "skill", "-g", "-y", "-a", "universal"}},
		{"find", func() error { _, _, err := runner.SkillsFind(context.Background(), fakeNpx, "go"); return err }, []string{"skills", "find", "go"}},
		{"remove", func() error { _, _, err := runner.SkillsRemove(context.Background(), fakeNpx, "skill"); return err }, []string{"skills", "remove", "skill", "-g", "-y"}},
		{"SkillHub search", func() error { _, _, err := runner.SkillHubSearch(context.Background(), fakeNpx, "go"); return err }, []string{"-y", "@astron-team/skillhub", "search", "go", "--json"}},
		{"SkillHub install", func() error {
			_, _, err := runner.SkillHubInstall(context.Background(), fakeNpx, "author/skill", skillHubStore)
			return err
		}, []string{"-y", "@astron-team/skillhub", "install", "author/skill", "--dir", skillHubStore, "--force", "--json"}},
		{"SkillHub remove", func() error {
			_, _, err := runner.SkillHubRemove(context.Background(), fakeNpx, "author/skill", "/tmp/store")
			return err
		}, []string{"-y", "@astron-team/skillhub", "remove", "author/skill", "--dir", "/tmp/store", "--json"}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.run(); err != nil {
				t.Fatalf("runner call failed: %v", err)
			}
			data, err := os.ReadFile(capture)
			if err != nil {
				t.Fatal(err)
			}
			got := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("argv=%q, want %q", got, tt.want)
			}
		})
	}
}
