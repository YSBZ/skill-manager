package server

import "testing"

func TestParseSkillHubSearch(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string // expected slugs, in order
	}{
		{"empty", "", nil},
		{"real items envelope", `{"ok":true,"items":[{"namespace":"global","slug":"cover-design","latestVersion":"20260716.134509","summary":"covers"},{"namespace":"github","slug":"api-design","latestVersion":"0.0.0","summary":"apis"}],"total":2}`, []string{"cover-design", "api-design"}},
		{"envelope results", `{"ok":true,"results":[{"slug":"pdf-parser","description":"parse pdf"},{"slug":"csv-tool"}]}`, []string{"pdf-parser", "csv-tool"}},
		{"envelope skills", `{"ok":true,"skills":[{"name":"web-scrape","version":"1.2.0"}]}`, []string{"web-scrape"}},
		{"envelope data", `{"data":[{"id":"img-gen"}]}`, []string{"img-gen"}},
		{"bare array", `[{"slug":"a"},{"slug":"b"}]`, []string{"a", "b"}},
		{"leading noise then json", "Fetching...\n{\"results\":[{\"slug\":\"x\"}]}", []string{"x"}},
		{"drops entries without slug/name/id", `{"results":[{"description":"no id"},{"slug":"keep"}]}`, []string{"keep"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseSkillHubSearch(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("got %d results, want %d: %+v", len(got), len(c.want), got)
			}
			for i, w := range c.want {
				if got[i].Slug != w {
					t.Errorf("result[%d].Slug = %q, want %q", i, got[i].Slug, w)
				}
			}
		})
	}
}

func TestSkillHubFieldMapping(t *testing.T) {
	got := parseSkillHubSearch(`{"results":[{"slug":"pdf","displayName":"PDF Parser","namespace":"team","version":"2.0.0","description":"d"}]}`)
	if len(got) != 1 {
		t.Fatalf("want 1 result, got %d", len(got))
	}
	r := got[0]
	if r.Slug != "pdf" || r.Name != "PDF Parser" || r.Namespace != "team" || r.Version != "2.0.0" || r.Description != "d" {
		t.Errorf("field mapping wrong: %+v", r)
	}
	// real @astron-team/skillhub fields: latestVersion + summary.
	real := parseSkillHubSearch(`{"items":[{"slug":"x","namespace":"global","latestVersion":"20260716.1","summary":"s"}]}`)
	if len(real) != 1 || real[0].Version != "20260716.1" || real[0].Description != "s" || real[0].Namespace != "global" {
		t.Errorf("real-format mapping wrong: %+v", real)
	}
}

func TestJSONOK(t *testing.T) {
	if !jsonOK(`{"ok":true,"foo":1}`) {
		t.Error("ok:true not detected")
	}
	if jsonOK(`{"ok":false,"message":"nope"}`) {
		t.Error("ok:false wrongly detected as ok")
	}
	if !jsonOK("noise\n{\"ok\":true}") {
		t.Error("leading noise before json object should still parse")
	}
}
