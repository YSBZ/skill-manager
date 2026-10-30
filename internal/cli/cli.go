// Package cli is the headless client half of SkillManager. It ships INSIDE the
// same binary (no separate program): when the executable is invoked with a known
// subcommand, main() dispatches here instead of starting the daemon/window.
//
// It is a thin HTTP client over the daemon's EXISTING localhost API — it does not
// touch config/manifest directly. So every mutation (enable/disable/move/adopt)
// runs through the same reconcile the GUI uses, and SkillManager's data stays
// consistent whether you act from the window, a script, or a cc/codex hook. It
// reads the daemon's address + bearer token (0600) from the central dir; the
// desktop app / daemon must be running (it now binds a loopback API port).
package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"skillmanage/internal/config"
)

// IsInvocation reports whether os.Args[1] should route to the CLI rather than
// start the daemon/window. A bare word (no leading dash) is ALWAYS treated as a
// subcommand candidate — an unknown one falls through to Run's "未知命令" usage
// error (exit 2) instead of silently launching the app. Help flags route too so
// `-h`/`--help` reach the CLI usage. Dash-prefixed launch args (Finder's -psn_…,
// the daemon's -central / -no-open) are left for the normal startup path.
//
// This replaces an earlier exact-match allowlist: with the allowlist, a typo like
// `skillmanager frobnicate` was NOT recognized and silently booted the daemon
// window with exit 0 — no hint the command was wrong. Routing every bare word to
// the CLI makes typos self-report.
func IsInvocation(argv1 string) bool {
	if argv1 == "-h" || argv1 == "--help" {
		return true
	}
	return !strings.HasPrefix(argv1, "-")
}

// Run executes one subcommand (args = os.Args[1:]) and returns a process exit code.
func Run(args []string) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		usage()
		return 0
	}
	cmd, rest := args[0], args[1:]
	c, err := newClient()
	if err != nil {
		fmt.Fprintln(os.Stderr, "skillmanager:", err)
		return 1
	}
	switch cmd {
	case "list":
		return c.list(rest)
	case "enable":
		return c.enable(rest)
	case "disable":
		return c.disable(rest)
	case "move":
		return c.move(rest)
	case "adopt":
		return c.adopt(rest)
	case "sync":
		return c.sync()
	default:
		fmt.Fprintf(os.Stderr, "skillmanager: 未知命令 %q\n\n", cmd)
		usage()
		return 2
	}
}

func usage() {
	fmt.Print(`skillmanager — 无 GUI 驱动同一个运行中的 SkillManager（数据自动保持一致）

用法:
  skillmanager list [targets|repos|enabled|<repo名>] [--count]   查看(或仅计数)目标/源/已启用/某源的 skill
  skillmanager enable  <选择器> --target <目录> [--mode snapshot|follow]
  skillmanager disable <选择器> --target <目录>
  skillmanager move    <skill名> --from <源> --to <repo名|local> [--desc ...]
  skillmanager adopt   <id> --root <目录> [--desc ...]   收编目标目录里未备份的手写 skill
  skillmanager sync                                      重新对齐软链（reconcile）

选择器示例: @agents/foo、fe-skills/bar、@local/baz、@repo/*（整源）
前提: SkillManager（桌面 App 或守护进程）正在运行。
`)
}

type client struct {
	base  string
	token string
	http  *http.Client
}

func newClient() (*client, error) {
	dir, err := config.DefaultCentralDir()
	if err != nil {
		return nil, err
	}
	tok, err := os.ReadFile(config.TokenPath(dir))
	if err != nil {
		return nil, fmt.Errorf("读取 token 失败（SkillManager 没运行过？）: %w", err)
	}
	addr, err := os.ReadFile(config.AddressPath(dir))
	if err != nil {
		return nil, fmt.Errorf("找不到运行地址，请先启动 SkillManager: %w", err)
	}
	base := strings.TrimRight(strings.TrimSpace(string(addr)), "/")
	return &client{base: base, token: strings.TrimSpace(string(tok)), http: &http.Client{Timeout: 120 * time.Second}}, nil
}

// do calls the daemon API. On a connection error it reports the daemon as down.
func (c *client) do(method, path string, body any) ([]byte, int, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, rdr)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("连不上 SkillManager（是否已启动？）: %w", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return out, resp.StatusCode, nil
}

// ok runs a mutating call and prints a one-line result; returns exit code.
func (c *client) ok(method, path string, body any, okMsg string) int {
	out, code, err := c.do(method, path, body)
	if err != nil {
		fmt.Fprintln(os.Stderr, "skillmanager:", err)
		return 1
	}
	if code < 200 || code >= 300 {
		fmt.Fprintf(os.Stderr, "skillmanager: 失败（HTTP %d）: %s\n", code, strings.TrimSpace(string(out)))
		return 1
	}
	fmt.Println(okMsg)
	return 0
}

// --- commands ---

func (c *client) enable(args []string) int {
	pos, fl := parse(args)
	if len(pos) < 1 || fl["target"] == "" {
		fmt.Fprintln(os.Stderr, "用法: skillmanager enable <选择器> --target <目录> [--mode snapshot|follow]")
		return 2
	}
	mode := fl["mode"]
	if mode == "" {
		mode = "snapshot"
	}
	// Validate the selector resolves to a real skill BEFORE enabling, so a typo
	// (e.g. fe-skills/does-not-exist) is rejected here instead of being written
	// into config as a dangling entry — the /api/enabled endpoint itself does no
	// existence check (the GUI only ever sends selectors it listed).
	if msg := c.validateSelector(pos[0]); msg != "" {
		fmt.Fprintln(os.Stderr, "skillmanager:", msg)
		return 1
	}
	rc := c.ok("POST", "/api/enabled", map[string]string{"skill": pos[0], "target": fl["target"], "mode": mode}, "已启用 "+pos[0]+" → "+fl["target"])
	if rc == 0 {
		c.do("POST", "/api/apply", nil) // materialize the link now
	}
	return rc
}

// validateSelector best-effort checks a "<源>/<skill>" selector before enabling.
// It refuses ONLY when it can positively list the source AND the named skill is
// absent (the typo case). Cases it can't decide are passed through untouched:
//   - a whole-source follow ("<源>/*") — the server validates the source;
//   - a source /api/skills can't enumerate (e.g. @skillhub) — returns non-200,
//     so we don't guess; the server/reconcile owns that namespace.
// This keeps the check from ever rejecting a legitimate selector while still
// catching the common mistake of enabling a skill name that doesn't exist.
func (c *client) validateSelector(sel string) string {
	i := strings.IndexByte(sel, '/')
	if i <= 0 || i == len(sel)-1 {
		return "选择器格式应为 <源>/<skill>，如 @agents/foo、fe-skills/bar、@local/baz、<源>/*（整源）"
	}
	ns, name := sel[:i], sel[i+1:]
	if name == "*" {
		return "" // whole-source follow — let the server validate the source
	}
	out, code, err := c.do("GET", "/api/skills?repo="+urlEscape(ns), nil)
	if err != nil || code != 200 {
		return "" // source not enumerable here (unknown / @skillhub) — let server decide
	}
	var skills []struct{ LinkName, LogicalName string }
	_ = json.Unmarshal(out, &skills)
	for _, s := range skills {
		if s.LinkName == name || s.LogicalName == name {
			return ""
		}
	}
	return fmt.Sprintf("源 %q 里没有名为 %q 的 skill（用 `skillmanager list %s` 看可用项）", ns, name, ns)
}

func (c *client) disable(args []string) int {
	pos, fl := parse(args)
	if len(pos) < 1 || fl["target"] == "" {
		fmt.Fprintln(os.Stderr, "用法: skillmanager disable <选择器> --target <目录>")
		return 2
	}
	rc := c.ok("DELETE", "/api/enabled", map[string]string{"skill": pos[0], "target": fl["target"]}, "已停用 "+pos[0]+" ← "+fl["target"])
	if rc == 0 {
		c.do("POST", "/api/apply", nil)
	}
	return rc
}

func (c *client) move(args []string) int {
	pos, fl := parse(args)
	if len(pos) < 1 || fl["from"] == "" || fl["to"] == "" {
		fmt.Fprintln(os.Stderr, "用法: skillmanager move <skill名> --from <源> --to <repo名|local> [--desc ...]")
		return 2
	}
	return c.ok("POST", "/api/move",
		map[string]string{"name": pos[0], "fromRepo": fl["from"], "toRepo": fl["to"], "description": fl["desc"]},
		"已移动 "+pos[0]+"："+fl["from"]+" → "+fl["to"])
}

func (c *client) adopt(args []string) int {
	pos, fl := parse(args)
	if len(pos) < 1 || fl["root"] == "" {
		fmt.Fprintln(os.Stderr, "用法: skillmanager adopt <id> --root <目录> [--desc ...]")
		return 2
	}
	return c.ok("POST", "/api/adopt",
		map[string]string{"id": pos[0], "root": fl["root"], "description": fl["desc"]},
		"已备份（收编）"+pos[0])
}

func (c *client) sync() int {
	return c.ok("POST", "/api/apply", nil, "已重新对齐软链")
}

func (c *client) list(args []string) int {
	pos, fl := parse(args)
	what := "overview"
	if len(pos) > 0 {
		what = pos[0]
	}
	countOnly := fl["count"] != "" // --count → print the number only
	st, code, err := c.do("GET", "/api/status", nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "skillmanager:", err)
		return 1
	}
	if code != 200 {
		fmt.Fprintf(os.Stderr, "skillmanager: 读状态失败（HTTP %d）\n", code)
		return 1
	}
	var status struct {
		Repos []struct {
			Name, Alias, State string
		} `json:"repos"`
		Enabled []struct {
			Skill, Target, Mode string
		} `json:"enabled"`
		Targets []string `json:"targets"`
	}
	_ = json.Unmarshal(st, &status)
	switch what {
	case "targets":
		if countOnly {
			fmt.Println(len(status.Targets))
			return 0
		}
		for _, t := range status.Targets {
			fmt.Println(t)
		}
	case "repos":
		if countOnly {
			fmt.Println(len(status.Repos))
			return 0
		}
		for _, r := range status.Repos {
			name := r.Name
			if r.Alias != "" {
				name = r.Alias + " (" + r.Name + ")"
			}
			fmt.Printf("%-32s %s\n", name, r.State)
		}
	case "enabled":
		if countOnly {
			fmt.Println(len(status.Enabled))
			return 0
		}
		for _, e := range status.Enabled {
			fmt.Printf("%-40s → %s\n", e.Skill, e.Target)
		}
	case "overview":
		fmt.Printf("目标 %d · git 仓 %d · 已启用 %d\n", len(status.Targets), len(status.Repos), len(status.Enabled))
		fmt.Println("（list targets|repos|enabled|<repo名> [--count] 看明细）")
	default:
		// treat `what` as a repo/source name → its skills
		sk, sc, _ := c.do("GET", "/api/skills?repo="+urlEscape(what), nil)
		if sc != 200 {
			fmt.Fprintf(os.Stderr, "skillmanager: 未知源 %q（HTTP %d）\n", what, sc)
			return 1
		}
		var skills []struct{ LinkName, LogicalName, Description string }
		_ = json.Unmarshal(sk, &skills)
		if countOnly {
			fmt.Println(len(skills))
			return 0
		}
		for _, s := range skills {
			n := s.LinkName
			if n == "" {
				n = s.LogicalName
			}
			// one line per skill (descriptions can carry newlines → uncountable text)
			fmt.Printf("%-32s %s\n", n, oneLine(s.Description))
		}
	}
	return 0
}

// oneLine collapses all whitespace (incl. newlines) to single spaces and truncates,
// so `list <repo>` prints exactly one line per skill — greppable and countable.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 100 {
		s = string(r[:99]) + "…"
	}
	return s
}

// parse splits args into positionals and --key value / --key=value flags.
func parse(args []string) (pos []string, fl map[string]string) {
	fl = map[string]string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "--") {
			k := strings.TrimPrefix(a, "--")
			if eq := strings.IndexByte(k, '='); eq >= 0 {
				fl[k[:eq]] = k[eq+1:]
				continue
			}
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				fl[k] = args[i+1]
				i++
			} else {
				fl[k] = "true"
			}
			continue
		}
		pos = append(pos, a)
	}
	return pos, fl
}

func urlEscape(s string) string {
	// minimal: repo/source names are slug-like; escape only what matters here.
	r := strings.NewReplacer(" ", "%20", "@", "%40", "/", "%2F", "#", "%23", "?", "%3F", "&", "%26")
	return r.Replace(s)
}
