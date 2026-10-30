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
	"time"
)

func writeSystemRuntimeFixture(t *testing.T, platform string) string {
	t.Helper()
	root := writeRuntimeFixture(t, platform)
	manifestPath := filepath.Join(root, "runtime.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest testManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.SchemaVersion = 2
	manifest.NodeMode = "system"
	manifest.Node = ""
	manifest.Npx = ""
	manifest.NpxCLI = "npm/bin/npx-cli.js"
	manifest.Versions = map[string]string{
		"node":   "system",
		"npm":    "11.20.0",
		"git":    "2.56.0",
		"skills": "1.7.4",
	}
	filteredPath := manifest.Path[:0]
	for _, entry := range manifest.Path {
		if !strings.HasPrefix(entry, "node/") && entry != "node" {
			filteredPath = append(filteredPath, entry)
		}
	}
	manifest.Path = filteredPath
	if err := os.RemoveAll(filepath.Join(root, "node")); err != nil {
		t.Fatal(err)
	}
	npmCLI := filepath.Join(root, filepath.FromSlash(manifest.NpxCLI))
	if err := os.MkdirAll(filepath.Dir(npmCLI), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(npmCLI, []byte("fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestLoadSystemNodeRuntimeV2KeepsOnlyBundledGitOnPATH(t *testing.T) {
	for _, platform := range []string{"darwin-arm64", "windows-amd64"} {
		t.Run(platform, func(t *testing.T) {
			root := writeSystemRuntimeFixture(t, platform)
			rt, err := loadRuntime(root, platform)
			if err != nil {
				t.Fatal(err)
			}
			if rt.schemaVersion != 2 || rt.nodeMode != "system" {
				t.Fatalf("runtime mode = schema %d / %q, want schema 2 / system", rt.schemaVersion, rt.nodeMode)
			}
			if rt.nodePath != "" || rt.npxPath != "" {
				t.Fatalf("v2 manifest unexpectedly selected bundled Node or npx: node=%q npx=%q", rt.nodePath, rt.npxPath)
			}
			canonicalRoot, err := filepath.EvalSymlinks(root)
			if err != nil {
				t.Fatal(err)
			}
			if want := filepath.Join(canonicalRoot, "npm", "bin", "npx-cli.js"); rt.npxCLIPath != want {
				t.Fatalf("npm CLI path = %q, want %q", rt.npxCLIPath, want)
			}
			for _, entry := range rt.path {
				if entry == filepath.Join(root, "node") || strings.Contains(filepath.ToSlash(entry), "/node/") {
					t.Fatalf("v2 PATH contains bundled Node: %v", rt.path)
				}
			}
		})
	}
}

func TestLoadSystemNodeRuntimeRejectsUnsafeManifest(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testManifest)
	}{
		{name: "wrong node mode", mutate: func(m *testManifest) { m.NodeMode = "bundled" }},
		{name: "bundled node path", mutate: func(m *testManifest) { m.Node = "node/node" }},
		{name: "bundled npx path", mutate: func(m *testManifest) { m.Npx = "node/npx" }},
		{name: "wrong node version marker", mutate: func(m *testManifest) { m.Versions["node"] = "22.20.0" }},
		{name: "wrong npm version", mutate: func(m *testManifest) { m.Versions["npm"] = "latest" }},
		{name: "npm CLI escapes runtime", mutate: func(m *testManifest) { m.NpxCLI = "../outside/npx-cli.js" }},
		{name: "unexpected Skills CLI path", mutate: func(m *testManifest) { m.SkillsCLI = "npm/bin/npx-cli.js" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := writeSystemRuntimeFixture(t, "darwin-arm64")
			manifestPath := filepath.Join(root, "runtime.json")
			data, err := os.ReadFile(manifestPath)
			if err != nil {
				t.Fatal(err)
			}
			var manifest testManifest
			if err := json.Unmarshal(data, &manifest); err != nil {
				t.Fatal(err)
			}
			tt.mutate(&manifest)
			data, err = json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := loadRuntime(root, "darwin-arm64"); err == nil {
				t.Fatal("loadRuntime accepted unsafe v2 manifest")
			}
		})
	}
}

func TestLoadRuntimeV2DoesNotProbeSystemNode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("probe marker fixture uses a POSIX script")
	}
	root := writeSystemRuntimeFixture(t, "darwin-arm64")
	nodeDir := filepath.Join(t.TempDir(), "node with spaces")
	if err := os.MkdirAll(nodeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "node-was-probed")
	fakeNode := filepath.Join(nodeDir, "node")
	if err := os.WriteFile(fakeNode, []byte("#!/bin/sh\necho invoked > "+marker+"\necho v22.20.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", nodeDir)
	if _, err := loadRuntime(root, "darwin-arm64"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("loadRuntime executed system Node unexpectedly: marker stat error=%v", err)
	}
}

func TestInstallerCommandV1AndInactiveModes(t *testing.T) {
	oldActive := activeRuntime()
	t.Cleanup(func() { setActiveRuntime(oldActive) })

	setActiveRuntime(nil)
	if path, dependency, managed := InstallerCommand(); path != "" || dependency != nil || managed {
		t.Fatalf("inactive InstallerCommand = (%q, %v, %v), want empty/nil/unmanaged", path, dependency, managed)
	}

	rt, err := loadRuntime(writeRuntimeFixture(t, "darwin-arm64"), "darwin-arm64")
	if err != nil {
		t.Fatal(err)
	}
	setActiveRuntime(rt)
	path, dependency, managed := InstallerCommand()
	if path != rt.npxPath || dependency != nil || !managed {
		t.Fatalf("v1 InstallerCommand = (%q, %v, %v), want bundled npx/nil/managed", path, dependency, managed)
	}
}

func TestSystemNodeProbeStatesAndInstallerCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("probe fixtures use POSIX scripts")
	}
	tests := []struct {
		name      string
		version   string
		wantState string
		wantReady bool
	}{
		{name: "minimum version", version: "v22.20.0", wantState: "ready", wantReady: true},
		{name: "newer version", version: "v26.13.0", wantState: "ready", wantReady: true},
		{name: "too old", version: "v22.19.0", wantState: "too_old"},
		{name: "invalid output", version: "not-a-node-version", wantState: "invalid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nodeDir := filepath.Join(t.TempDir(), "node runtime with spaces")
			if err := os.MkdirAll(nodeDir, 0o755); err != nil {
				t.Fatal(err)
			}
			nodePath := filepath.Join(nodeDir, "node")
			script := "#!/bin/sh\nprintf '%s\\n'\n"
			script = strings.Replace(script, "%s", tt.version, 1)
			if err := os.WriteFile(nodePath, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			activateSystemRuntimeForTest(t, nodeDir)
			path, dependency, managed := InstallerCommand()
			if !managed || dependency == nil || dependency.State != tt.wantState {
				t.Fatalf("InstallerCommand = (%q, %+v, %v), want state %q and managed", path, dependency, managed, tt.wantState)
			}
			if dependency.RequiredVersion != systemNodeRequiredVersion || dependency.DownloadURL != nodeDownloadURL {
				t.Fatalf("dependency metadata = %+v", dependency)
			}
			if tt.wantReady {
				absNode, err := filepath.Abs(nodePath)
				if err != nil {
					t.Fatal(err)
				}
				if path != absNode || dependency.Version != tt.version {
					t.Fatalf("ready Node command = (%q, %+v), want absolute %q and version %q", path, dependency, absNode, tt.version)
				}
			} else if path != "" {
				t.Fatalf("unusable Node returned command path %q", path)
			}
		})
	}
}

func TestSystemNodeMissingDoesNotBlockGitActivation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("system Node lookup test is POSIX-only")
	}
	activateSystemRuntimeForTest(t, "")
	path, dependency, managed := InstallerCommand()
	if path != "" || dependency == nil || dependency.State != "missing" || !managed {
		t.Fatalf("missing Node InstallerCommand = (%q, %+v, %v)", path, dependency, managed)
	}
	rt := activeRuntime()
	if rt == nil || rt.gitPath == "" {
		t.Fatalf("Git runtime did not activate with missing Node: %+v", rt)
	}
	if got := filepath.SplitList(os.Getenv("PATH"))[0]; got != filepath.Join(rt.root, "git/bin") {
		t.Fatalf("Git runtime PATH not activated: %q", os.Getenv("PATH"))
	}
}

func TestSystemNodeProbeTimeoutIsInvalidAndDoesNotLeakOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("probe timeout fixture uses a POSIX script")
	}
	nodeDir := filepath.Join(t.TempDir(), "node with spaces")
	if err := os.MkdirAll(nodeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	nodePath := filepath.Join(nodeDir, "node")
	if err := os.WriteFile(nodePath, []byte("#!/bin/sh\necho PRIVATE_NODE_STDERR >&2\nexec /bin/sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	activateSystemRuntimeForTest(t, nodeDir)
	if elapsed := time.Since(started); elapsed > 8*time.Second {
		t.Fatalf("version probe timeout was not bounded: %s", elapsed)
	}
	path, dependency, managed := InstallerCommand()
	if path != "" || dependency == nil || dependency.State != "invalid" || !managed {
		t.Fatalf("timed out Node probe = (%q, %+v, %v)", path, dependency, managed)
	}
	encoded, err := json.Marshal(dependency)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "PRIVATE_NODE_STDERR") || strings.Contains(string(encoded), nodePath) {
		t.Fatalf("dependency exposed probe output or path: %s", encoded)
	}
}

func TestSystemNodeProbeRejectsOversizedOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("probe output fixture uses a POSIX script")
	}
	nodeDir := filepath.Join(t.TempDir(), "node with spaces")
	if err := os.MkdirAll(nodeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	nodePath := filepath.Join(nodeDir, "node")
	script := "#!/bin/sh\nprintf 'v22.20.0\\n'; printf '%8192s' ''\n"
	if err := os.WriteFile(nodePath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	activateSystemRuntimeForTest(t, nodeDir)
	path, dependency, managed := InstallerCommand()
	if path != "" || dependency == nil || dependency.State != "invalid" || dependency.Version != "" || !managed {
		t.Fatalf("oversized version output = (%q, %+v, %v), want invalid without version", path, dependency, managed)
	}
}

func TestSystemNodeFeatureEnvironmentVersionGates(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("feature gate fixtures use POSIX scripts")
	}
	tests := []struct {
		version   string
		wantCA    bool
		wantProxy bool
	}{
		{version: "v22.20.0", wantCA: true, wantProxy: false},
		{version: "v22.21.0", wantCA: true, wantProxy: true},
		{version: "v24.5.0", wantCA: false, wantProxy: true},
		{version: "v24.6.0", wantCA: true, wantProxy: true},
	}
	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			nodeDir := filepath.Join(t.TempDir(), "node")
			if err := os.MkdirAll(nodeDir, 0o755); err != nil {
				t.Fatal(err)
			}
			nodePath := filepath.Join(nodeDir, "node")
			script := "#!/bin/sh\nprintf '%s\\n'\n"
			script = strings.Replace(script, "%s", tt.version, 1)
			if err := os.WriteFile(nodePath, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			activateSystemRuntimeForTest(t, nodeDir)
			t.Setenv("http_proxy", "http://proxy.example")
			// Activation already occurred in the helper; activate once more to
			// check feature defaults against the proxy added for this version.
			rt := activeRuntime()
			if err := activate(rt, filepath.Join(t.TempDir(), "central-again")); err != nil {
				t.Fatal(err)
			}
			_, caSet := os.LookupEnv("NODE_USE_SYSTEM_CA")
			_, proxySet := os.LookupEnv("NODE_USE_ENV_PROXY")
			if caSet != tt.wantCA || proxySet != tt.wantProxy {
				t.Fatalf("Node feature env: systemCA=%q proxy=%q, want enabled=(%v,%v)", os.Getenv("NODE_USE_SYSTEM_CA"), os.Getenv("NODE_USE_ENV_PROXY"), tt.wantCA, tt.wantProxy)
			}
		})
	}
}

func TestSystemNodeCommandDispatchUsesNodeAndLocalCLIs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("command fixture uses POSIX system Node")
	}
	nodeDir := filepath.Join(t.TempDir(), "node with spaces")
	if err := os.MkdirAll(nodeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	nodePath := filepath.Join(nodeDir, "node")
	if err := os.WriteFile(nodePath, []byte("#!/bin/sh\necho v22.20.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	activateSystemRuntimeForTest(t, nodeDir)
	rt := activeRuntime()
	key, _, _ := InstallerCommand()
	ctx := context.Background()
	command := npxCommandFor(ctx, rt, "darwin", key, "skills", "add", "owner/repo", "-g", "-y")
	if command.Path != key || !reflect.DeepEqual(command.Args, []string{key, rt.skillsCLIPath, "add", "owner/repo", "-g", "-y"}) {
		t.Fatalf("system Node Skills command = %#v", command)
	}
	npmCommand := npxCommandFor(ctx, rt, "darwin", key, "@astron-team/skillhub", "search", "go")
	if npmCommand.Path != key || !reflect.DeepEqual(npmCommand.Args, []string{key, rt.npxCLIPath, "-y", "@astron-team/skillhub", "search", "go"}) {
		t.Fatalf("system Node npm command = %#v", npmCommand)
	}
	fake := npxCommandFor(ctx, rt, "darwin", "/fake/npx", "skills", "find", "go")
	if fake.Path != "/fake/npx" || !reflect.DeepEqual(fake.Args, []string{"/fake/npx", "skills", "find", "go"}) {
		t.Fatalf("fake npx command changed: %#v", fake)
	}
}

func TestSystemNodeDoesNotEnableProxyWithoutProxyEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("probe fixture uses a POSIX script")
	}
	nodeDir := filepath.Join(t.TempDir(), "node")
	if err := os.MkdirAll(nodeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nodeDir, "node"), []byte("#!/bin/sh\necho v22.21.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	activateSystemRuntimeForTest(t, nodeDir)
	if got, exists := os.LookupEnv("NODE_USE_ENV_PROXY"); exists {
		t.Fatalf("NODE_USE_ENV_PROXY=%q, want unset without proxy environment", got)
	}
}

func activateSystemRuntimeForTest(t *testing.T, nodePATH string) *runtimeBundle {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("system Node fixtures use POSIX scripts")
	}
	for _, key := range []string{"NODE_USE_SYSTEM_CA", "NODE_USE_ENV_PROXY", "NODE_EXTRA_CA_CERTS", "HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		unsetEnvForTest(t, key)
	}
	prepareActivationEnvForTest(t)
	t.Setenv("PATH", nodePATH)
	app := filepath.Join(t.TempDir(), "Skill Manager.app")
	exe := filepath.Join(app, "Contents", "MacOS", "skillmanager")
	root := runtimeRootForExecutable(exe, "darwin", "arm64")
	if err := os.MkdirAll(filepath.Dir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	fixture := writeSystemRuntimeFixture(t, "darwin-arm64")
	if err := os.Rename(fixture, root); err != nil {
		t.Fatal(err)
	}
	oldActive := activeRuntime()
	t.Cleanup(func() { setActiveRuntime(oldActive) })
	central := filepath.Join(t.TempDir(), "central")
	activated, err := activateFrom(exe, "darwin", "arm64", central)
	if err != nil || !activated {
		t.Fatalf("v2 activation must not fail for a system Node capability issue: activated=%v err=%v", activated, err)
	}
	return activeRuntime()
}
