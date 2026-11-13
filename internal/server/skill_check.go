package server

import (
	"net/http"
	"path/filepath"
	"strings"

	"skillmanage/internal/harness"
	"skillmanage/internal/reconcile"
	"skillmanage/internal/scanner"
	"skillmanage/internal/skillcheck"
)

// checkContext contains only skills discoverable in this target's scope. Source
// catalogs and unrelated targets must never satisfy a runtime dependency.
type checkContext struct {
	client    string
	available map[string]string
}

func (s *Server) checkContext(target string) checkContext {
	client := string(harness.Classify(target))
	ctx := checkContext{client: client, available: map[string]string{}}
	add := func(sk scanner.Skill, namespace string) {
		for _, name := range []string{sk.LinkName, sk.FrontmatterName} {
			if name == "" {
				continue
			}
			if namespace != "" {
				name = namespace + ":" + name
			}
			if _, exists := ctx.available[name]; !exists {
				ctx.available[name] = sk.Dir // nearest directory wins
			}
		}
	}
	for _, dir := range append([]string{harness.Expand(target)}, harness.AncestorSkillDirs(target)...) {
		skills, _ := scanner.ScanInventory(dir)
		for _, sk := range skills {
			add(sk, "")
		}
		// Codex's built-in skills are discoverable but read-only. Include only
		// the actual guarded global system root, not arbitrary nested folders.
		system := filepath.Join(dir, ".system")
		if client == "codex" && harness.Guarded(system) {
			builtins, _ := scanner.Scan(system)
			for _, sk := range builtins {
				add(sk, "")
			}
		}
	}
	for _, p := range listPluginSkillsForTarget(target) {
		if p.Harness != client || p.Dir == "" {
			continue
		}
		// Keep plugin namespaces. A foo:plan dependency is not satisfied by
		// bar:plan or an unrelated bare plan directory.
		ctx.available[p.Plugin+":"+p.Name] = p.Dir
	}
	return ctx
}

// pluginTargetPriority returns -1 for an install not discoverable in this
// target. Project/local installs inherit into descendants; the closest project
// wins over ancestors and user installs. This describes local discovery only,
// not whether the harness will enable or execute the plugin at runtime.
func pluginTargetPriority(p pluginSkill, target string) int {
	if p.Harness != string(harness.Classify(target)) || p.Dir == "" {
		return -1
	}
	switch p.Scope {
	case "user", "system", "managed":
		return 1 << 20
	case "":
		// Older manifests did not record a scope. A project path, when supplied,
		// still constrains discovery rather than being silently made global.
		if p.ProjectPath == "" {
			return 1 << 20
		}
	case "project", "local":
	default:
		return -1
	}
	if harness.Scope(target) == harness.ScopeUser || !filepath.IsAbs(p.ProjectPath) {
		return -1
	}
	canonical := func(path string) string {
		path = filepath.Clean(path)
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			return resolved
		}
		return path
	}
	project := filepath.Dir(harness.Expand(target))
	switch filepath.Base(project) {
	case ".claude", ".codex", ".agents", ".pi":
		project = filepath.Dir(project)
	}
	project, installed := canonical(project), canonical(p.ProjectPath)
	for distance := 0; ; distance++ {
		if project == installed {
			priority := distance * 2
			if p.Scope != "local" {
				priority++
			}
			return priority
		}
		parent := filepath.Dir(project)
		if parent == project {
			return -1
		}
		project = parent
	}
}

func (s *Server) configuredCheckTarget(raw string) (string, bool) {
	if strings.TrimSpace(raw) == "" {
		return "", false
	}
	want := harness.Expand(raw)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, target := range s.cfg.Targets {
		if harness.Expand(target) == want {
			return target, true
		}
	}
	return "", false
}

func validCheckName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00")
}

// checkSourceRoot deliberately narrows sourceRoot's lexical repo resolver to
// registered sources; the new check API never accepts arbitrary source paths.
func (s *Server) checkSourceRoot(repo string) (string, bool) {
	if repo == reconcile.SkillHubNamespace {
		return s.skillHubStore(), true
	}
	if repo == reconcile.LocalNamespace || repo == reconcile.AgentsNamespace || strings.HasPrefix(repo, reconcile.DirNamespacePrefix) {
		return s.sourceRoot(repo)
	}
	s.mu.Lock()
	known := false
	for _, r := range s.cfg.Repos {
		if reconcile.RepoName(r.URL) == repo {
			known = true
			break
		}
	}
	s.mu.Unlock()
	if !known {
		return "", false
	}
	return s.sourceRoot(repo)
}

// handleSkillCheck is a read-only sibling of the detail endpoints. The request
// identifies a registered target/source/plugin plus one scanned child, never a
// user-supplied filesystem path. Even broken links can produce a useful report.
func (s *Server) handleSkillCheck(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	name := q.Get("name")
	target, ok := s.configuredCheckTarget(q.Get("target"))
	if !ok || !validCheckName(name) {
		http.Error(w, "invalid target or skill name", http.StatusBadRequest)
		return
	}
	ctx := s.checkContext(target)
	var entries []scanner.Skill
	if plugin := q.Get("plugin"); plugin != "" {
		if requested := q.Get("harness"); requested != "" && requested != ctx.client {
			http.Error(w, "plugin is not in target client", http.StatusBadRequest)
			return
		}
		for _, p := range listPluginSkillsForTarget(target) {
			if p.Plugin == plugin && p.Name == name && p.Harness == ctx.client && p.Dir != "" {
				writeJSON(w, http.StatusOK, skillcheck.Inspect(p.Dir, ctx.client, ctx.available))
				return
			}
		}
	} else if repo := q.Get("repo"); repo != "" {
		root, known := s.checkSourceRoot(repo)
		if !known {
			http.Error(w, "unknown source", http.StatusBadRequest)
			return
		}
		entries, _ = scanner.Scan(root)
	} else {
		entries, _ = scanner.ScanInventoryWithIssues(harness.Expand(target))
	}
	for _, sk := range entries {
		if sk.LinkName == name || sk.LogicalName == name {
			writeJSON(w, http.StatusOK, skillcheck.Inspect(sk.Dir, ctx.client, ctx.available))
			return
		}
	}
	http.Error(w, "skill not found", http.StatusNotFound)
}

// readableSkillContent shares the checker's bounded, regular-file-only reader
// with details so a malformed skill cannot block the UI on a FIFO or huge file.
func readableSkillContent(dir string) string {
	content, err := skillcheck.ReadContent(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return ""
	}
	return content
}
