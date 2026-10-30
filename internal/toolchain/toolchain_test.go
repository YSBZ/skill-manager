package toolchain

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

type testManifest struct {
	SchemaVersion   int               `json:"schemaVersion"`
	Platform        string            `json:"platform"`
	NodeMode        string            `json:"nodeMode,omitempty"`
	Node            string            `json:"node,omitempty"`
	Npx             string            `json:"npx,omitempty"`
	NpxCLI          string            `json:"npxCLI"`
	SkillsCLI       string            `json:"skillsCLI"`
	Git             string            `json:"git"`
	GitExecPath     string            `json:"gitExecPath"`
	GitConfigSystem string            `json:"gitConfigSystem,omitempty"`
	GitTemplateDir  string            `json:"gitTemplateDir,omitempty"`
	CABundle        string            `json:"caBundle,omitempty"`
	Path            []string          `json:"path"`
	Versions        map[string]string `json:"versions"`
}

func writeRuntimeFixture(t *testing.T, platform string) string {
	t.Helper()
	root := t.TempDir()
	windows := strings.HasPrefix(platform, "windows-")
	manifest := testManifest{
		SchemaVersion: 1,
		Platform:      platform,
		Versions:      map[string]string{"node": "26.13.0", "git": "2.56.0", "skills": "1.7.4"},
	}
	paths := map[string]bool{}
	if windows {
		manifest.Node = "node/node.exe"
		manifest.Npx = "node/npx.cmd"
		manifest.NpxCLI = "node/node_modules/npm/bin/npx-cli.js"
		manifest.SkillsCLI = "skills/node_modules/skills/dist/cli.mjs"
		manifest.Git = "git/cmd/git.exe"
		manifest.GitExecPath = "git/ucrt64/libexec/git-core"
		manifest.GitConfigSystem = "git/etc/gitconfig"
		manifest.GitTemplateDir = "git/share/git-core/templates"
		manifest.CABundle = "git/mingw64/ssl/certs/ca-bundle.crt"
		manifest.Path = []string{"node", "git/cmd", "git/ucrt64/bin", "git/mingw64/bin", "git/usr/bin"}
	} else {
		manifest.Node = "node/bin/node"
		manifest.Npx = "node/bin/npx"
		manifest.NpxCLI = "node/lib/node_modules/npm/bin/npx-cli.js"
		manifest.SkillsCLI = "skills/node_modules/skills/dist/cli.mjs"
		manifest.Git = "git/bin/git"
		manifest.GitExecPath = "git/libexec/git-core"
		manifest.GitConfigSystem = "git/etc/gitconfig"
		manifest.GitTemplateDir = "git/share/git-core/templates"
		manifest.CABundle = "git/ssl/cert.pem"
		manifest.Path = []string{"node/bin", "git/bin"}
	}
	for _, p := range []string{manifest.Node, manifest.Npx, manifest.NpxCLI, manifest.SkillsCLI, manifest.Git, manifest.GitExecPath, manifest.GitConfigSystem, manifest.GitTemplateDir, manifest.CABundle} {
		if p != "" {
			paths[p] = true
		}
	}
	for _, p := range manifest.Path {
		paths[p] = true
	}
	for p := range paths {
		full := filepath.Join(root, filepath.FromSlash(p))
		if executablePath(p, windows) {
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			mode := os.FileMode(0o644)
			if !windows {
				mode = 0o755
			}
			if err := os.WriteFile(full, []byte("fixture"), mode); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(full, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "runtime.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func executablePath(p string, windows bool) bool {
	if windows {
		return p == "git/etc/gitconfig" || strings.HasSuffix(strings.ToLower(p), ".exe") || strings.HasSuffix(strings.ToLower(p), ".cmd") || strings.HasSuffix(strings.ToLower(p), ".js") || strings.HasSuffix(strings.ToLower(p), ".mjs") || strings.HasSuffix(strings.ToLower(p), ".crt")
	}
	return p == "node/bin/node" || p == "node/bin/npx" || p == "git/bin/git" || p == "git/etc/gitconfig" || p == "git/ssl/cert.pem" || strings.HasSuffix(p, ".js") || strings.HasSuffix(p, ".mjs") || strings.HasSuffix(p, ".crt")
}

func TestLoadRuntimeValidatesAndResolvesManifest(t *testing.T) {
	root := writeRuntimeFixture(t, "darwin-arm64")
	rt, err := loadRuntime(root, "darwin-arm64")
	if err != nil {
		t.Fatal(err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if rt.platform != "darwin-arm64" || rt.nodePath != filepath.Join(canonicalRoot, "node/bin/node") || rt.gitPath != filepath.Join(canonicalRoot, "git/bin/git") {
		t.Fatalf("loaded runtime paths do not match manifest: %+v", rt)
	}
	if len(rt.path) != 2 || rt.path[0] != filepath.Join(canonicalRoot, "node/bin") || rt.path[1] != filepath.Join(canonicalRoot, "git/bin") {
		t.Fatalf("PATH dirs = %v", rt.path)
	}
}

func TestLoadWindowsRuntimeLayout(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Skill Manager", "runtime", "windows-amd64")
	if err := os.MkdirAll(filepath.Dir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	fixture := writeRuntimeFixture(t, "windows-amd64")
	if err := os.Rename(fixture, root); err != nil {
		t.Fatal(err)
	}
	rt, err := loadRuntime(root, "windows-amd64")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(rt.nodePath) != "node.exe" || filepath.Base(rt.npxPath) != "npx.cmd" || filepath.Base(rt.gitPath) != "git.exe" {
		t.Fatalf("Windows executable paths do not match the portable layout: %+v", rt)
	}
	cmd := npxCommandFor(context.Background(), rt, "windows", rt.npxPath, "skills", "update", "-g", "--yes")
	want := []string{rt.nodePath, rt.skillsCLIPath, "update", "-g", "--yes"}
	if cmd.Path != rt.nodePath || !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("Windows bundled Skills CLI command = %#v, want path %q args %#v", cmd, rt.nodePath, want)
	}
	other := npxCommandFor(context.Background(), rt, "windows", rt.npxPath, "-y", "@astron-team/skillhub", "search", "go")
	wantOther := []string{rt.nodePath, rt.npxCLIPath, "-y", "@astron-team/skillhub", "search", "go"}
	if other.Path != rt.nodePath || !reflect.DeepEqual(other.Args, wantOther) {
		t.Fatalf("Windows bundled npm CLI command = %#v, want path %q args %#v", other, rt.nodePath, wantOther)
	}
}

func TestRuntimeRootUsesSupportedAppLayouts(t *testing.T) {
	macExe := filepath.Join("Applications", "Skill Manager.app", "Contents", "MacOS", "skillmanager")
	if got, want := runtimeRootForExecutable(macExe, "darwin", "arm64"), filepath.Join("Applications", "Skill Manager.app", "Contents", "Resources", "runtime", "darwin-arm64"); got != want {
		t.Fatalf("macOS runtime root = %q, want %q", got, want)
	}
	winExe := filepath.Join("Tools", "Skill Manager", "SkillManager.exe")
	if got, want := runtimeRootForExecutable(winExe, "windows", "amd64"), filepath.Join("Tools", "Skill Manager", "runtime", "windows-amd64"); got != want {
		t.Fatalf("Windows runtime root = %q, want %q", got, want)
	}
	if got := runtimeRootForExecutable("/tmp/skillmanager", "linux", "amd64"); got != "" {
		t.Fatalf("unsupported development runtime root = %q, want empty", got)
	}
}

func TestLoadRuntimeAllowsInternalSymlinks(t *testing.T) {
	root := writeRuntimeFixture(t, "darwin-arm64")
	actual := filepath.Join(root, "skills/node_modules/skills/dist/actual.mjs")
	if err := os.WriteFile(actual, []byte("fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "skills/node_modules/skills/dist/cli.mjs")
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(actual, link); err != nil {
		t.Fatal(err)
	}
	if _, err := loadRuntime(root, "darwin-arm64"); err != nil {
		t.Fatalf("internal symlink should be accepted: %v", err)
	}
}

func TestLoadRuntimeRejectsMalformedOrEscapingBundle(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, root string, manifest *testManifest)
	}{
		{name: "wrong platform", mutate: func(_ *testing.T, _ string, m *testManifest) { m.Platform = "darwin-amd64" }},
		{name: "wrong node version", mutate: func(_ *testing.T, _ string, m *testManifest) { m.Versions["node"] = "latest" }},
		{name: "Node directory missing from PATH", mutate: func(_ *testing.T, _ string, m *testManifest) { m.Path = []string{"git/bin"} }},
		{name: "path escape", mutate: func(_ *testing.T, _ string, m *testManifest) { m.Node = "../outside/node" }},
		{name: "absolute path", mutate: func(_ *testing.T, _ string, m *testManifest) { m.Git = "/usr/bin/git" }},
		{name: "missing executable bit", mutate: func(_ *testing.T, root string, _ *testManifest) {
			if err := os.Chmod(filepath.Join(root, "node/bin/node"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "symlink escape", mutate: func(t *testing.T, root string, _ *testManifest) {
			outside := filepath.Join(t.TempDir(), "outside.mjs")
			if err := os.WriteFile(outside, []byte("fixture"), 0o644); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(root, "skills/node_modules/skills/dist/cli.mjs")
			if err := os.Remove(link); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, link); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := writeRuntimeFixture(t, "darwin-arm64")
			manifestPath := filepath.Join(root, "runtime.json")
			data, err := os.ReadFile(manifestPath)
			if err != nil {
				t.Fatal(err)
			}
			var manifest testManifest
			if err := json.Unmarshal(data, &manifest); err != nil {
				t.Fatal(err)
			}
			tt.mutate(t, root, &manifest)
			data, err = json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := loadRuntime(root, "darwin-arm64"); err == nil {
				t.Fatal("loadRuntime accepted invalid bundle")
			}
		})
	}
}

func TestActivateFromUsesPlatformResourcesAndDevFallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("runtime bundle layout is desktop macOS/Windows only")
	}
	goos, goarch := "darwin", "arm64"
	oldActive := activeRuntime()
	t.Cleanup(func() { setActiveRuntime(oldActive) })
	t.Setenv("PATH", "/existing/bin")
	central := filepath.Join(t.TempDir(), "central with spaces")

	// Development executable with no resource directory must leave process config
	// untouched and retain the system-tool fallback.
	devExe := filepath.Join(t.TempDir(), "skillmanager")
	activated, err := activateFrom(devExe, goos, goarch, central)
	if err != nil || activated || activeRuntime() != nil {
		t.Fatalf("missing dev bundle: activated=%v runtime=%v err=%v", activated, activeRuntime(), err)
	}
	if got := os.Getenv("PATH"); got != "/existing/bin" {
		t.Fatalf("dev fallback changed PATH to %q", got)
	}

	// A runtime parent without this architecture's directory is a damaged app
	// bundle, not a development install that can safely fall back to system tools.
	partialExe := filepath.Join(t.TempDir(), "SkillManager.app", "Contents", "MacOS", "skillmanager")
	partialRoot := runtimeRootForExecutable(partialExe, goos, goarch)
	if err := os.MkdirAll(filepath.Dir(partialRoot), 0o755); err != nil {
		t.Fatal(err)
	}
	if activated, err := activateFrom(partialExe, goos, goarch, central); err == nil || activated {
		t.Fatalf("missing platform runtime should fail activation: activated=%v err=%v", activated, err)
	}

	// A runtime directory that exists but has a broken manifest is not a dev
	// fallback: refuse startup instead of mixing bundled and system tools.
	bundleExe := devExe
	bundleExe = filepath.Join(t.TempDir(), "SkillManager.app", "Contents", "MacOS", "skillmanager")
	root := runtimeRootForExecutable(bundleExe, goos, goarch)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "runtime.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := activateFrom(bundleExe, goos, goarch, central); err == nil {
		t.Fatal("malformed existing bundle should fail activation")
	}
}

func TestActivateFromPrependsBundleAndUsesCentralCache(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("manifest fixture uses the macOS layout")
	}
	goos, goarch := "darwin", "arm64"
	platform := goos + "-" + goarch
	app := filepath.Join(t.TempDir(), "Skill Manager.app")
	exe := filepath.Join(app, "Contents", "MacOS", "skillmanager")
	root := runtimeRootForExecutable(exe, goos, goarch)
	if err := os.MkdirAll(filepath.Dir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	fixture := writeRuntimeFixture(t, platform)
	if err := os.Rename(fixture, root); err != nil {
		t.Fatal(err)
	}
	oldActive := activeRuntime()
	t.Cleanup(func() { setActiveRuntime(oldActive) })
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("GIT_EXEC_PATH", "/custom/git-core")
	t.Setenv("GIT_CONFIG_SYSTEM", "/custom/gitconfig")
	t.Setenv("GIT_TEMPLATE_DIR", "/custom/templates")
	t.Setenv("GIT_SSL_CAINFO", "/custom/ca.pem")
	t.Setenv("HTTPS_PROXY", "http://proxy.example")
	t.Setenv("NODE_USE_SYSTEM_CA", "0")
	t.Setenv("NODE_EXTRA_CA_CERTS", "/custom/node-ca.pem")
	t.Setenv("NODE_USE_ENV_PROXY", "0")
	homeBefore := os.Getenv("HOME")
	central := filepath.Join(t.TempDir(), "central with spaces")

	activated, err := activateFrom(exe, goos, goarch, central)
	if err != nil || !activated {
		t.Fatalf("activateFrom: activated=%v err=%v", activated, err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	gotPATH := filepath.SplitList(os.Getenv("PATH"))
	if len(gotPATH) < 4 || gotPATH[0] != filepath.Join(canonicalRoot, "node/bin") || gotPATH[1] != filepath.Join(canonicalRoot, "git/bin") {
		t.Fatalf("bundle paths did not lead PATH: %v", gotPATH)
	}
	if gotPATH[2] != "/usr/bin" || gotPATH[3] != "/bin" {
		t.Fatalf("original OS paths were not preserved: %v", gotPATH)
	}
	if os.Getenv("GIT_EXEC_PATH") != "/custom/git-core" || os.Getenv("GIT_CONFIG_SYSTEM") != "/custom/gitconfig" || os.Getenv("GIT_TEMPLATE_DIR") != "/custom/templates" || os.Getenv("GIT_SSL_CAINFO") != "/custom/ca.pem" {
		t.Fatal("explicit Git path overrides were replaced")
	}
	if os.Getenv("HTTPS_PROXY") != "http://proxy.example" || os.Getenv("HOME") != homeBefore {
		t.Fatal("activation changed proxy or HOME")
	}
	if os.Getenv("NODE_USE_SYSTEM_CA") != "0" || os.Getenv("NODE_EXTRA_CA_CERTS") != "/custom/node-ca.pem" || os.Getenv("NODE_USE_ENV_PROXY") != "0" {
		t.Fatal("activation replaced explicit Node TLS or proxy settings")
	}
	cache := filepath.Join(central, "cache", "npm")
	if os.Getenv("npm_config_cache") != cache {
		t.Fatalf("npm_config_cache=%q, want %q", os.Getenv("npm_config_cache"), cache)
	}
	if info, err := os.Stat(cache); err != nil || !info.IsDir() {
		t.Fatalf("npm cache was not created: info=%v err=%v", info, err)
	}
	if os.Getenv("DISABLE_TELEMETRY") != "1" {
		t.Fatalf("Skills telemetry was not disabled: %q", os.Getenv("DISABLE_TELEMETRY"))
	}
}

func TestActivateFromSetsNodeSystemCAAndProxyDefaults(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("manifest fixture uses the macOS layout")
	}
	for _, proxyKey := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		t.Run(proxyKey, func(t *testing.T) {
			for _, key := range []string{"NODE_USE_SYSTEM_CA", "NODE_EXTRA_CA_CERTS", "NODE_USE_ENV_PROXY", "HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
				unsetEnvForTest(t, key)
			}
			t.Setenv(proxyKey, "http://proxy.example")
			prepareActivationEnvForTest(t)
			activateRuntimeFixtureForTest(t)
			if got := os.Getenv("NODE_USE_SYSTEM_CA"); got != "1" {
				t.Fatalf("NODE_USE_SYSTEM_CA=%q, want 1", got)
			}
			if got := os.Getenv("NODE_USE_ENV_PROXY"); got != "1" {
				t.Fatalf("NODE_USE_ENV_PROXY=%q, want 1 with %s configured", got, proxyKey)
			}
			if _, exists := os.LookupEnv("NODE_EXTRA_CA_CERTS"); exists {
				t.Fatalf("activation set NODE_EXTRA_CA_CERTS unexpectedly: %q", os.Getenv("NODE_EXTRA_CA_CERTS"))
			}
		})
	}
}

func TestActivateFromDoesNotEnableNodeProxyWithoutProxyEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("manifest fixture uses the macOS layout")
	}
	for _, key := range []string{"NODE_USE_SYSTEM_CA", "NODE_EXTRA_CA_CERTS", "NODE_USE_ENV_PROXY", "HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		unsetEnvForTest(t, key)
	}
	prepareActivationEnvForTest(t)
	activateRuntimeFixtureForTest(t)
	if got := os.Getenv("NODE_USE_SYSTEM_CA"); got != "1" {
		t.Fatalf("NODE_USE_SYSTEM_CA=%q, want 1", got)
	}
	if got, exists := os.LookupEnv("NODE_USE_ENV_PROXY"); exists {
		t.Fatalf("NODE_USE_ENV_PROXY=%q, want unset when no proxy is configured", got)
	}
}

func prepareActivationEnvForTest(t *testing.T) {
	t.Helper()
	for key, value := range map[string]string{
		"PATH":              "/usr/bin:/bin",
		"npm_config_cache":  "",
		"DISABLE_TELEMETRY": "",
		"GIT_EXEC_PATH":     "",
		"GIT_CONFIG_SYSTEM": "",
		"GIT_TEMPLATE_DIR":  "",
		"GIT_SSL_CAINFO":    "",
		"GIT_SSL_CAPATH":    "",
	} {
		t.Setenv(key, value)
	}
}

func activateRuntimeFixtureForTest(t *testing.T) {
	t.Helper()
	app := filepath.Join(t.TempDir(), "Skill Manager.app")
	exe := filepath.Join(app, "Contents", "MacOS", "skillmanager")
	root := runtimeRootForExecutable(exe, "darwin", "arm64")
	if err := os.MkdirAll(filepath.Dir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	fixture := writeRuntimeFixture(t, "darwin-arm64")
	if err := os.Rename(fixture, root); err != nil {
		t.Fatal(err)
	}
	oldActive := activeRuntime()
	t.Cleanup(func() { setActiveRuntime(oldActive) })
	central := filepath.Join(t.TempDir(), "central")
	activated, err := activateFrom(exe, "darwin", "arm64", central)
	if err != nil || !activated {
		t.Fatalf("activateFrom: activated=%v err=%v", activated, err)
	}
}

func TestActivateFromSetsUnsetGitRuntimePaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("manifest fixture uses the macOS layout")
	}
	for _, key := range []string{"GIT_EXEC_PATH", "GIT_CONFIG_SYSTEM", "GIT_TEMPLATE_DIR", "GIT_SSL_CAINFO", "GIT_SSL_CAPATH"} {
		unsetEnvForTest(t, key)
	}
	app := filepath.Join(t.TempDir(), "SkillManager.app")
	exe := filepath.Join(app, "Contents", "MacOS", "skillmanager")
	goos, goarch := "darwin", "arm64"
	root := runtimeRootForExecutable(exe, goos, goarch)
	if err := os.MkdirAll(filepath.Dir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	fixture := writeRuntimeFixture(t, goos+"-"+goarch)
	if err := os.Rename(fixture, root); err != nil {
		t.Fatal(err)
	}
	oldActive := activeRuntime()
	t.Cleanup(func() { setActiveRuntime(oldActive) })
	t.Setenv("PATH", "/usr/bin:/bin")
	central := filepath.Join(t.TempDir(), "central")
	if _, err := activateFrom(exe, goos, goarch, central); err != nil {
		t.Fatal(err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	for key, rel := range map[string]string{
		"GIT_EXEC_PATH":     "git/libexec/git-core",
		"GIT_CONFIG_SYSTEM": "git/etc/gitconfig",
		"GIT_TEMPLATE_DIR":  "git/share/git-core/templates",
		"GIT_SSL_CAINFO":    "git/ssl/cert.pem",
	} {
		if got, want := os.Getenv(key), filepath.Join(canonicalRoot, rel); got != want {
			t.Errorf("%s=%q, want %q", key, got, want)
		}
	}
}

func TestNpxCommandDispatchesSkillsLocallyAndOtherPackagesThroughNpm(t *testing.T) {
	root := writeRuntimeFixture(t, "darwin-arm64")
	rt, err := loadRuntime(root, "darwin-arm64")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	skills := npxCommandFor(ctx, rt, "darwin", rt.npxPath, "skills", "add", "owner/repo", "-g", "-y", "-a", "universal")
	wantSkills := []string{rt.nodePath, rt.skillsCLIPath, "add", "owner/repo", "-g", "-y", "-a", "universal"}
	if !reflect.DeepEqual(skills.Args, wantSkills) || skills.Path != rt.nodePath {
		t.Fatalf("bundled Skills CLI command = %#v, want path %q args %#v", skills, rt.nodePath, wantSkills)
	}

	other := npxCommandFor(ctx, rt, "darwin", rt.npxPath, "@astron-team/skillhub", "search", "react", "--json")
	wantOther := []string{rt.nodePath, rt.npxCLIPath, "-y", "@astron-team/skillhub", "search", "react", "--json"}
	if !reflect.DeepEqual(other.Args, wantOther) || other.Path != rt.nodePath {
		t.Fatalf("bundled npm CLI command = %#v, want path %q args %#v", other, rt.nodePath, wantOther)
	}

	otherWithYes := npxCommandFor(ctx, rt, "darwin", rt.npxPath, "-y", "@astron-team/skillhub", "search", "react")
	if !reflect.DeepEqual(otherWithYes.Args, []string{rt.nodePath, rt.npxCLIPath, "-y", "@astron-team/skillhub", "search", "react"}) {
		t.Fatalf("explicit -y should not be duplicated: %#v", otherWithYes.Args)
	}
}

func TestNpxCommandOnlyUsesBundleForResolvedBundledNpx(t *testing.T) {
	root := writeRuntimeFixture(t, "darwin-arm64")
	rt, err := loadRuntime(root, "darwin-arm64")
	if err != nil {
		t.Fatal(err)
	}
	cmd := npxCommandFor(context.Background(), rt, "darwin", "/user/bin/npx", "skills", "find", "go")
	if cmd.Path != "/user/bin/npx" || !reflect.DeepEqual(cmd.Args, []string{"/user/bin/npx", "skills", "find", "go"}) {
		t.Fatalf("non-bundled npx path should retain direct invocation: %#v", cmd)
	}
}

func TestLegacyWindowsNpxCommandKeepsCmdShimBehavior(t *testing.T) {
	cmd := npxCommandFor(context.Background(), nil, "windows", `C:\Node Runtime\npx.cmd`, "skills", "find", "go")
	want := []string{"cmd", "/c", `C:\Node Runtime\npx.cmd`, "skills", "find", "go"}
	if cmd.Path != "cmd" || !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("legacy Windows npx command = %#v, want path cmd args %#v", cmd, want)
	}
}

func unsetEnvForTest(t *testing.T, key string) {
	t.Helper()
	old, existed := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(key, old)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}
