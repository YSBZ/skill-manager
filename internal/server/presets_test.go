package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"skillmanage/internal/config"
)

func countEnabled(s *Server, target string) int {
	n := 0
	for _, e := range s.cfg.Enabled {
		if e.Target == target {
			n++
		}
	}
	return n
}

func TestPresetsLifecycle(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()
	const t1, t2 = "/tmp/sm-test-t1", "/tmp/sm-test-t2"
	s.cfg.Targets = []string{t1, t2}
	s.cfg.Enabled = []config.EnabledEntry{
		{Skill: "repoA/sk1", Target: t1, Mode: config.ModeSnapshot},
		{Skill: "repoA/sk2", Target: t1, Mode: config.ModeSnapshot},
	}

	do := func(method, target string, body any) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req(method, target, s.token, body))
		return w
	}

	// create preset "grp" from t1's enabled set
	if w := do("POST", "/api/presets", map[string]string{"name": "grp", "target": t1}); w.Code != http.StatusOK {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	if len(s.cfg.Presets) != 1 || s.cfg.Presets[0].Name != "grp" || len(s.cfg.Presets[0].Skills) != 2 {
		t.Fatalf("preset not saved: %+v", s.cfg.Presets)
	}

	// list
	w := do("GET", "/api/presets", nil)
	var lr struct {
		Presets []config.Preset `json:"presets"`
	}
	json.Unmarshal(w.Body.Bytes(), &lr)
	if len(lr.Presets) != 1 {
		t.Fatalf("list: %+v", lr)
	}

	// apply to t2 (additive)
	if w := do("POST", "/api/presets/apply", map[string]string{"name": "grp", "target": t2}); w.Code != http.StatusOK {
		t.Fatalf("apply: %d %s", w.Code, w.Body.String())
	}
	if countEnabled(s, t2) != 2 {
		t.Fatalf("apply did not enable on t2: %+v", s.cfg.Enabled)
	}
	if countEnabled(s, t1) != 2 {
		t.Fatalf("apply disturbed t1 (must be additive): %+v", s.cfg.Enabled)
	}

	// apply again → dedup, no double
	do("POST", "/api/presets/apply", map[string]string{"name": "grp", "target": t2})
	if countEnabled(s, t2) != 2 {
		t.Fatalf("re-apply duplicated: %+v", s.cfg.Enabled)
	}

	// remove from t2 → t2 cleared, t1 intact
	if w := do("POST", "/api/presets/remove", map[string]string{"name": "grp", "target": t2}); w.Code != http.StatusOK {
		t.Fatalf("remove: %d %s", w.Code, w.Body.String())
	}
	if countEnabled(s, t2) != 0 {
		t.Fatalf("remove left entries on t2: %+v", s.cfg.Enabled)
	}
	if countEnabled(s, t1) != 2 {
		t.Fatalf("remove disturbed t1: %+v", s.cfg.Enabled)
	}

	// delete preset
	if w := do("DELETE", "/api/presets?name=grp", nil); w.Code != http.StatusOK {
		t.Fatalf("delete: %d", w.Code)
	}
	if len(s.cfg.Presets) != 0 {
		t.Fatalf("preset not deleted: %+v", s.cfg.Presets)
	}
}

// TestPresetSetEditable covers the explicit-edit path (PUT): new empty preset →
// set a skills list (add-from-library) → trim it (remove a skill). Dedup too.
func TestPresetSetEditable(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()
	do := func(method, target string, body any) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req(method, target, s.token, body))
		return w
	}

	// new empty preset
	if w := do("PUT", "/api/presets", map[string]any{"name": "lib", "skills": []any{}}); w.Code != http.StatusOK {
		t.Fatalf("put empty: %d %s", w.Code, w.Body.String())
	}
	if len(s.cfg.Presets) != 1 || len(s.cfg.Presets[0].Skills) != 0 {
		t.Fatalf("empty preset not created: %+v", s.cfg.Presets)
	}

	// add two skills from the library (with a duplicate that must collapse)
	body := map[string]any{"name": "lib", "skills": []map[string]string{
		{"skill": "repoA/x"}, {"skill": "@local/y"}, {"skill": "repoA/x"},
	}}
	if w := do("PUT", "/api/presets", body); w.Code != http.StatusOK {
		t.Fatalf("put skills: %d %s", w.Code, w.Body.String())
	}
	if len(s.cfg.Presets[0].Skills) != 2 {
		t.Fatalf("dedup failed: %+v", s.cfg.Presets[0].Skills)
	}
	if s.cfg.Presets[0].Skills[0].Mode != config.ModeSnapshot {
		t.Fatalf("default mode should be snapshot: %+v", s.cfg.Presets[0].Skills[0])
	}

	// trim to one (remove a skill via full-list replace)
	if w := do("PUT", "/api/presets", map[string]any{"name": "lib", "skills": []map[string]string{{"skill": "@local/y"}}}); w.Code != http.StatusOK {
		t.Fatalf("put trim: %d", w.Code)
	}
	if len(s.cfg.Presets[0].Skills) != 1 || s.cfg.Presets[0].Skills[0].Skill != "@local/y" {
		t.Fatalf("trim failed: %+v", s.cfg.Presets[0].Skills)
	}

	// missing name → 400
	if w := do("PUT", "/api/presets", map[string]any{"skills": []any{}}); w.Code != http.StatusBadRequest {
		t.Fatalf("missing name should be 400: %d", w.Code)
	}
}

// TestPresetCreatePreservesFollow: snapshotting a target that follows a whole repo
// (<ns>/*) stores the wildcard verbatim — "整仓" is a first-class preset member.
func TestPresetCreatePreservesFollow(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()
	const tg = "/tmp/sm-follow-t"
	s.cfg.Targets = []string{tg}
	s.cfg.Enabled = []config.EnabledEntry{{Skill: "repoF/*", Target: tg, Mode: config.ModeFollow}}

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req("POST", "/api/presets", s.token, map[string]string{"name": "g", "target": tg}))
	if w.Code != http.StatusOK {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	if len(s.cfg.Presets) != 1 || len(s.cfg.Presets[0].Skills) != 1 {
		t.Fatalf("expected one preset with one member: %+v", s.cfg.Presets)
	}
	if got := s.cfg.Presets[0].Skills[0]; got.Skill != "repoF/*" || got.Mode != config.ModeFollow {
		t.Fatalf("follow should be stored verbatim: %+v", got)
	}
}

// TestPresetEditPropagates: editing a preset syncs add/remove to targets where it
// was fully applied, leaves other targets alone, and keeps overlap selectors.
func TestPresetEditPropagates(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()
	const t1, t2, t3 = "/tmp/sm-ep-t1", "/tmp/sm-ep-t2", "/tmp/sm-ep-t3"
	do := func(method, target string, body any) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req(method, target, s.token, body))
		return w
	}
	// preset "g" = {X, Y}. Applied fully in t1; partially (only X) in t2; t3 none.
	// another preset "h" also has Y → Y is overlap-protected on removal.
	s.cfg.Presets = []config.Preset{
		{Name: "g", Skills: []config.PresetSkill{{Skill: "repoA/X"}, {Skill: "repoA/Y"}}},
		{Name: "h", Skills: []config.PresetSkill{{Skill: "repoA/Y"}}},
	}
	s.cfg.Enabled = []config.EnabledEntry{
		{Skill: "repoA/X", Target: t1}, {Skill: "repoA/Y", Target: t1}, // t1 fully applied
		{Skill: "repoA/X", Target: t2}, // t2 partial → not synced
	}
	// edit g → {X, Z}: add Z, remove Y.
	body := map[string]any{"name": "g", "skills": []map[string]string{{"skill": "repoA/X"}, {"skill": "repoA/Z"}}}
	if w := do("PUT", "/api/presets", body); w.Code != http.StatusOK {
		t.Fatalf("put: %d %s", w.Code, w.Body.String())
	}
	has := func(skill, target string) bool {
		for _, e := range s.cfg.Enabled {
			if e.Skill == skill && e.Target == target {
				return true
			}
		}
		return false
	}
	if !has("repoA/Z", t1) {
		t.Fatalf("added Z should reach fully-applied t1: %+v", s.cfg.Enabled)
	}
	if !has("repoA/Y", t1) {
		t.Fatalf("Y is overlap (still in preset h) → must stay in t1: %+v", s.cfg.Enabled)
	}
	if has("repoA/Z", t2) || has("repoA/Z", t3) {
		t.Fatalf("Z must NOT reach partial/never-applied targets: %+v", s.cfg.Enabled)
	}
	if !has("repoA/X", t2) {
		t.Fatalf("t2's own X must be untouched: %+v", s.cfg.Enabled)
	}
}

// TestPresetEditRemovesNonOverlap: a removed selector unique to the preset loses
// its link in the applied target.
func TestPresetEditRemovesNonOverlap(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()
	const t1 = "/tmp/sm-ep2-t1"
	s.cfg.Presets = []config.Preset{{Name: "g", Skills: []config.PresetSkill{{Skill: "repoA/X"}, {Skill: "repoA/Y"}}}}
	s.cfg.Enabled = []config.EnabledEntry{{Skill: "repoA/X", Target: t1}, {Skill: "repoA/Y", Target: t1}}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req("PUT", "/api/presets", s.token, map[string]any{"name": "g", "skills": []map[string]string{{"skill": "repoA/X"}}}))
	if w.Code != http.StatusOK {
		t.Fatalf("put: %d", w.Code)
	}
	for _, e := range s.cfg.Enabled {
		if e.Skill == "repoA/Y" {
			t.Fatalf("Y (unique, removed) should be unlinked in t1: %+v", s.cfg.Enabled)
		}
	}
}

// TestPresetDeleteClearsLinks: deleting a preset tears down its unique selectors'
// links across ALL targets, but preserves selectors still claimed by another
// preset (overlap), and never touches unrelated enabled skills.
func TestPresetDeleteClearsLinks(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()
	const t1, t2 = "/tmp/sm-del-t1", "/tmp/sm-del-t2"
	do := func(method, target string, body any) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req(method, target, s.token, body))
		return w
	}
	// preset "a" = {X, Y}; preset "b" = {Y} → Y overlaps and must survive.
	s.cfg.Presets = []config.Preset{
		{Name: "a", Skills: []config.PresetSkill{{Skill: "repoA/X"}, {Skill: "repoA/Y"}}},
		{Name: "b", Skills: []config.PresetSkill{{Skill: "repoA/Y"}}},
	}
	s.cfg.Enabled = []config.EnabledEntry{
		{Skill: "repoA/X", Target: t1}, {Skill: "repoA/X", Target: t2}, // unique to a → both cleared
		{Skill: "repoA/Y", Target: t1}, // shared with b → kept
		{Skill: "repoA/Z", Target: t1}, // unrelated → kept
	}

	w := do("DELETE", "/api/presets?name=a", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	var res struct {
		Removed int `json:"removed"`
		Shared  int `json:"shared"`
	}
	json.Unmarshal(w.Body.Bytes(), &res)
	if res.Removed != 2 || res.Shared != 1 {
		t.Fatalf("expected removed=2 shared=1, got %+v", res)
	}
	left := map[string]int{}
	for _, e := range s.cfg.Enabled {
		left[e.Skill]++
	}
	if left["repoA/X"] != 0 {
		t.Fatalf("X links should be cleared everywhere: %+v", s.cfg.Enabled)
	}
	if left["repoA/Y"] != 1 || left["repoA/Z"] != 1 {
		t.Fatalf("shared Y / unrelated Z must survive: %+v", s.cfg.Enabled)
	}
	if len(s.cfg.Presets) != 1 || s.cfg.Presets[0].Name != "b" {
		t.Fatalf("only preset b should remain: %+v", s.cfg.Presets)
	}
}
