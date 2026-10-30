package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// UpdateFeed / UpdateToken are the online-update feed URL and its optional
// read-only token, set at build time. Public release builds use their configured
// public feed; local builds can override it through ignored build configuration.
var (
	UpdateFeed  string
	UpdateToken string
)

// updateCheckTTL bounds how often the feed is polled — the UI may query the
// endpoint on each load/focus, but the configured feed is polled at most this often.
const updateCheckTTL = time.Hour

// latestManifest is the shape of the feed's latest.json.
type latestManifest struct {
	Version     string            `json:"version"`
	ReleaseCode string            `json:"releaseCode,omitempty"`
	Notes       string            `json:"notes"`
	Assets      map[string]string `json:"assets"`
	SHA256      map[string]string `json:"sha256"`
}

// handleUpdateCheck reports whether a newer release is available, per the
// configured feed. Disabled silently when no feed is built in. Network failures
// degrade to "no newer version", never an error banner.
func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if UpdateFeed == "" {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	s.mu.Lock()
	if s.updateCache != nil && !s.updateAt.IsZero() && time.Since(s.updateAt) < updateCheckTTL {
		cached := s.updateCache
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, cached)
		return
	}
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(s.detachedCtx(), 12*time.Second)
	defer cancel()
	m, err := fetchLatestManifest(ctx, UpdateFeed, UpdateToken)
	if err != nil {
		// An unavailable update feed degrades quietly; do not cache the failure.
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled": true, "newer": false, "current": Version,
			"error": "更新源暂时不可用，请检查网络连接。",
		})
		return
	}
	_, canSelfUpdate := desktopAssetKey()
	resp := map[string]any{
		"enabled":       true,
		"current":       Version,
		"latest":        m.Version,
		"newer":         releaseLess(Version, ReleaseCode, m.Version, m.ReleaseCode),
		"notes":         m.Notes,
		"canSelfUpdate": canSelfUpdate, // desktop on a supported OS → one-click update
	}
	s.mu.Lock()
	s.updateCache = resp
	s.updateAt = time.Now()
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, resp)
}

// fetchLatestManifest GETs the feed's latest.json (configured service raw/release asset),
// authenticating with the read-only token via the feed token header header.
func fetchLatestManifest(ctx context.Context, feed, token string) (*latestManifest, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feed, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("PRIVATE-TOKEN", token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("feed status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var m latestManifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	if strings.TrimSpace(m.Version) == "" {
		return nil, fmt.Errorf("feed missing version")
	}
	return &m, nil
}

// versionLess reports whether a < b by numeric semver segments, so "5.1.9" sorts
// before "5.1.10" (a string compare would get this wrong). A leading "v" is
// ignored; non-numeric segments fall back to a lexical tiebreak.
func versionLess(a, b string) bool { return compareVersion(a, b) < 0 }

func releaseLess(currentVersion, currentCode, latestVersion, latestCode string) bool {
	cur, curErr := strconv.ParseUint(strings.TrimSpace(currentCode), 10, 64)
	lat, latErr := strconv.ParseUint(strings.TrimSpace(latestCode), 10, 64)
	if curErr == nil && latErr == nil && cur > 0 && lat > 0 {
		return cur < lat
	}
	return versionLess(currentVersion, latestVersion)
}

func compareVersion(a, b string) int {
	as := strings.Split(strings.TrimPrefix(strings.TrimSpace(a), "v"), ".")
	bs := strings.Split(strings.TrimPrefix(strings.TrimSpace(b), "v"), ".")
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		av, bv := "0", "0" // a missing segment counts as 0, so "5.1" == "5.1.0"
		if i < len(as) && as[i] != "" {
			av = as[i]
		}
		if i < len(bs) && bs[i] != "" {
			bv = bs[i]
		}
		ai, aerr := strconv.Atoi(av)
		bi, berr := strconv.Atoi(bv)
		if aerr == nil && berr == nil {
			if ai != bi {
				if ai < bi {
					return -1
				}
				return 1
			}
			continue
		}
		if av != bv {
			if av < bv {
				return -1
			}
			return 1
		}
	}
	return 0
}
