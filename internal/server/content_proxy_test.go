package server

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func contentProxyServer(t *testing.T, upstreamURL string) *Server {
	t.Helper()
	t.Setenv(contentServiceURLEnv, upstreamURL)
	return newTestServer(t)
}

func TestContentProxyRequiresAuthenticationAndRejectsUpstreamURL(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = io.WriteString(w, `{}`)
	}))
	defer upstream.Close()
	s := contentProxyServer(t, upstream.URL)
	h := s.Handler()

	unauthenticated := httptest.NewRecorder()
	h.ServeHTTP(unauthenticated, req("GET", "/api/content/feed", "", nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("missing token status=%d, want 401", unauthenticated.Code)
	}
	calendarUnauthenticated := httptest.NewRecorder()
	h.ServeHTTP(calendarUnauthenticated, req("GET", "/api/content/reset-calendar", "", nil))
	if calendarUnauthenticated.Code != http.StatusUnauthorized || calls != 0 {
		t.Fatalf("calendar missing token status=%d upstream calls=%d, want 401 and zero calls", calendarUnauthenticated.Code, calls)
	}

	badURL := httptest.NewRecorder()
	h.ServeHTTP(badURL, req("GET", "/api/content/feed?upstreamURL=https%3A%2F%2Fevil.example", s.token, nil))
	if badURL.Code != http.StatusBadRequest || calls != 0 {
		t.Fatalf("upstreamURL status=%d upstream calls=%d, want 400 and zero calls", badURL.Code, calls)
	}
}

func TestContentFeedQueryDefaultsAndRejectsInvalid(t *testing.T) {
	var calls int
	var forwarded url.Values
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		forwarded = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}))
	defer upstream.Close()
	s := contentProxyServer(t, upstream.URL)
	h := s.Handler()

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req("GET", "/api/content/feed?kind=project&cursor=opaque%2Fcursor%20", s.token, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("valid query status=%d body=%q", w.Code, w.Body.String())
	}
	if forwarded.Get("kind") != "project" || forwarded.Get("view") != "weekly" || forwarded.Get("limit") != "20" || forwarded.Get("cursor") != "opaque/cursor " {
		t.Fatalf("forwarded query=%v, want project defaults and opaque cursor", forwarded)
	}
	if _, ok := forwarded["window"]; ok {
		t.Fatalf("project query forwarded unsupported window: %v", forwarded)
	}

	invalid := []string{
		"upstreamURL=https%3A%2F%2Fevil.example",
		"kind=news&kind=project",
		"q=a&q=b",
		"unknown=x",
		"kind=%zz",
		"limit=",
		"limit=0",
		"limit=nope",
		"kind=project&window=7d",
		"q=" + url.QueryEscape(strings.Repeat("中", 201)),
	}
	for _, query := range invalid {
		t.Run(query, func(t *testing.T) {
			before := calls
			got := httptest.NewRecorder()
			h.ServeHTTP(got, req("GET", "/api/content/feed?"+query, s.token, nil))
			if got.Code != http.StatusBadRequest || calls != before {
				t.Errorf("query %q status=%d upstream calls delta=%d, want 400 before upstream", query, got.Code, calls-before)
			}
		})
	}
}

func TestContentProxyMapsFixedRoutesAndForwardsETag(t *testing.T) {
	type upstreamCall struct {
		path        string
		query       url.Values
		ifNoneMatch string
		authorize   string
		cookie      string
		xCustom     string
	}
	var calls []upstreamCall
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, upstreamCall{
			path:        r.URL.Path,
			query:       r.URL.Query(),
			ifNoneMatch: r.Header.Get("If-None-Match"),
			authorize:   r.Header.Get("Authorization"),
			cookie:      r.Header.Get("Cookie"),
			xCustom:     r.Header.Get("X-Custom"),
		})
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
		w.Header().Set("ETag", `"content-v1"`)
		w.Header().Set("Set-Cookie", "secret=must-not-pass")
		w.Header().Set("X-Upstream-Debug", "must-not-pass")
		if r.Header.Get("If-None-Match") == `"content-v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	defer upstream.Close()
	s := contentProxyServer(t, upstream.URL+"/content-root")
	h := s.Handler()

	feedReq := req("GET", "/api/content/feed?kind=news", s.token, nil)
	feedReq.Header.Set("If-None-Match", `"old"`)
	feedReq.Header.Set("Cookie", "local=secret")
	feedReq.Header.Set("X-Custom", "local-only")
	feed := httptest.NewRecorder()
	h.ServeHTTP(feed, feedReq)
	if feed.Code != http.StatusOK || feed.Header().Get("ETag") != `"content-v1"` || feed.Header().Get("Cache-Control") == "" {
		t.Fatalf("feed response status=%d headers=%v body=%q", feed.Code, feed.Header(), feed.Body.String())
	}
	if feed.Header().Get("Set-Cookie") != "" || feed.Header().Get("X-Upstream-Debug") != "" {
		t.Fatalf("upstream response headers leaked: %v", feed.Header())
	}
	if len(calls) != 1 || calls[0].path != "/content-root/v1/feed" || calls[0].ifNoneMatch != `"old"` || calls[0].authorize != "" || calls[0].cookie != "" || calls[0].xCustom != "" {
		t.Fatalf("feed upstream call=%+v, want fixed path and header allowlist", calls)
	}
	if calls[0].query.Get("view") != "selected" || calls[0].query.Get("window") != "7d" || calls[0].query.Get("limit") != "20" {
		t.Fatalf("feed forwarded defaults=%v", calls[0].query)
	}

	conditional := req("GET", "/api/content/feed?kind=news", s.token, nil)
	conditional.Header.Set("If-None-Match", `"content-v1"`)
	notModified := httptest.NewRecorder()
	h.ServeHTTP(notModified, conditional)
	if notModified.Code != http.StatusNotModified || notModified.Body.Len() != 0 || notModified.Header().Get("ETag") != `"content-v1"` {
		t.Fatalf("304 response status=%d ETag=%q body=%q", notModified.Code, notModified.Header().Get("ETag"), notModified.Body.String())
	}

	for _, route := range []struct {
		local string
		want  string
	}{
		{local: "/api/content/entries/news_123", want: "/content-root/v1/entries/news_123"},
		{local: "/api/content/status", want: "/content-root/v1/status"},
		{local: "/api/content/reset-calendar", want: "/content-root/v1/reset-calendar"},
	} {
		before := len(calls)
		response := httptest.NewRecorder()
		h.ServeHTTP(response, req("GET", route.local, s.token, nil))
		if response.Code != http.StatusOK || len(calls) != before+1 || calls[before].path != route.want {
			t.Errorf("route %s status=%d calls=%+v, want mapped path %s", route.local, response.Code, calls[before:], route.want)
		}
	}
}

func TestContentResetCalendarProxyForwardsConditionalRequest(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/reset-calendar" || r.URL.RawQuery != "" {
			t.Errorf("upstream request=%s?%s, want fixed calendar route", r.URL.Path, r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `"calendar-v1"`)
		if r.Header.Get("If-None-Match") == `"calendar-v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = io.WriteString(w, `{"timezone":"Asia/Shanghai"}`)
	}))
	defer upstream.Close()
	s := contentProxyServer(t, upstream.URL)
	h := s.Handler()
	first := httptest.NewRecorder()
	h.ServeHTTP(first, req("GET", "/api/content/reset-calendar", s.token, nil))
	if first.Code != http.StatusOK || first.Header().Get("ETag") != `"calendar-v1"` || calls != 1 {
		t.Fatalf("initial proxy response status=%d ETag=%q calls=%d", first.Code, first.Header().Get("ETag"), calls)
	}
	conditional := req("GET", "/api/content/reset-calendar", s.token, nil)
	conditional.Header.Set("If-None-Match", `"calendar-v1"`)
	second := httptest.NewRecorder()
	h.ServeHTTP(second, conditional)
	if second.Code != http.StatusNotModified || second.Body.Len() != 0 || second.Header().Get("ETag") != `"calendar-v1"` || calls != 2 {
		t.Fatalf("conditional proxy response status=%d ETag=%q body=%q calls=%d", second.Code, second.Header().Get("ETag"), second.Body.String(), calls)
	}
}

func TestContentModelLeaderboardProxyUsesFixedRouteAndCategoryWhitelist(t *testing.T) {
	var calls []string
	var conditionalETags []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path+"?"+r.URL.RawQuery)
		conditionalETags = append(conditionalETags, r.Header.Get("If-None-Match"))
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("ETag", `"leaderboard-v1"`)
		if r.Header.Get("If-None-Match") == `"leaderboard-v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = io.WriteString(w, `{"category":"`+r.URL.Query().Get("category")+`"}`)
	}))
	defer upstream.Close()
	s := contentProxyServer(t, upstream.URL+"/content-root")
	h := s.Handler()
	unauthenticated := httptest.NewRecorder()
	h.ServeHTTP(unauthenticated, req(http.MethodGet, "/api/content/model-leaderboard", "", nil))
	if unauthenticated.Code != http.StatusUnauthorized || len(calls) != 0 {
		t.Fatalf("unauthenticated leaderboard status=%d upstream calls=%v", unauthenticated.Code, calls)
	}

	for _, tc := range []struct {
		local string
		want  string
	}{
		{local: "/api/content/model-leaderboard", want: "/content-root/v1/model-leaderboard?category=overall"},
		{local: "/api/content/model-leaderboard?category=coding", want: "/content-root/v1/model-leaderboard?category=coding"},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req(http.MethodGet, tc.local, s.token, nil))
		if w.Code != http.StatusOK || w.Header().Get("ETag") != `"leaderboard-v1"` || len(calls) == 0 || calls[len(calls)-1] != tc.want {
			t.Errorf("%s status=%d ETag=%q calls=%v, want fixed upstream request %s", tc.local, w.Code, w.Header().Get("ETag"), calls, tc.want)
		}
	}
	conditional := req(http.MethodGet, "/api/content/model-leaderboard?category=coding", s.token, nil)
	conditional.Header.Set("If-None-Match", `"leaderboard-v1"`)
	notModified := httptest.NewRecorder()
	h.ServeHTTP(notModified, conditional)
	if notModified.Code != http.StatusNotModified || notModified.Body.Len() != 0 || len(calls) != 3 || conditionalETags[2] != `"leaderboard-v1"` {
		t.Fatalf("conditional proxy response status=%d calls=%v etags=%v body=%q", notModified.Code, calls, conditionalETags, notModified.Body.String())
	}

	for _, query := range []string{
		"category=overall&category=coding",
		"category=unknown",
		"category=coding&extra=1",
		"category=",
		"category=%zz",
	} {
		before := len(calls)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req(http.MethodGet, "/api/content/model-leaderboard?"+query, s.token, nil))
		if w.Code != http.StatusBadRequest || len(calls) != before {
			t.Errorf("query %q status=%d upstream calls=%v, want 400 without forwarding", query, w.Code, calls[before:])
		}
	}
	posted := httptest.NewRecorder()
	h.ServeHTTP(posted, req(http.MethodPost, "/api/content/model-leaderboard", s.token, nil))
	if posted.Code != http.StatusMethodNotAllowed || len(calls) != 3 {
		t.Fatalf("POST model leaderboard status=%d upstream calls=%v, want 405 without forwarding", posted.Code, calls)
	}
}

func TestContentEntryRejectsInvalidIDBeforeUpstream(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = io.WriteString(w, `{}`)
	}))
	defer upstream.Close()
	s := contentProxyServer(t, upstream.URL)

	got := httptest.NewRecorder()
	s.Handler().ServeHTTP(got, req("GET", "/api/content/entries/bad!id", s.token, nil))
	if got.Code != http.StatusBadRequest || calls != 0 {
		t.Fatalf("status=%d upstream calls=%d, want 400 and zero calls", got.Code, calls)
	}
}

func TestContentProxyConfigFailureDoesNotBreakDaemon(t *testing.T) {
	for _, configuredURL := range []string{"", "ftp://example.test", "http:///missing-host", "https://user:password@example.test/path", "https://example.test/path?token=x", "https://example.test/path#fragment"} {
		t.Run(configuredURL, func(t *testing.T) {
			t.Setenv(contentServiceURLEnv, configuredURL)
			s := newTestServer(t)
			h := s.Handler()

			localStatus := httptest.NewRecorder()
			h.ServeHTTP(localStatus, req("GET", "/api/status", s.token, nil))
			if localStatus.Code != http.StatusOK {
				t.Fatalf("local daemon status=%d, want 200", localStatus.Code)
			}

			contentStatus := httptest.NewRecorder()
			h.ServeHTTP(contentStatus, req("GET", "/api/content/status", s.token, nil))
			if contentStatus.Code != http.StatusServiceUnavailable {
				t.Fatalf("content proxy status=%d body=%q, want 503", contentStatus.Code, contentStatus.Body.String())
			}
		})
	}
}

func TestContentServiceURLEnvironmentOverridesBuildValue(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}))
	defer upstream.Close()
	previousBuildValue := ContentServiceURL
	ContentServiceURL = "ftp://invalid-build-value"
	t.Cleanup(func() { ContentServiceURL = previousBuildValue })
	s := contentProxyServer(t, upstream.URL)
	got := httptest.NewRecorder()
	s.Handler().ServeHTTP(got, req("GET", "/api/content/status", s.token, nil))
	if got.Code != http.StatusOK {
		t.Fatalf("environment override status=%d body=%q", got.Code, got.Body.String())
	}
}

func TestContentServiceURLUsesBuildValueWhenEnvironmentIsMissing(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}))
	defer upstream.Close()
	previousBuildValue := ContentServiceURL
	ContentServiceURL = upstream.URL
	t.Cleanup(func() { ContentServiceURL = previousBuildValue })
	t.Setenv(contentServiceURLEnv, "")
	if err := os.Unsetenv(contentServiceURLEnv); err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t)
	got := httptest.NewRecorder()
	s.Handler().ServeHTTP(got, req("GET", "/api/content/status", s.token, nil))
	if got.Code != http.StatusOK {
		t.Fatalf("build URL status=%d body=%q", got.Code, got.Body.String())
	}
}

func TestContentProxyRejectsInvalidQueryOnEntryAndStatus(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = io.WriteString(w, `{}`)
	}))
	defer upstream.Close()
	s := contentProxyServer(t, upstream.URL)
	for _, target := range []string{
		"/api/content/entries/news_123?unexpected=1",
		"/api/content/status?limit=%zz",
		"/api/content/reset-calendar?month=2026-10",
		"/api/content/reset-calendar?month=%zz",
	} {
		got := httptest.NewRecorder()
		s.Handler().ServeHTTP(got, req("GET", target, s.token, nil))
		if got.Code != http.StatusBadRequest || calls != 0 {
			t.Errorf("target %s status=%d calls=%d, want 400 before upstream", target, got.Code, calls)
		}
	}
}

func TestContentProxyDoesNotFollowRedirectsAndSanitizesErrors(t *testing.T) {
	var redirected int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/status":
			http.Redirect(w, r, "/redirect-target", http.StatusFound)
		case "/redirect-target":
			redirected++
			_, _ = io.WriteString(w, `{}`)
		case "/v1/feed":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, "secret internal hostname and stack trace")
		}
	}))
	defer upstream.Close()
	s := contentProxyServer(t, upstream.URL)
	h := s.Handler()

	redirect := httptest.NewRecorder()
	h.ServeHTTP(redirect, req("GET", "/api/content/status", s.token, nil))
	if redirect.Code != http.StatusBadGateway || redirected != 0 || redirect.Header().Get("Location") != "" {
		t.Fatalf("redirect response status=%d redirected=%d headers=%v, want safe 502 without following", redirect.Code, redirected, redirect.Header())
	}

	errorResponse := httptest.NewRecorder()
	h.ServeHTTP(errorResponse, req("GET", "/api/content/feed", s.token, nil))
	if errorResponse.Code != http.StatusBadGateway || strings.Contains(errorResponse.Body.String(), "secret internal") || strings.Contains(errorResponse.Body.String(), "stack trace") {
		t.Fatalf("upstream error response status=%d body=%q, want sanitized 502", errorResponse.Code, errorResponse.Body.String())
	}
}

func TestContentProxyPreservesSafeContentErrorStatuses(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "news_missing"):
			w.WriteHeader(http.StatusNotFound)
		case strings.HasSuffix(r.URL.Path, "news_withdrawn"):
			w.WriteHeader(http.StatusGone)
		case r.URL.Path == "/v1/feed":
			w.WriteHeader(http.StatusConflict)
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_, _ = io.WriteString(w, "secret upstream details")
	}))
	defer upstream.Close()
	s := contentProxyServer(t, upstream.URL)
	for _, tc := range []struct {
		target string
		status int
		code   string
	}{
		{target: "/api/content/entries/news_missing", status: http.StatusNotFound, code: "not_found"},
		{target: "/api/content/entries/news_withdrawn", status: http.StatusGone, code: "withdrawn"},
		{target: "/api/content/feed?cursor=expired", status: http.StatusConflict, code: "cursor_expired"},
		{target: "/api/content/status", status: http.StatusServiceUnavailable, code: "unavailable"},
	} {
		got := httptest.NewRecorder()
		s.Handler().ServeHTTP(got, req("GET", tc.target, s.token, nil))
		if got.Code != tc.status || !strings.Contains(got.Body.String(), `"error_code":"`+tc.code+`"`) || strings.Contains(got.Body.String(), "secret upstream") {
			t.Errorf("target %s status=%d body=%q, want sanitized %d/%s", tc.target, got.Code, got.Body.String(), tc.status, tc.code)
		}
	}
}

func TestContentProxyBoundsResponseBodyAndUsesEightSecondTimeout(t *testing.T) {
	var oversize bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if oversize {
			_, _ = io.WriteString(w, `{"data":"`+strings.Repeat("x", 8*1024*1024)+`"}`)
			return
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	defer upstream.Close()
	s := contentProxyServer(t, upstream.URL)
	h := s.Handler()
	if s.contentHTTPClient.Timeout != 8*time.Second {
		t.Fatalf("content HTTP timeout=%s, want 8s", s.contentHTTPClient.Timeout)
	}

	first := httptest.NewRecorder()
	h.ServeHTTP(first, req("GET", "/api/content/status", s.token, nil))
	if first.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", first.Code, first.Body.String())
	}
	oversize = true
	second := httptest.NewRecorder()
	h.ServeHTTP(second, req("GET", "/api/content/status", s.token, nil))
	if second.Code != http.StatusBadGateway || strings.Contains(second.Body.String(), "xxxx") {
		t.Fatalf("oversized response status=%d body prefix=%q, want safe 502", second.Code, second.Body.String()[:min(80, second.Body.Len())])
	}
}

func TestContentProxyValidIDPatternBoundaries(t *testing.T) {
	for _, id := range []string{"a", strings.Repeat("z", 160)} {
		t.Run(fmt.Sprintf("valid-%d", len(id)), func(t *testing.T) {
			upstreamPath := ""
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upstreamPath = r.URL.Path
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{}`)
			}))
			defer upstream.Close()
			s := contentProxyServer(t, upstream.URL)
			got := httptest.NewRecorder()
			s.Handler().ServeHTTP(got, req("GET", "/api/content/entries/"+id, s.token, nil))
			if got.Code != http.StatusOK || upstreamPath != "/v1/entries/"+id {
				t.Fatalf("status=%d upstream path=%q, want valid ID mapped", got.Code, upstreamPath)
			}
		})
	}
}
