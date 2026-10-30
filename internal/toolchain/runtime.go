// Package toolchain selects and configures the optional per-app Node and Git
// runtime shipped in desktop application resources.
package toolchain

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
)

const (
	manifestFilename   = "runtime.json"
	manifestSchemaV1   = 1
	manifestSchemaV2   = 2
	bundledNodeVersion = "26.13.0"
	systemNodeMode     = "system"
	systemNpmVersion   = "11.20.0"
	systemNpxCLIPath   = "npm/bin/npx-cli.js"
	skillsCLIPath      = "skills/node_modules/skills/dist/cli.mjs"
	gitVersion         = "2.56.0"
	skillsVersion      = "1.7.4"
)

type manifest struct {
	SchemaVersion   int      `json:"schemaVersion"`
	Platform        string   `json:"platform"`
	NodeMode        string   `json:"nodeMode,omitempty"`
	Node            string   `json:"node"`
	Npx             string   `json:"npx"`
	NpxCLI          string   `json:"npxCLI"`
	SkillsCLI       string   `json:"skillsCLI"`
	Git             string   `json:"git"`
	GitExecPath     string   `json:"gitExecPath"`
	GitConfigSystem string   `json:"gitConfigSystem,omitempty"`
	GitTemplateDir  string   `json:"gitTemplateDir,omitempty"`
	CABundle        string   `json:"caBundle,omitempty"`
	Path            []string `json:"path"`
	Versions        struct {
		Node   string `json:"node"`
		Npm    string `json:"npm"`
		Git    string `json:"git"`
		Skills string `json:"skills"`
	} `json:"versions"`
}

type runtimeBundle struct {
	root           string
	platform       string
	schemaVersion  int
	nodeMode       string
	nodePath       string
	npxPath        string
	npxCLIPath     string
	skillsCLIPath  string
	gitPath        string
	gitExecPath    string
	gitConfig      string
	gitTemplate    string
	caBundle       string
	path           []string
	nodeVersion    parsedNodeVersion
	nodeDependency *NodeDependency
}

func loadRuntime(root, platform string) (*runtimeBundle, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve runtime root: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolve runtime root: %w", err)
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("inspect runtime root: %w", err)
	}
	if !rootInfo.IsDir() {
		return nil, errors.New("runtime root is not a directory")
	}

	manifestPath, err := resolveWithin(root, manifestFilename)
	if err != nil {
		return nil, fmt.Errorf("resolve runtime manifest: %w", err)
	}
	manifestInfo, err := os.Stat(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("inspect runtime manifest: %w", err)
	}
	if !manifestInfo.Mode().IsRegular() {
		return nil, errors.New("runtime manifest is not a regular file")
	}
	file, err := os.Open(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("open runtime manifest: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var cfg manifest
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode runtime manifest: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New("runtime manifest contains multiple JSON values")
		}
		return nil, fmt.Errorf("decode runtime manifest trailer: %w", err)
	}
	if cfg.Platform != platform {
		return nil, fmt.Errorf("runtime platform %q does not match %q", cfg.Platform, platform)
	}
	switch cfg.SchemaVersion {
	case manifestSchemaV1:
		if cfg.NodeMode != "" {
			return nil, fmt.Errorf("runtime manifest v1 cannot specify nodeMode %q", cfg.NodeMode)
		}
		if cfg.Versions.Node != bundledNodeVersion || cfg.Versions.Git != gitVersion || cfg.Versions.Skills != skillsVersion {
			return nil, fmt.Errorf("unsupported v1 runtime versions: node=%q git=%q skills=%q", cfg.Versions.Node, cfg.Versions.Git, cfg.Versions.Skills)
		}
	case manifestSchemaV2:
		if cfg.NodeMode != systemNodeMode {
			return nil, fmt.Errorf("runtime manifest v2 requires nodeMode %q", systemNodeMode)
		}
		if cfg.Node != "" || cfg.Npx != "" {
			return nil, errors.New("runtime manifest v2 cannot specify bundled node or npx paths")
		}
		if cfg.NpxCLI != systemNpxCLIPath || cfg.SkillsCLI != skillsCLIPath {
			return nil, errors.New("runtime manifest v2 contains unexpected npm or Skills CLI paths")
		}
		if cfg.Versions.Node != "system" || cfg.Versions.Npm != systemNpmVersion || cfg.Versions.Git != gitVersion || cfg.Versions.Skills != skillsVersion {
			return nil, fmt.Errorf("unsupported v2 runtime versions: node=%q npm=%q git=%q skills=%q", cfg.Versions.Node, cfg.Versions.Npm, cfg.Versions.Git, cfg.Versions.Skills)
		}
	default:
		return nil, fmt.Errorf("unsupported runtime manifest schema %d", cfg.SchemaVersion)
	}

	rt := &runtimeBundle{root: root, platform: platform, schemaVersion: cfg.SchemaVersion, nodeMode: cfg.NodeMode}
	var errPath error
	if cfg.SchemaVersion == manifestSchemaV1 {
		if rt.nodePath, errPath = validateExecutable(root, cfg.Node, platform, "node"); errPath != nil {
			return nil, errPath
		}
		if rt.npxPath, errPath = validateExecutable(root, cfg.Npx, platform, "npx"); errPath != nil {
			return nil, errPath
		}
		rt.nodeVersion, errPath = parseNodeVersion(cfg.Versions.Node)
		if errPath != nil {
			return nil, fmt.Errorf("invalid bundled Node version: %w", errPath)
		}
	}
	if rt.npxCLIPath, errPath = validateFile(root, cfg.NpxCLI, "npxCLI"); errPath != nil {
		return nil, errPath
	}
	if rt.skillsCLIPath, errPath = validateFile(root, cfg.SkillsCLI, "skillsCLI"); errPath != nil {
		return nil, errPath
	}
	if rt.gitPath, errPath = validateExecutable(root, cfg.Git, platform, "git"); errPath != nil {
		return nil, errPath
	}
	if rt.gitExecPath, errPath = validateDirectory(root, cfg.GitExecPath, "gitExecPath"); errPath != nil {
		return nil, errPath
	}
	if cfg.GitConfigSystem != "" {
		if rt.gitConfig, errPath = validateFile(root, cfg.GitConfigSystem, "gitConfigSystem"); errPath != nil {
			return nil, errPath
		}
	}
	if cfg.GitTemplateDir != "" {
		if rt.gitTemplate, errPath = validateDirectory(root, cfg.GitTemplateDir, "gitTemplateDir"); errPath != nil {
			return nil, errPath
		}
	}
	if cfg.CABundle != "" {
		if rt.caBundle, errPath = validateFile(root, cfg.CABundle, "caBundle"); errPath != nil {
			return nil, errPath
		}
	}
	if len(cfg.Path) == 0 {
		return nil, errors.New("runtime manifest PATH is empty")
	}
	seen := make(map[string]bool, len(cfg.Path))
	for _, rel := range cfg.Path {
		if cfg.SchemaVersion == manifestSchemaV2 {
			clean := path.Clean(rel)
			if clean != "git" && !strings.HasPrefix(clean, "git/") {
				return nil, fmt.Errorf("v2 runtime PATH entry %q is outside the Git runtime", rel)
			}
		}
		resolved, err := validateDirectory(root, rel, "path entry")
		if err != nil {
			return nil, err
		}
		key := resolved
		if platformOS(platform) == "windows" {
			key = strings.ToLower(key)
		}
		if seen[key] {
			return nil, fmt.Errorf("duplicate runtime PATH entry %q", rel)
		}
		seen[key] = true
		rt.path = append(rt.path, resolved)
	}
	type commandPath struct {
		field string
		path  string
	}
	commands := []commandPath{
		{field: "git", path: cfg.Git},
	}
	if cfg.SchemaVersion == manifestSchemaV1 {
		commands = append(commands,
			commandPath{field: "node", path: cfg.Node},
			commandPath{field: "npx", path: cfg.Npx},
		)
	}
	for _, command := range commands {
		dir, err := validateDirectory(root, path.Dir(path.Clean(command.path)), command.field+" executable directory")
		if err != nil {
			return nil, err
		}
		key := dir
		if platformOS(platform) == "windows" {
			key = strings.ToLower(key)
		}
		if !seen[key] {
			return nil, fmt.Errorf("runtime PATH does not contain the %s executable directory", command.field)
		}
	}
	return rt, nil
}

func validateExecutable(root, rel, platform, field string) (string, error) {
	resolved, err := resolveWithin(root, rel)
	if err != nil {
		return "", fmt.Errorf("invalid runtime %s path: %w", field, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspect runtime %s: %w", field, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("runtime %s is not a regular file", field)
	}
	if platformOS(platform) == "windows" {
		ext := strings.ToLower(filepath.Ext(rel))
		if field == "npx" && ext != ".cmd" {
			return "", errors.New("Windows runtime npx must be a .cmd shim")
		}
		if (field == "node" || field == "git") && ext != ".exe" {
			return "", fmt.Errorf("Windows runtime %s must be an .exe", field)
		}
	} else if info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("runtime %s is not executable", field)
	}
	return resolved, nil
}

func validateFile(root, rel, field string) (string, error) {
	resolved, err := resolveWithin(root, rel)
	if err != nil {
		return "", fmt.Errorf("invalid runtime %s path: %w", field, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspect runtime %s: %w", field, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("runtime %s is not a regular file", field)
	}
	return resolved, nil
}

func validateDirectory(root, rel, field string) (string, error) {
	resolved, err := resolveWithin(root, rel)
	if err != nil {
		return "", fmt.Errorf("invalid runtime %s path: %w", field, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspect runtime %s: %w", field, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("runtime %s is not a directory", field)
	}
	return resolved, nil
}

// resolveWithin resolves a slash-separated manifest path and any symlink chain,
// requiring the final object to remain inside root.
func resolveWithin(root, rel string) (string, error) {
	if rel == "" || strings.ContainsRune(rel, '\\') || strings.ContainsRune(rel, '\x00') || strings.Contains(rel, ":") || path.IsAbs(rel) {
		return "", errors.New("path must be a non-empty relative slash path")
	}
	clean := path.Clean(rel)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("path escapes runtime root")
	}
	full := filepath.Join(root, filepath.FromSlash(clean))
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		return "", err
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", errors.New("symlink resolves outside runtime root")
	}
	return resolved, nil
}

func platformOS(platform string) string {
	if i := strings.IndexByte(platform, '-'); i >= 0 {
		return platform[:i]
	}
	return ""
}

func runtimeRootForExecutable(executablePath, goos, goarch string) string {
	if resolved, err := filepath.EvalSymlinks(executablePath); err == nil {
		executablePath = resolved
	}
	platform := goos + "-" + goarch
	switch goos {
	case "darwin":
		exeDir := filepath.Dir(executablePath)
		if filepath.Base(exeDir) != "MacOS" || filepath.Base(filepath.Dir(exeDir)) != "Contents" {
			return ""
		}
		return filepath.Join(filepath.Dir(exeDir), "Resources", "runtime", platform)
	case "windows":
		return filepath.Join(filepath.Dir(executablePath), "runtime", platform)
	default:
		return ""
	}
}

func canonicalForCompare(path string) string {
	abs, err := filepath.Abs(path)
	if err == nil {
		path = abs
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return filepath.Clean(path)
}

func samePath(a, b string, goos string) bool {
	a, b = canonicalForCompare(a), canonicalForCompare(b)
	if goos == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func runtimePlatform(goos, goarch string) string { return goos + "-" + goarch }

var activeState struct {
	mu sync.RWMutex
	rt *runtimeBundle
}

func activeRuntime() *runtimeBundle {
	activeState.mu.RLock()
	defer activeState.mu.RUnlock()
	return activeState.rt
}

func setActiveRuntime(rt *runtimeBundle) {
	activeState.mu.Lock()
	activeState.rt = rt
	activeState.mu.Unlock()
}
