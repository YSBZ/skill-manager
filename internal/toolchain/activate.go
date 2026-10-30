package toolchain

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ActivateDefault selects a platform runtime beside the running desktop app.
// Development binaries without a runtime directory keep using system tools.
// An existing but invalid bundle is an error, so startup cannot mix bundled
// Node with system Git or silently accept a partial package.
func ActivateDefault(centralDir string) error {
	executablePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate application executable: %w", err)
	}
	_, err = activateFrom(executablePath, runtime.GOOS, runtime.GOARCH, centralDir)
	return err
}

func activateFrom(executablePath, goos, goarch, centralDir string) (bool, error) {
	root := runtimeRootForExecutable(executablePath, goos, goarch)
	if root == "" {
		return false, nil
	}
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		parentInfo, parentErr := os.Lstat(filepath.Dir(root))
		if errors.Is(parentErr, os.ErrNotExist) {
			return false, nil
		}
		if parentErr != nil {
			return false, fmt.Errorf("inspect application runtime directory: %w", parentErr)
		}
		if !parentInfo.IsDir() {
			return false, errors.New("application runtime parent is not a directory")
		}
		return false, fmt.Errorf("application runtime platform directory %q is missing", filepath.Base(root))
	}
	if err != nil {
		return false, fmt.Errorf("inspect application runtime: %w", err)
	}
	if !info.IsDir() {
		return false, errors.New("application runtime path is not a directory")
	}

	rt, err := loadRuntime(root, runtimePlatform(goos, goarch))
	if err != nil {
		return false, fmt.Errorf("invalid application runtime: %w", err)
	}
	if rt.schemaVersion == manifestSchemaV2 {
		rt.nodePath, rt.nodeDependency, rt.nodeVersion = inspectSystemNode(goos)
	}
	if err := activate(rt, centralDir); err != nil {
		return false, err
	}
	setActiveRuntime(rt)
	return true, nil
}

func activate(rt *runtimeBundle, centralDir string) error {
	if centralDir == "" {
		return errors.New("central directory is required to configure the npm cache")
	}
	centralDir, err := filepath.Abs(centralDir)
	if err != nil {
		return fmt.Errorf("resolve central directory: %w", err)
	}
	npmCache := filepath.Join(centralDir, "cache", "npm")
	if err := os.MkdirAll(npmCache, 0o700); err != nil {
		return fmt.Errorf("create private npm cache: %w", err)
	}
	if err := os.Chmod(npmCache, 0o700); err != nil {
		return fmt.Errorf("restrict npm cache permissions: %w", err)
	}

	mergedPath := prependPath(rt.path, filepath.SplitList(os.Getenv("PATH")))
	if err := os.Setenv("PATH", strings.Join(mergedPath, string(os.PathListSeparator))); err != nil {
		return fmt.Errorf("activate bundled runtime PATH: %w", err)
	}
	if err := os.Setenv("npm_config_cache", npmCache); err != nil {
		return fmt.Errorf("configure private npm cache: %w", err)
	}
	if err := os.Setenv("DISABLE_TELEMETRY", "1"); err != nil {
		return fmt.Errorf("disable Skills CLI telemetry: %w", err)
	}
	nodeVersion, hasNode := nodeVersionForActivation(rt)
	if hasNode && nodeSupportsSystemCA(nodeVersion) {
		if err := setDefaultEnv("NODE_USE_SYSTEM_CA", "1"); err != nil {
			return fmt.Errorf("enable Node system certificate store: %w", err)
		}
	}
	if hasNode && nodeSupportsEnvironmentProxy(nodeVersion) && hasHTTPProxyEnvironment() {
		if err := setDefaultEnv("NODE_USE_ENV_PROXY", "1"); err != nil {
			return fmt.Errorf("enable Node environment proxy support: %w", err)
		}
	}
	for _, setting := range [][2]string{
		{"GIT_EXEC_PATH", rt.gitExecPath},
		{"GIT_CONFIG_SYSTEM", rt.gitConfig},
		{"GIT_TEMPLATE_DIR", rt.gitTemplate},
	} {
		if err := setDefaultEnv(setting[0], setting[1]); err != nil {
			return fmt.Errorf("configure bundled %s: %w", setting[0], err)
		}
	}
	if rt.caBundle != "" {
		_, hasCAInfo := os.LookupEnv("GIT_SSL_CAINFO")
		_, hasCAPath := os.LookupEnv("GIT_SSL_CAPATH")
		if !hasCAInfo && !hasCAPath {
			if err := setDefaultEnv("GIT_SSL_CAINFO", rt.caBundle); err != nil {
				return fmt.Errorf("configure bundled Git CA bundle: %w", err)
			}
		}
	}
	return nil
}

func hasHTTPProxyEnvironment() bool {
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		if os.Getenv(key) != "" {
			return true
		}
	}
	return false
}

func setDefaultEnv(key, value string) error {
	if value == "" {
		return nil
	}
	if _, exists := os.LookupEnv(key); !exists {
		return os.Setenv(key, value)
	}
	return nil
}

func prependPath(front, existing []string) []string {
	seen := make(map[string]bool, len(front)+len(existing))
	out := make([]string, 0, len(front)+len(existing))
	for _, list := range [][]string{front, existing} {
		for _, item := range list {
			if item != "" && !seen[item] {
				seen[item] = true
				out = append(out, item)
			}
		}
	}
	return out
}
