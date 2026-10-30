package toolchain

import (
	"context"
	"os/exec"
	"runtime"
	"time"
)

const waitDelay = 2 * time.Second

// NpxCommand builds the process for an npx-style invocation. For a matching
// managed command key, skills use the local Skills CLI and other commands use
// the bundled npm npx entry point. Unmanaged and fake command paths preserve
// the existing platform-specific behavior.
func NpxCommand(ctx context.Context, npxPath string, args ...string) *exec.Cmd {
	return npxCommandFor(ctx, activeRuntime(), runtime.GOOS, npxPath, args...)
}

func npxCommandFor(ctx context.Context, rt *runtimeBundle, goos, npxPath string, args ...string) *exec.Cmd {
	if rt != nil && platformOS(rt.platform) == goos && matchesManagedInstaller(rt, npxPath, goos) {
		if len(args) > 0 && args[0] == "skills" {
			return newCommand(ctx, rt.nodePath, append([]string{rt.skillsCLIPath}, args[1:]...)...)
		}
		npxArgs := append([]string{rt.npxCLIPath}, ensureNpxConfirmation(args)...)
		return newCommand(ctx, rt.nodePath, npxArgs...)
	}
	return legacyNpxCommand(ctx, goos, npxPath, args...)
}

func matchesManagedInstaller(rt *runtimeBundle, commandPath, goos string) bool {
	if commandPath == "" {
		return false
	}
	switch rt.schemaVersion {
	case manifestSchemaV1:
		return samePath(commandPath, rt.npxPath, goos)
	case manifestSchemaV2:
		return rt.nodeDependency != nil && rt.nodeDependency.State == "ready" && rt.nodePath != "" && samePath(commandPath, rt.nodePath, goos)
	default:
		return false
	}
}

func ensureNpxConfirmation(args []string) []string {
	for _, arg := range args {
		if arg == "-y" || arg == "--yes" {
			return args
		}
	}
	confirmed := make([]string, 0, len(args)+1)
	confirmed = append(confirmed, "-y")
	return append(confirmed, args...)
}

func newCommand(ctx context.Context, path string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.WaitDelay = waitDelay
	configureCancellation(cmd)
	return cmd
}

func legacyNpxCommand(ctx context.Context, goos, npxPath string, args ...string) *exec.Cmd {
	if goos == "windows" {
		return newCommand(ctx, "cmd", append([]string{"/c", npxPath}, args...)...)
	}
	return newCommand(ctx, npxPath, args...)
}
