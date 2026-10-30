package server

// SkillHub source: skills a SkillHub registry's CLI (`npx @astron-team/skillhub`)
// installs into OUR managed store (config.SkillHubStorePath) via its `--dir` flag.
// We link FROM the store into targets under the @skillhub namespace
// (reconcile.SkillHubNamespace); the CLI owns install/update/remove — same
// never-modify posture as @agents/@dir. Download-only: SkillManager pulls from the
// CLI's built-in PUBLIC registry anonymously — no registry/token/auth config, no
// private-registry support (this system only downloads).

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"skillmanage/internal/config"
	"skillmanage/internal/reconcile"
	"skillmanage/internal/scanner"
)

// skillHubInfo describes the SkillHub source for /api/status (download-only).
type skillHubInfo struct {
	Available bool   `json:"available"` // npx present → CLI usable
	Root      string `json:"root"`      // the managed store path
	Count     int    `json:"count"`     // skills installed in the store
}

// skillHubStore is the managed store the SkillHub CLI installs into (--dir).
func (s *Server) skillHubStore() string { return config.SkillHubStorePath(s.centralDir) }

// listSkillHubLocked scans the managed store. Every dir there is a SkillHub
// skill — the store is ours exclusively, so no lockfile cross-check is needed
// (unlike ~/.agents/skills, which mixes tools). Caller holds s.mu.
func (s *Server) listSkillHubLocked() []scanner.Skill {
	all, err := scanner.Scan(s.skillHubStore())
	if err != nil {
		return nil
	}
	return all
}

// handleListSkillHub lists skills installed in the managed store for the modal.
func (s *Server) handleListSkillHub(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	skills := s.listSkillHubLocked()
	s.mu.Unlock()
	if skills == nil {
		skills = []scanner.Skill{}
	}
	writeJSON(w, http.StatusOK, skills)
}

// skillHubResult is one normalized search hit shaped for the online card.
type skillHubResult struct {
	Slug        string `json:"slug"`
	Name        string `json:"name,omitempty"`
	Namespace   string `json:"namespace,omitempty"`
	Version     string `json:"version,omitempty"`
	Description string `json:"description,omitempty"`
}

// parseSkillHubSearch parses `skillhub search --json` into normalized results.
// The registry's exact JSON shape is not pinned, so this is defensive: it takes
// either a bare array or an {ok,results|skills|data:[...]} envelope and maps
// common field names. Pure (no I/O) → unit-testable.
func parseSkillHubSearch(stdout string) []skillHubResult {
	out := []skillHubResult{}
	raw := strings.TrimSpace(stdout)
	if i := strings.IndexAny(raw, "[{"); i >= 0 {
		raw = raw[i:] // skip any leading non-JSON noise
	}
	if raw == "" {
		return out
	}
	var env struct {
		Items   []map[string]any `json:"items"` // the actual @astron-team/skillhub shape
		Results []map[string]any `json:"results"`
		Skills  []map[string]any `json:"skills"`
		Data    []map[string]any `json:"data"`
	}
	var items []map[string]any
	if json.Unmarshal([]byte(raw), &env) == nil {
		switch {
		case len(env.Items) > 0:
			items = env.Items
		case len(env.Results) > 0:
			items = env.Results
		case len(env.Skills) > 0:
			items = env.Skills
		case len(env.Data) > 0:
			items = env.Data
		}
	}
	if items == nil {
		_ = json.Unmarshal([]byte(raw), &items) // bare array
	}
	str := func(m map[string]any, keys ...string) string {
		for _, k := range keys {
			if sv, ok := m[k].(string); ok && sv != "" {
				return sv
			}
		}
		return ""
	}
	for _, m := range items {
		slug := str(m, "slug", "name", "id")
		if slug == "" {
			continue
		}
		out = append(out, skillHubResult{
			Slug:        slug,
			Name:        str(m, "displayName", "title", "name"),
			Namespace:   str(m, "namespace", "scope"),
			Version:     str(m, "latestVersion", "version", "latest"),
			Description: str(m, "summary", "description"),
		})
	}
	return out
}

// handleSkillHubFind searches the logged-in SkillHub registry via the CLI.
func (s *Server) handleSkillHubFind(w http.ResponseWriter, r *http.Request) {
	if !originGuard(w, r) {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, http.StatusOK, map[string]any{"available": true, "results": []skillHubResult{}})
		return
	}
	if strings.HasPrefix(q, "-") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "搜索词不能以 - 开头"})
		return
	}
	s.mu.Lock()
	npx, runner := s.npxPath, s.runner
	s.mu.Unlock()
	if npx == "" || runner == nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false})
		return
	}
	ctx, cancel := context.WithTimeout(s.detachedCtx(), 120*time.Second)
	defer cancel()
	stdout, stderr, err := runner.SkillHubSearch(ctx, npx, q)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"available": true, "results": []skillHubResult{},
			"error": skillHubFailReason(ansiRe.ReplaceAllString(stdout+"\n"+stderr, ""), err),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"available": true, "results": parseSkillHubSearch(stdout)})
}

type skillHubSlugReq struct {
	Slug string `json:"slug"`
}

// handleSkillHubInstall installs a slug INTO the managed store (not an agent
// dir). Install ≠ enable: the skill then appears under @skillhub for the user to
// enable into a target via the normal picker — two-step, like skills.sh add.
func (s *Server) handleSkillHubInstall(w http.ResponseWriter, r *http.Request) {
	if !originGuard(w, r) {
		return
	}
	var req skillHubSlugReq
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	slug := strings.TrimSpace(req.Slug)
	if slug == "." || slug == ".." || !skillNameRe.MatchString(slug) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "无效的 slug"})
		return
	}
	s.mu.Lock()
	npx, runner := s.npxPath, s.runner
	s.mu.Unlock()
	if npx == "" || runner == nil {
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{"error": npxUnavailableErr("npx @astron-team/skillhub install", s.nodeDependency)})
		return
	}
	ctx, cancel := context.WithTimeout(s.detachedCtx(), 300*time.Second)
	defer cancel()
	stdout, stderr, err := runner.SkillHubInstall(ctx, npx, slug, s.skillHubStore())
	clean := ansiRe.ReplaceAllString(stdout+"\n"+stderr, "")
	ok := err == nil && (jsonOK(stdout) || strings.Contains(clean, "Installed") || strings.Contains(clean, "installed"))
	resp := map[string]any{"ok": ok, "stdout": stdout, "stderr": stderr}
	if ok {
		// If this slug is already enabled (@skillhub/<slug>), refresh its link to
		// point at the newly installed content.
		s.ReconcileOnly()
	} else {
		resp["error"] = skillHubFailReason(clean, err)
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleSkillHubRemove uninstalls a slug: (1) drop our @skillhub/<slug> links
// across all targets + reconcile them off disk, THEN (2) delegate the CLI to
// delete the store copy — no dangling links, and we never rm the store ourselves.
func (s *Server) handleSkillHubRemove(w http.ResponseWriter, r *http.Request) {
	if !originGuard(w, r) {
		return
	}
	var req skillHubSlugReq
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	slug := strings.TrimSpace(req.Slug)
	if slug == "." || slug == ".." || !skillNameRe.MatchString(slug) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "无效的 slug"})
		return
	}
	s.mu.Lock()
	npx, runner := s.npxPath, s.runner
	s.mu.Unlock()
	if npx == "" || runner == nil {
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{"error": npxUnavailableErr("npx @astron-team/skillhub remove", s.nodeDependency)})
		return
	}
	sel := reconcile.SkillHubNamespace + "/" + slug
	s.mu.Lock()
	kept := s.cfg.Enabled[:0]
	removedLinks := 0
	for _, e := range s.cfg.Enabled {
		if e.Skill == sel {
			removedLinks++
			continue
		}
		kept = append(kept, e)
	}
	s.cfg.Enabled = kept
	var saveErr error
	if removedLinks > 0 {
		saveErr = config.SaveConfig(s.centralDir, s.cfg)
	}
	s.mu.Unlock()
	if saveErr != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": saveErr.Error()})
		return
	}
	if removedLinks > 0 {
		s.ReconcileOnly()
	}
	ctx, cancel := context.WithTimeout(s.detachedCtx(), 120*time.Second)
	defer cancel()
	stdout, stderr, err := runner.SkillHubRemove(ctx, npx, slug, s.skillHubStore())
	clean := ansiRe.ReplaceAllString(stdout+"\n"+stderr, "")
	ok := err == nil && (jsonOK(stdout) || strings.Contains(clean, "Removed") || strings.Contains(clean, "removed"))
	resp := map[string]any{"ok": ok, "stdout": stdout, "stderr": stderr, "removedLinks": removedLinks}
	if !ok {
		resp["error"] = skillHubFailReason(clean, err)
	}
	writeJSON(w, http.StatusOK, resp)
}

// jsonOK reports whether a --json CLI response object has {"ok":true}.
func jsonOK(stdout string) bool {
	raw := strings.TrimSpace(stdout)
	if i := strings.IndexByte(raw, '{'); i >= 0 {
		raw = raw[i:]
	}
	var v struct {
		OK bool `json:"ok"`
	}
	return json.Unmarshal([]byte(raw), &v) == nil && v.OK
}

// skillHubFailReason extracts a human failure line: prefer the --json error
// {"ok":false,"message":"…"}, then the process error, then the first failure line.
func skillHubFailReason(clean string, err error) string {
	if raw := strings.TrimSpace(clean); raw != "" {
		if i := strings.IndexByte(raw, '{'); i >= 0 {
			var v struct {
				Message string `json:"message"`
			}
			if json.Unmarshal([]byte(raw[i:]), &v) == nil && v.Message != "" {
				return v.Message
			}
		}
	}
	if err != nil {
		return err.Error()
	}
	if ln := firstFailureLine(clean); ln != "" {
		return ln
	}
	return "操作失败（CLI 未报告原因）"
}
