// Package harness resolves the set of agent skill directories SkillManage can
// link into ("targets"), classifies them by agent (harness) and scope, and
// guards directories the daemon must never write to. It is a leaf package with
// no SkillManage dependencies so reconcile/server can import it without cycles.
//
// Phase 2 turns SkillManage from a single-harness (Claude Code) linker into a
// cross-harness mapping layer: the same skill source can map into both Claude
// Code's and Codex's skill directories. Harness identity is carried by the
// target directory path (KTD1) — there is no persisted harness field — so this
// package centralizes the path↔harness knowledge that would otherwise scatter
// across the server and UI.
package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Harness identifies an agent that consumes skills.
type Harness string

const (
	HarnessClaudeCode Harness = "cc"
	HarnessCodex      Harness = "codex"
	// HarnessUnknown is a directory we cannot tie to a known agent by its path.
	// It is NOT defaulted to cc — the label stays "unknown" so the user is not
	// misled into thinking an arbitrary directory is a Claude Code dir.
	HarnessUnknown Harness = "unknown"
)

// Classify infers the consuming agent from a directory path: codex for
// .codex/.agents skill dirs (or under $CODEX_HOME/skills), cc for .claude skill
// dirs, otherwise unknown. cc is NOT the catch-all default.
func Classify(dir string) Harness {
	switch {
	case IsCodexTarget(dir):
		return HarnessCodex
	case isCCTarget(dir):
		return HarnessClaudeCode
	default:
		return HarnessUnknown
	}
}

// isCCTarget reports whether dir is a Claude Code skills directory: the personal
// ~/.claude/skills, or any */.claude/skills (project-level).
func isCCTarget(dir string) bool {
	r := expand(dir)
	if home, err := os.UserHomeDir(); err == nil {
		if underOrEqual(r, filepath.Join(home, ".claude", "skills")) {
			return true
		}
	}
	return strings.HasSuffix(r, filepath.FromSlash("/.claude/skills"))
}

// DiscoverDefaultTargets returns the conventional agent skill directories that
// actually exist on this machine, in canonical form. It probes the default
// install locations (Claude Code's ~/.claude/skills and Codex's
// $CODEX_HOME|~/.codex /skills) by existence — nothing is hardcoded into config,
// and a location that isn't present is simply left for the user to add manually.
func DiscoverDefaultTargets() []string {
	var out []string
	if home, err := os.UserHomeDir(); err == nil {
		if dirExists(filepath.Join(home, ".claude", "skills")) {
			out = append(out, "~/.claude/skills/")
		}
	}
	if dirExists(codexSkillsRoot()) {
		if os.Getenv("CODEX_HOME") != "" {
			out = append(out, codexSkillsRoot()) // abs form when overridden
		} else {
			out = append(out, "~/.codex/skills/")
		}
	}
	return out
}

// DiscoverDefaultDirectorySources returns the conventional cross-tool directory
// sources that actually exist on this machine, in canonical "~"-relative form.
// Currently that is the Agent Skills convention ~/.agents/skills — where
// skills.sh (npx skills) installs and other compliant tools share skills (phase
// 3 R1.4). These are recognized READ-ONLY: they are never link targets, so they
// are kept separate from DiscoverDefaultTargets. A location that isn't present
// is left for the user to register manually (R2.3); nothing is hardcoded into
// config. WSL needs no special handling — os.UserHomeDir() already returns the
// WSL distro's own home, so we only ever look at the current environment's home
// and never reach into a Windows host (/mnt/c).
func DiscoverDefaultDirectorySources() []string {
	var out []string
	if home, err := os.UserHomeDir(); err == nil {
		if dirExists(filepath.Join(home, ".agents", "skills")) {
			out = append(out, "~/.agents/skills/")
		}
	}
	return out
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// Target is one linkable skill directory. Dir is the canonical string stored in
// EnabledEntry.Target and resolvable by reconcile.expandTarget (a leading "~"
// is kept for portability; an absolute path is used when an env override like
// CODEX_HOME forces one). There is no personal/project taxonomy — a target is
// just a directory the user chose. Harness (cc vs codex) is inferred from the
// path. Guarded directories are never returned as Targets — use Guarded() to
// test an arbitrary path on a write path.
type Target struct {
	Harness Harness `json:"harness"`
	Dir     string  `json:"dir"`
	Alias   string  `json:"alias,omitempty"` // optional user display name (cosmetic)
}

// Targets classifies each user-provided sync directory by the agent that
// consumes it — cc when the path is a Claude Code skills dir, codex when it is a
// .codex/.agents skills dir — and drops any blank or guarded entry. The order of
// dirs is preserved. cc and codex skills share one SKILL.md format, so the same
// source maps into either kind of target unchanged; the classification only
// drives the nested-SKILL.md guard (codex-only) and UI labeling.
func Targets(dirs []string) []Target {
	var out []Target
	for _, d := range dirs {
		if strings.TrimSpace(d) == "" || Guarded(d) {
			continue
		}
		out = append(out, Target{Harness: Classify(d), Dir: d})
	}
	return out
}

// Guarded reports whether dir is a Codex-owned directory the daemon must never
// link into, copy into, or adopt from: the bundled system-skill tree
// (<codexHome>/skills/.system) and the curated import clone
// (<codexHome>/vendor_imports/skills). Enforced on every write path (KTD7), not
// just hidden from target lists.
func Guarded(dir string) bool {
	r := expand(dir)
	system := filepath.Join(codexSkillsRoot(), ".system")
	vendor := filepath.Join(codexHome(), "vendor_imports", "skills")
	return underOrEqual(r, system) || underOrEqual(r, vendor)
}

// IsCodexTarget reports whether dir is a Codex skill directory. Codex reads
// GLOBAL skills from ~/.codex/skills ($CODEX_HOME/skills) and PROJECT skills from
// <repo>/.agents/skills. The user-level ~/.agents/skills is NOT Codex's — it is
// npx (skills.sh)'s install store — so it is excluded here even though it shares
// the /.agents/skills suffix. (Verified against real installs on disk: user
// ~/.agents/skills holds npx skills, project dev/.agents/skills holds Codex's.)
// reconcile uses this to scope the nested-SKILL.md guard (KTD6) to Codex targets.
func IsCodexTarget(dir string) bool {
	r := expand(dir)
	if home, err := os.UserHomeDir(); err == nil {
		if underOrEqual(r, filepath.Join(home, ".agents", "skills")) {
			return false // user-level ~/.agents/skills is npx's store, not Codex's
		}
	}
	if underOrEqual(r, codexSkillsRoot()) {
		return true
	}
	return strings.HasSuffix(r, filepath.FromSlash("/.codex/skills")) ||
		strings.HasSuffix(r, filepath.FromSlash("/.agents/skills"))
}

// SkillScope is whether a skills directory is user-level (personal/global, e.g.
// ~/.claude/skills, $CODEX_HOME/skills, ~/.agents/skills) or project-level
// (repo-local, e.g. /work/myrepo/.claude/skills). The Agent Skills convention
// is that project-level skills take precedence over user-level on a name clash
// (R5.1); the inventory view uses this to label which of two same-named skills
// actually wins (project) and which is shadowed (user). It is a DISPLAY signal —
// reconcile does not suppress links by scope this phase.
type SkillScope string

const (
	ScopeUser    SkillScope = "user"
	ScopeProject SkillScope = "project"
)

// Scope classifies a skills directory as user-level or project-level. A path
// under the home (or $CODEX_HOME) install roots is user-level; anything else is
// project-level (a repo checkout's .claude/.codex/.agents skills dir).
func Scope(dir string) SkillScope {
	r := expand(dir)
	if underOrEqual(r, codexSkillsRoot()) { // honors $CODEX_HOME override
		return ScopeUser
	}
	if home, err := os.UserHomeDir(); err == nil {
		for _, sub := range [][]string{{".claude", "skills"}, {".codex", "skills"}, {".agents", "skills"}} {
			if underOrEqual(r, filepath.Join(append([]string{home}, sub...)...)) {
				return ScopeUser
			}
		}
	}
	return ScopeProject
}

// SkillDirsFor expands a user-selected directory into the concrete Claude Code
// and Codex skills directories it implies. Only these two agents are supported,
// so their well-known subdir conventions are matched directly — the user OK'd
// hardcoding the *pattern* (".claude/skills", ".codex/skills"), which is distinct
// from hardcoding absolute install paths into config:
//
//	dir itself          — when the selection already is a cc/codex skills dir
//	dir/skills          — when dir is a .claude/.codex home (e.g. ~/.claude)
//	dir/.claude/skills  — cc, for a project or home root (e.g. a repo checkout)
//	dir/.agents/skills  — codex, where Codex reads PROJECT skills (preferred)
//	dir/.codex/skills   — codex, legacy fallback (Codex doesn't read this at the
//	                      project level; kept so existing setups still resolve)
//
// It returns the existing, non-guarded candidates — at most one per harness, so
// a project root yields exactly one cc + one codex target. .agents/skills is
// probed BEFORE .codex/skills so a project's Codex target lands on .agents/skills
// (where Codex actually reads project skills). Empty when none apply, so the
// caller can fall back to adding the selection verbatim.
func SkillDirsFor(dir string) []string {
	base := expand(dir)
	cands := []string{
		base,
		filepath.Join(base, "skills"),
		filepath.Join(base, ".claude", "skills"),
		filepath.Join(base, ".agents", "skills"),
		filepath.Join(base, ".codex", "skills"),
	}
	var out []string
	seenHarness := map[Harness]bool{}
	for _, c := range cands {
		h := Classify(c)
		if h == HarnessUnknown || seenHarness[h] || Guarded(c) || !dirExists(c) {
			continue
		}
		seenHarness[h] = true
		out = append(out, c)
	}
	return out
}

// ScaffoldSkillDirs creates the missing skills/ subdirectory under any agent home
// (.claude/.codex/.agents) that already exists at dir (when dir IS such a home) or
// directly beneath it (when dir is a project root). This lets a freshly-created
// project that has .claude but no .claude/skills be added as a sync target — we
// fill in the conventional skills/ child rather than rejecting it or adding the
// project root as an unknown dir. It NEVER creates an agent home that is absent
// (so a project without .codex does not gain one) and never touches guarded
// locations. Returns the dirs it created, in canonical absolute form.
func ScaffoldSkillDirs(dir string) []string {
	base := expand(dir)
	var roots []string
	switch filepath.Base(base) {
	case ".claude", ".codex", ".agents":
		roots = append(roots, base) // dir itself is an agent home
	}
	for _, name := range []string{".claude", ".codex", ".agents"} {
		roots = append(roots, filepath.Join(base, name)) // homes nested under a project root
	}
	var created []string
	for _, root := range roots {
		if !dirExists(root) {
			continue
		}
		sk := filepath.Join(root, "skills")
		if dirExists(sk) || Guarded(sk) {
			continue
		}
		if err := os.MkdirAll(sk, 0o755); err == nil {
			created = append(created, sk)
		}
	}
	return created
}

// CreateSkillDirs scaffolds the requested harness skill dir(s) UNDER base and
// returns them (canonical absolute). Unlike ScaffoldSkillDirs it deliberately
// CREATES an absent agent home — the user explicitly opted in when a plain
// folder was neither a CC nor a Codex dir. kinds are "cc" (→ base/.claude/skills)
// and "codex" (→ base/.agents/skills, where Codex reads PROJECT skills). Unknown
// kinds are ignored; base must not be guarded.
func CreateSkillDirs(dir string, kinds []string) ([]string, error) {
	base := expand(dir)
	if Guarded(base) {
		return nil, fmt.Errorf("directory is guarded")
	}
	sub := map[string]string{
		"cc":    filepath.Join(".claude", "skills"),
		"codex": filepath.Join(".agents", "skills"), // project-level Codex skills dir
	}
	seen := map[string]bool{}
	var out []string
	for _, k := range kinds {
		rel, ok := sub[k]
		if !ok || seen[k] {
			continue
		}
		seen[k] = true
		p := filepath.Join(base, rel)
		if err := os.MkdirAll(p, 0o755); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no valid harness to create")
	}
	return out, nil
}

// AncestorSkillDirs returns the EXISTING skill directories a runtime would ALSO
// read for target's harness, BESIDES target itself — target's "inherited" levels.
// The UI uses it to flag skills already provided from above so the user avoids
// re-enabling them here. For a Codex target it walks parent dirs' .agents/skills
// up to the filesystem root (the user-level ~/.agents/skills is npx's, excluded
// by IsCodexTarget) and adds the user-global ~/.codex/skills; for a CC target it
// adds the user-global ~/.claude/skills. A user-level target has no ancestors.
// Nearest-first order. Pure path logic + existence check; it does not scan skills.
func AncestorSkillDirs(target string) []string {
	t := expand(target)
	var out []string
	seen := map[string]bool{t: true}
	add := func(p string) {
		p = expand(p)
		if seen[p] || !dirExists(p) {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	switch {
	case IsCodexTarget(t):
		// Project-level <proj>/.agents/skills: Codex reads .agents/skills from every
		// parent dir up to the fs root, so walk them. (IsCodexTarget skips the npx
		// ~/.agents/skills, so it is never counted as inherited Codex.)
		if strings.HasSuffix(t, filepath.FromSlash("/.agents/skills")) {
			proj := filepath.Dir(filepath.Dir(t)) // <proj> from <proj>/.agents/skills
			for {
				parent := filepath.Dir(proj)
				if parent == proj { // reached fs root
					break
				}
				if c := filepath.Join(parent, ".agents", "skills"); IsCodexTarget(c) {
					add(c)
				}
				proj = parent
			}
		}
		add(codexSkillsRoot()) // user-global Codex
	case Classify(t) == HarnessClaudeCode:
		if home, err := os.UserHomeDir(); err == nil {
			add(filepath.Join(home, ".claude", "skills")) // user-global CC
		}
	}
	return out
}

// PluginRootFor returns the agent plugin tree associated with a skills dir: the
// sibling "plugins" dir of the skills dir's parent (e.g. ~/.claude/skills →
// ~/.claude/plugins, /work/.codex/skills → /work/.codex/plugins). Plugin skills
// live nested under here, managed by the agent's plugin system rather than
// hand-authored, so the adopt scan only walks this tree when the user opts in.
func PluginRootFor(skillsDir string) string {
	d := expand(skillsDir)
	return filepath.Join(filepath.Dir(d), "plugins")
}

// Expand resolves a leading "~" and returns a cleaned absolute path. It is the
// single canonical resolver for target Dirs (which keep the portable "~" form):
// callers outside this package — adopt's scan/relocate paths, the server's
// adopt-root validation — must resolve a Target.Dir through this, never through
// filepath.Abs (which does not expand "~").
func Expand(p string) string { return expand(p) }

// --- internal helpers ---

func codexHome() string {
	if h := os.Getenv("CODEX_HOME"); h != "" {
		return expand(h)
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".codex")
	}
	return ".codex"
}

func codexSkillsRoot() string { return filepath.Join(codexHome(), "skills") }

// expand resolves a leading "~" and returns a cleaned absolute path. Mirrors
// reconcile.expandTarget intentionally — harness must not import reconcile.
func expand(p string) string {
	p = strings.TrimSpace(p)
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
		}
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	return filepath.Clean(p)
}

// underOrEqual reports whether path is root or a descendant of root (both
// already cleaned/absolute), without being fooled by sibling prefixes like
// "/a/skills-x" vs "/a/skills".
func underOrEqual(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+string(os.PathSeparator))
}
