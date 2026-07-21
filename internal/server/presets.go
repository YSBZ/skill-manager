package server

import (
	"net/http"
	"strings"

	"skillmanage/internal/config"
	"skillmanage/internal/harness"
)

// Presets are named, reusable GROUPS of skill selectors — a pure abstraction over
// the library (references, never copies). Members are individual skills (<ns>/<name>,
// snapshot) or a whole-repo follow (<ns>/*, "整仓"); both are first-class. Two ways
// to build one: snapshot a target's enabled set (POST /api/presets), or set the
// skill list explicitly — new empty + add-from-library + remove a skill all via
// PUT /api/presets. Apply = batch-enable a preset's selectors into a target
// (additive: other enabled skills untouched); Remove = batch-disable them. Like
// /api/enabled, these only mutate config.Enabled + persist; the frontend triggers
// reconcile via /api/apply afterward (avoiding a re-lock in the handler).

func (s *Server) findPresetLocked(name string) *config.Preset {
	for i := range s.cfg.Presets {
		if s.cfg.Presets[i].Name == name {
			return &s.cfg.Presets[i]
		}
	}
	return nil
}

// handleListPresets returns all saved presets.
func (s *Server) handleListPresets(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	presets := append([]config.Preset(nil), s.cfg.Presets...)
	s.mu.Unlock()
	if presets == nil {
		presets = []config.Preset{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"presets": presets})
}

// handleCreatePreset snapshots a target's currently-enabled selections into a new
// named preset (upsert by name). Stores only selectors + mode — never content.
func (s *Server) handleCreatePreset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string `json:"name"`
		Target string `json:"target"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || strings.TrimSpace(req.Target) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "需要预设名与来源目录"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	want := harness.Expand(req.Target)
	seen := make(map[string]bool)
	var skills []config.PresetSkill
	for _, e := range s.cfg.Enabled {
		if harness.Expand(e.Target) != want || seen[e.Skill] {
			continue
		}
		// Copy selectors verbatim — including a whole-repo follow (<ns>/*), which is
		// a first-class preset member ("整仓") the picker represents with a per-source
		// toggle; never expanded into individual skills.
		seen[e.Skill] = true
		skills = append(skills, config.PresetSkill{Skill: e.Skill, Mode: e.Mode})
	}
	if len(skills) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "当前目录没有已启用的 skill，无法存为预设"})
		return
	}
	if p := s.findPresetLocked(name); p != nil {
		p.Skills = skills // overwrite same-named preset
	} else {
		s.cfg.Presets = append(s.cfg.Presets, config.Preset{Name: name, Skills: skills})
	}
	if err := s.persistConfigLocked(w); err != nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "name": name, "count": len(skills)})
}

// handleSetPreset upserts a preset's skill list explicitly — the editor path:
// new empty preset, add-from-library, or remove a single skill all funnel through
// here by sending the full desired list. Replaces the named preset's Skills with
// the (deduped) list; creates the preset if absent. An empty list is allowed.
//
// Live propagation: edits sync to every target where the preset was ALREADY fully
// applied (all of its OLD selectors enabled there) — added selectors get enabled,
// removed ones get disabled (kept if still claimed by another preset, same overlap
// rule as delete). Targets that never had the group are untouched. The frontend
// reconciles via /api/apply afterward. Stores only selectors + mode — never content.
func (s *Server) handleSetPreset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string `json:"name"`
		Skills []struct {
			Skill string      `json:"skill"`
			Mode  config.Mode `json:"mode"`
		} `json:"skills"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "需要预设名"})
		return
	}
	seen := make(map[string]bool, len(req.Skills))
	var skills []config.PresetSkill
	for _, ps := range req.Skills {
		sel := strings.TrimSpace(ps.Skill)
		if sel == "" || seen[sel] {
			continue
		}
		seen[sel] = true
		mode := ps.Mode
		if mode == "" {
			mode = config.ModeSnapshot
		}
		skills = append(skills, config.PresetSkill{Skill: sel, Mode: mode})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Snapshot the OLD members (for diff-propagation) before overwriting.
	var oldSkills []config.PresetSkill
	if p := s.findPresetLocked(name); p != nil {
		oldSkills = append(oldSkills, p.Skills...)
		p.Skills = skills
	} else {
		s.cfg.Presets = append(s.cfg.Presets, config.Preset{Name: name, Skills: skills})
	}
	s.propagatePresetEditLocked(name, oldSkills, skills)
	if err := s.persistConfigLocked(w); err != nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "name": name, "count": len(skills)})
}

// propagatePresetEditLocked syncs a preset edit to every target where the preset
// was ALREADY fully applied (all OLD selectors enabled there): added selectors get
// enabled, removed ones disabled — unless a removed selector is still claimed by
// another preset (overlap kept, same rule as delete). No-op if the preset had no
// members (not applied anywhere) or nothing changed. Caller holds s.mu; the
// frontend reconciles via /api/apply.
func (s *Server) propagatePresetEditLocked(name string, oldSkills, newSkills []config.PresetSkill) {
	if len(oldSkills) == 0 {
		return // an empty/new preset isn't applied anywhere yet
	}
	oldSet := make(map[string]bool, len(oldSkills))
	for _, ps := range oldSkills {
		oldSet[ps.Skill] = true
	}
	newSet := make(map[string]bool, len(newSkills))
	for _, ps := range newSkills {
		newSet[ps.Skill] = true
	}
	var added []config.PresetSkill
	for _, ps := range newSkills {
		if !oldSet[ps.Skill] {
			added = append(added, ps)
		}
	}
	removed := make(map[string]bool)
	for _, ps := range oldSkills {
		if !newSet[ps.Skill] {
			removed[ps.Skill] = true
		}
	}
	if len(added) == 0 && len(removed) == 0 {
		return
	}
	// A removed selector still claimed by ANOTHER preset keeps its links (overlap).
	for i := range s.cfg.Presets {
		if s.cfg.Presets[i].Name == name {
			continue
		}
		for _, ps := range s.cfg.Presets[i].Skills {
			delete(removed, ps.Skill)
		}
	}
	// Targets where the group was fully applied = every OLD selector enabled there.
	hits := make(map[string]int)    // expanded target -> # of old selectors enabled
	raw := make(map[string]string)  // expanded target -> a representative raw target
	for _, e := range s.cfg.Enabled {
		if !oldSet[e.Skill] {
			continue
		}
		key := harness.Expand(e.Target)
		hits[key]++
		if raw[key] == "" {
			raw[key] = e.Target
		}
	}
	applied := make(map[string]string) // expanded -> raw, only fully-applied targets
	for key, n := range hits {
		if n >= len(oldSkills) {
			applied[key] = raw[key]
		}
	}
	if len(applied) == 0 {
		return
	}
	// Disable removed selectors in applied targets.
	if len(removed) > 0 {
		out := s.cfg.Enabled[:0]
		for _, e := range s.cfg.Enabled {
			if removed[e.Skill] {
				if _, ok := applied[harness.Expand(e.Target)]; ok {
					continue // drop the link in this applied target
				}
			}
			out = append(out, e)
		}
		s.cfg.Enabled = out
	}
	// Enable added selectors in applied targets (dedup).
	for _, ps := range added {
		for key, rawTarget := range applied {
			dup := false
			for _, e := range s.cfg.Enabled {
				if e.Skill == ps.Skill && harness.Expand(e.Target) == key {
					dup = true
					break
				}
			}
			if !dup {
				s.cfg.Enabled = append(s.cfg.Enabled, config.EnabledEntry{Skill: ps.Skill, Target: rawTarget, Mode: ps.Mode})
			}
		}
	}
}

// handleDeletePreset removes a preset by name AND tears down the links its skills
// built across ALL targets (the frontend reconciles via /api/apply afterward).
// Overlap handling: a selector still claimed by ANOTHER preset is preserved —
// only selectors UNIQUE to the deleted preset have their enabled entries removed.
// (Presets are pure references with no per-link provenance, so "still wanted by
// another preset" is the tractable overlap signal; a skill independently enabled
// elsewhere can't be distinguished and is treated as this preset's.)
func (s *Server) handleDeletePreset(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	del := s.findPresetLocked(name)
	if del == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": 0, "shared": 0}) // already gone
		return
	}
	total := len(del.Skills) // capture before the in-place presets filter aliases del
	// Selectors still claimed by OTHER presets must keep their links (overlap).
	shared := make(map[string]bool)
	for i := range s.cfg.Presets {
		if s.cfg.Presets[i].Name == name {
			continue
		}
		for _, ps := range s.cfg.Presets[i].Skills {
			shared[ps.Skill] = true
		}
	}
	toDrop := make(map[string]bool)
	for _, ps := range del.Skills {
		if !shared[ps.Skill] {
			toDrop[ps.Skill] = true
		}
	}
	// Remove this preset's unique selectors from Enabled across every target.
	removed := 0
	if len(toDrop) > 0 {
		out := s.cfg.Enabled[:0]
		for _, e := range s.cfg.Enabled {
			if toDrop[e.Skill] {
				removed++
				continue
			}
			out = append(out, e)
		}
		s.cfg.Enabled = out
	}
	// Drop the preset itself.
	keep := s.cfg.Presets[:0]
	for _, p := range s.cfg.Presets {
		if p.Name != name {
			keep = append(keep, p)
		}
	}
	s.cfg.Presets = keep
	if err := s.persistConfigLocked(w); err != nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": removed, "shared": total - len(toDrop)})
}

// handleApplyPreset batch-enables a preset's selectors into target (additive).
func (s *Server) handleApplyPreset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string `json:"name"`
		Target string `json:"target"`
	}
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.Target) == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if harness.Guarded(req.Target) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "target is a guarded directory"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	preset := s.findPresetLocked(req.Name)
	if preset == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "预设不存在"})
		return
	}
	want := harness.Expand(req.Target)
	added := 0
	for _, ps := range preset.Skills {
		dup := false
		for _, x := range s.cfg.Enabled {
			if x.Skill == ps.Skill && harness.Expand(x.Target) == want {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		s.cfg.Enabled = append(s.cfg.Enabled, config.EnabledEntry{Skill: ps.Skill, Target: req.Target, Mode: ps.Mode})
		added++
	}
	if err := s.persistConfigLocked(w); err != nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "added": added, "total": len(preset.Skills)})
}

// handleRemovePreset batch-disables a preset's selectors from target.
func (s *Server) handleRemovePreset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string `json:"name"`
		Target string `json:"target"`
	}
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.Target) == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	preset := s.findPresetLocked(req.Name)
	if preset == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "预设不存在"})
		return
	}
	want := harness.Expand(req.Target)
	sel := make(map[string]bool, len(preset.Skills))
	for _, ps := range preset.Skills {
		sel[ps.Skill] = true
	}
	out := s.cfg.Enabled[:0]
	removed := 0
	for _, x := range s.cfg.Enabled {
		if sel[x.Skill] && harness.Expand(x.Target) == want {
			removed++
			continue
		}
		out = append(out, x)
	}
	s.cfg.Enabled = out
	if err := s.persistConfigLocked(w); err != nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": removed})
}
