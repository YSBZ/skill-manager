//go:build windows

package server

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"syscall"

	"skillmanage/internal/toolchain"
)

// defaultSkillsRunner preserves the legacy `cmd /c` npx.cmd path when no runtime
// bundle is active; bundled commands use Node directly. HideWindow +
// CREATE_NO_WINDOW keep children of the windowless app from flashing a console.
func defaultSkillsRunner() skillsRunner { return windowsRunner{} }

const createNoWindow = 0x08000000 // CREATE_NO_WINDOW

type windowsRunner struct{}

func (windowsRunner) UpdateSkill(ctx context.Context, npxPath, name string) (string, string, error) {
	// name is allowlist-validated by the caller before it reaches either the
	// bundled Node process or the legacy cmd /c npx.cmd path.
	// -g --yes: global scope + skip the interactive prompt (daemon has no tty).
	cmd := toolchain.NpxCommand(ctx, npxPath, "skills", "update", name, "-g", "--yes")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

// SkillsAddURL runs `npx skills add <repoURL> --skill <name> -g -y -a universal`
// (skillsmp.com install form). repoURL/skill are allowlist-validated by caller.
func (windowsRunner) SkillsAddURL(ctx context.Context, npxPath, repoURL, skill string) (string, string, error) {
	cmd := toolchain.NpxCommand(ctx, npxPath, "skills", "add", repoURL, "--skill", skill, "-g", "-y", "-a", "universal")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

// SkillsMpFind fetches a skillsmp.com search URL with curl (Cloudflare 403s Go's
// TLS fingerprint). curl.exe (Windows 10+) is a real exe, so we exec it DIRECTLY —
// NOT via `cmd /c` — otherwise cmd's %VAR% expansion would mangle the %header{}
// in the -w writeout. HideWindow/CreateNoWindow still suppress any console flash.
func (windowsRunner) SkillsMpFind(ctx context.Context, url, apiKey string) (string, string, error) {
	curl, err := exec.LookPath("curl")
	if err != nil {
		return "", "需要 curl 才能搜索 skillsmp", err
	}
	args := []string{"-fsS", "--max-time", "20", "-H", "Accept: application/json", "-H", "User-Agent: " + skillsMpUserAgent}
	var stdin *strings.Reader
	if apiKey != "" {
		// API key via curl config on stdin (not argv) — keeps the secret out of process listings.
		args = append(args, "-K", "-")
		stdin = strings.NewReader("header = \"Authorization: Bearer " + apiKey + "\"\n")
	}
	args = append(args, "-w", skillsMpRateWriteout, url) // capture daily quota headers after the body
	cmd := exec.CommandContext(ctx, curl, args...)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
	return out.String(), errb.String(), err
}

// UpdateAll runs `npx skills update -g --yes` (no skill arg → updates everything).
func (windowsRunner) UpdateAll(ctx context.Context, npxPath string) (string, string, error) {
	cmd := toolchain.NpxCommand(ctx, npxPath, "skills", "update", "-g", "--yes")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

// UpdatePlugin delegates to `claude plugin update <plugin> -s <scope>` via cmd /c
// (claude is a .cmd shim on Windows). plugin/scope validated by the caller.
func (windowsRunner) UpdatePlugin(ctx context.Context, cliPath, plugin, scope string) (string, string, error) {
	cmd := exec.CommandContext(ctx, "cmd", "/c", cliPath, "plugin", "update", plugin, "-s", scope)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

// ListPlugins runs `claude plugin list --json` (local, no marketplace) via cmd /c.
func (windowsRunner) ListPlugins(ctx context.Context, cliPath string) (string, string, error) {
	cmd := exec.CommandContext(ctx, "cmd", "/c", cliPath, "plugin", "list", "--json")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

// ListMarketplaces runs `claude plugin marketplace list --json` via cmd /c.
func (windowsRunner) ListMarketplaces(ctx context.Context, cliPath string) (string, string, error) {
	cmd := exec.CommandContext(ctx, "cmd", "/c", cliPath, "plugin", "marketplace", "list", "--json")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

// SkillsFind runs `npx skills find <query>`. query is caller-guarded
// (non-empty, no leading "-").
func (windowsRunner) SkillsFind(ctx context.Context, npxPath, query string) (string, string, error) {
	cmd := toolchain.NpxCommand(ctx, npxPath, "skills", "find", query)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

// SkillsAdd runs `npx skills add <pkg> -g -y -a universal` — global,
// non-interactive, canonical-only (no harness symlinks; see runner_unix.go for the
// flag rationale). pkg is caller-validated (owner/repo@skill form).
func (windowsRunner) SkillsAdd(ctx context.Context, npxPath, pkg string) (string, string, error) {
	cmd := toolchain.NpxCommand(ctx, npxPath, "skills", "add", pkg, "-g", "-y", "-a", "universal")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

// SkillsRemove runs `npx skills remove <name> -g -y` — no -a, which
// auto-targets all agents + the global canonical (verified). name caller-validated.
func (windowsRunner) SkillsRemove(ctx context.Context, npxPath, name string) (string, string, error) {
	cmd := toolchain.NpxCommand(ctx, npxPath, "skills", "remove", name, "-g", "-y")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

// --- SkillHub (npx @astron-team/skillhub) ---
// No registry/token: download-only from the CLI's built-in public registry.

func (windowsRunner) SkillHubSearch(ctx context.Context, npxPath, query string) (string, string, error) {
	cmd := toolchain.NpxCommand(ctx, npxPath, "-y", "@astron-team/skillhub", "search", query, "--json")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

func (windowsRunner) SkillHubInstall(ctx context.Context, npxPath, slug, dir string) (string, string, error) {
	cmd := toolchain.NpxCommand(ctx, npxPath, "-y", "@astron-team/skillhub", "install", slug, "--dir", dir, "--force", "--json")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

func (windowsRunner) SkillHubRemove(ctx context.Context, npxPath, slug, dir string) (string, string, error) {
	cmd := toolchain.NpxCommand(ctx, npxPath, "-y", "@astron-team/skillhub", "remove", slug, "--dir", dir, "--json")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}
