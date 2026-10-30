package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"skillmanage/internal/toolchain"
)

func TestStatusReportsNodeDependencySnapshot(t *testing.T) {
	cases := []struct {
		name        string
		state       string
		version     string
		npxPath     string
		wantNpx     bool
		wantVersion string
	}{
		{name: "missing", state: "missing"},
		{name: "too old", state: "too_old", version: "20.11.1", wantVersion: "20.11.1"},
		{name: "ready", state: "ready", version: "22.20.0", npxPath: "/managed/node", wantNpx: true, wantVersion: "22.20.0"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestServer(t)
			s.npxPath = tt.npxPath
			s.nodeDependency = &toolchain.NodeDependency{
				State: tt.state, Version: tt.version, RequiredVersion: "22.20.0", DownloadURL: "https://nodejs.org/en/download",
			}
			w := httptest.NewRecorder()
			s.handleStatus(w, req("GET", "/api/status", s.token, nil))
			if w.Code != http.StatusOK {
				t.Fatalf("status code = %d, want 200", w.Code)
			}
			var body struct {
				NpxAvailable   bool                      `json:"npxAvailable"`
				NodeDependency *toolchain.NodeDependency `json:"nodeDependency"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.NpxAvailable != tt.wantNpx {
				t.Errorf("npxAvailable = %v, want %v", body.NpxAvailable, tt.wantNpx)
			}
			if body.NodeDependency == nil {
				t.Fatal("status must include the Node dependency snapshot")
			}
			if body.NodeDependency.State != tt.state || body.NodeDependency.Version != tt.wantVersion || body.NodeDependency.RequiredVersion != "22.20.0" || body.NodeDependency.DownloadURL != "https://nodejs.org/en/download" {
				t.Errorf("nodeDependency = %+v", body.NodeDependency)
			}
		})
	}
}

func TestNodeRequiringAPIActionsRejectMissingOrOldNodeBeforeRunner(t *testing.T) {
	cases := []struct {
		name string
		run  func(*Server, *httptest.ResponseRecorder)
	}{
		{name: "skills.sh add", run: func(s *Server, w *httptest.ResponseRecorder) {
			s.handleSkillsShAdd(w, req("POST", "/api/skillssh/add", s.token, map[string]string{"pkg": "owner/repo@skill"}))
		}},
		{name: "content discovery install preflight", run: func(s *Server, w *httptest.ResponseRecorder) {
			s.handleSkillsShAdd(w, req("POST", "/api/skillssh/add", s.token, contentInstallRequestBody()))
		}},
		{name: "skills.sh update all", run: func(s *Server, w *httptest.ResponseRecorder) {
			s.handleUpdateSkillsShAll(w, req("POST", "/api/skillssh/update-all", s.token, map[string]any{}))
		}},
		{name: "skills.sh update one", run: func(s *Server, w *httptest.ResponseRecorder) {
			s.handleUpdateSkillsShOne(w, req("POST", "/api/skillssh/update", s.token, map[string]string{"name": "installed-skill"}))
		}},
		{name: "skills.sh remove", run: func(s *Server, w *httptest.ResponseRecorder) {
			s.handleSkillsShRemove(w, req("POST", "/api/skillssh/remove", s.token, map[string]string{"name": "installed-skill"}))
		}},
		{name: "directory source update", run: func(s *Server, w *httptest.ResponseRecorder) {
			s.handleDirSourceUpdate(w, req("POST", "/api/dirsource/update", s.token, map[string]string{"name": "installed-skill"}))
		}},
		{name: "SkillHub install", run: func(s *Server, w *httptest.ResponseRecorder) {
			s.handleSkillHubInstall(w, req("POST", "/api/skillhub/install", s.token, map[string]string{"slug": "skill"}))
		}},
		{name: "SkillHub remove", run: func(s *Server, w *httptest.ResponseRecorder) {
			s.handleSkillHubRemove(w, req("POST", "/api/skillhub/remove", s.token, map[string]string{"slug": "skill"}))
		}},
	}

	for _, state := range []struct {
		name    string
		state   string
		version string
	}{
		{name: "missing", state: "missing"},
		{name: "too old", state: "too_old", version: "20.11.1"},
	} {
		t.Run(state.name, func(t *testing.T) {
			for _, action := range cases {
				t.Run(action.name, func(t *testing.T) {
					s := newTestServer(t)
					fr := &fakeRunner{}
					s.npxPath = ""
					s.nodeDependency = &toolchain.NodeDependency{
						State: state.state, Version: state.version, RequiredVersion: "22.20.0", DownloadURL: "https://nodejs.org/en/download",
					}
					s.runner = fr
					w := httptest.NewRecorder()
					action.run(s, w)
					if w.Code != http.StatusPreconditionFailed {
						t.Fatalf("status = %d, body=%s; want 412", w.Code, w.Body.String())
					}
					for _, required := range []string{"22.20.0", "https://nodejs.org/en/download", "重新打开"} {
						if !strings.Contains(w.Body.String(), required) {
							t.Errorf("response %q missing guidance %q", w.Body.String(), required)
						}
					}
					stateHint := "未检测到 Node.js"
					if state.state == "too_old" {
						stateHint = "20.11.1"
					}
					if !strings.Contains(w.Body.String(), stateHint) {
						t.Errorf("response %q missing state-specific hint %q", w.Body.String(), stateHint)
					}
					if strings.Contains(w.Body.String(), "也可手动运行") || strings.Contains(w.Body.String(), "npx skills") {
						t.Errorf("managed Node failure should not suggest terminal commands: %q", w.Body.String())
					}
					if fr.calls != 0 {
						t.Errorf("fake runner calls = %d, want 0", fr.calls)
					}
				})
			}
		})
	}
}

func TestReadyNodeAllowsSkillsInstallRunner(t *testing.T) {
	s := newTestServer(t)
	fr := &fakeRunner{addStdout: "✓ installed\nInstalled 1 skill"}
	s.npxPath = "/managed/node"
	s.nodeDependency = &toolchain.NodeDependency{State: "ready", Version: "22.20.0", RequiredVersion: "22.20.0", DownloadURL: "https://nodejs.org/en/download"}
	s.runner = fr
	w := httptest.NewRecorder()
	s.handleSkillsShAdd(w, req("POST", "/api/skillssh/add", s.token, map[string]string{"pkg": "owner/repo@skill"}))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s; want 200", w.Code, w.Body.String())
	}
	if fr.calls != 1 {
		t.Fatalf("runner calls = %d, want 1", fr.calls)
	}
}

func TestLegacyNpxUnavailableHintIsRetainedWithoutNodeSnapshot(t *testing.T) {
	hint := npxUnavailableErr("npx skills add", nil)
	if !strings.Contains(hint, "npx 不可用") || !strings.Contains(hint, "nodejs.org") {
		t.Fatalf("legacy hint = %q", hint)
	}
	if !strings.Contains(hint, "或手动运行：npx skills add") {
		t.Fatalf("legacy hint should retain its manual command: %q", hint)
	}
}
