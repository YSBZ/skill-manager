package contentgithub

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var testNow = time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)

func TestDefaultConfigBounds(t *testing.T) {
	c := DefaultConfig()
	if c.QueryVersion != "github-v1" || len(c.Queries) != 6 ||
		c.MaxCandidates != 100 || c.MaxPagesPerQuery != 2 ||
		c.PageSize != 10 || c.CoreRequestsPerHour != 40 ||
		c.MaxSkillsPerRepository != 20 || c.DiscoveryInterval != 24*time.Hour {
		t.Fatalf("unexpected config: %+v", c)
	}
	wantQueries := []string{
		"topic:agent-skills is:public archived:false fork:false",
		"topic:claude-skills is:public archived:false fork:false",
		"topic:codex-skills is:public archived:false fork:false",
		"SKILL.md in:readme is:public archived:false fork:false",
		"agent workflow in:name,description,readme is:public archived:false fork:false",
		"mcp server in:name,description,readme is:public archived:false fork:false",
	}
	for i := range wantQueries {
		if c.Queries[i] != wantQueries[i] {
			t.Fatalf("query %d = %q, want %q", i, c.Queries[i], wantQueries[i])
		}
	}
}

func TestNewClientCapsTimeoutAndTrimsToken(t *testing.T) {
	injected := &http.Client{Timeout: time.Hour}
	c := NewClient("https://api.github.com/", injected, "  secret-token \n")
	if c.baseURL != "https://api.github.com" {
		t.Fatalf("baseURL = %q", c.baseURL)
	}
	if c.hc == injected || c.hc.Timeout != 20*time.Second {
		t.Fatalf("injected client must be cloned and capped at 20s: client=%p timeout=%s", c.hc, c.hc.Timeout)
	}
	if injected.Timeout != time.Hour {
		t.Fatalf("NewClient mutated injected client timeout to %s", injected.Timeout)
	}
	if c.token != "secret-token" {
		t.Fatalf("token = %q", c.token)
	}
	if got := NewClient("https://api.github.com", nil, "").hc.Timeout; got != 20*time.Second {
		t.Fatalf("default timeout = %s, want 20s", got)
	}
	short := &http.Client{Timeout: time.Second}
	if got := NewClient("https://api.github.com", short, "").hc.Timeout; got != time.Second {
		t.Fatalf("short timeout = %s, want 1s", got)
	}
}

func TestNewCollectorAppliesBoundedConfig(t *testing.T) {
	cfg := Config{QueryVersion: "custom", Queries: []string{"topic:agent-skills"},
		MaxCandidates: 101, MaxPagesPerQuery: 3, PageSize: 31,
		CoreRequestsPerHour: 41, MaxSkillsPerRepository: 21}
	client := NewClient("https://api.github.com", nil, "")
	c := NewCollector(nil, client, cfg)
	if c.cfg.QueryVersion != "custom" || len(c.cfg.Queries) != 1 ||
		c.cfg.MaxCandidates != 100 || c.cfg.MaxPagesPerQuery != 2 || c.cfg.PageSize != 10 ||
		c.cfg.CoreRequestsPerHour != 40 || c.cfg.MaxSkillsPerRepository != 20 ||
		c.cfg.DiscoveryInterval != 24*time.Hour || client.coreBudget != 40 {
		t.Fatalf("unexpected normalized config: cfg=%+v coreBudget=%d", c.cfg, client.coreBudget)
	}
	cfg.Queries[0] = "mutated"
	if c.cfg.Queries[0] != "topic:agent-skills" {
		t.Fatalf("collector config aliases caller query slice: %q", c.cfg.Queries[0])
	}

	defaults := NewCollector(nil, client, Config{})
	if defaults.cfg.QueryVersion != "github-v1" || len(defaults.cfg.Queries) != 6 {
		t.Fatalf("empty config did not use defaults: %+v", defaults.cfg)
	}
}

func TestGetJSONUsesGitHubHeadersAndAuthorizationOnly(t *testing.T) {
	var hit atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/repos/octo/demo" {
			t.Errorf("request = %s %s", r.Method, r.URL.RequestURI())
		}
		if r.URL.Query().Get("per_page") != "30" {
			t.Errorf("query = %v", r.URL.Query())
		}
		if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
			t.Errorf("Accept = %q", got)
		}
		if got := r.Header.Get("X-GitHub-Api-Version"); got != "2022-11-28" {
			t.Errorf("API version = %q", got)
		}
		if got := r.Header.Get("User-Agent"); got != "SkillManager-content-discovery" {
			t.Errorf("User-Agent = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret-token" {
			t.Errorf("Authorization = %q", got)
		}
		if strings.Contains(r.URL.RequestURI(), "secret-token") {
			t.Errorf("token leaked into request URI: %q", r.URL.RequestURI())
		}
		for name, values := range r.Header {
			if strings.EqualFold(name, "Authorization") {
				continue
			}
			if strings.Contains(strings.Join(values, ","), "secret-token") {
				t.Errorf("token leaked into %s header", name)
			}
		}
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "4999")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	c := NewClient(server.URL, nil, " secret-token ")
	var dst struct {
		OK bool `json:"ok"`
	}
	q := url.Values{"per_page": {"30"}}
	if err := c.getJSON(context.Background(), testNow, "core", "/repos/octo/demo", q, &dst); err != nil {
		t.Fatalf("getJSON: %v", err)
	}
	if hit.Load() != 1 || !dst.OK {
		t.Fatalf("request count=%d decoded=%+v", hit.Load(), dst)
	}
}

func TestGetJSONRejectsUnfixedEndpointWithoutRequest(t *testing.T) {
	var hit atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit.Add(1)
	}))
	defer server.Close()
	c := NewClient(server.URL, nil, "")
	for _, path := range []string{"//attacker.test/steal", "https://attacker.test/steal", "/repos/../steal", "/repos/%2e%2e/steal", "/repos/x?token=y"} {
		if err := c.getJSON(context.Background(), testNow, "core", path, nil, &struct{}{}); err == nil {
			t.Errorf("getJSON(%q) unexpectedly succeeded", path)
		}
	}
	if hit.Load() != 0 {
		t.Fatalf("unsafe endpoint paths sent %d requests", hit.Load())
	}
}

func TestGetJSONDoesNotFollowOffOriginRedirect(t *testing.T) {
	var outsideHit atomic.Int32
	outside := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		outsideHit.Add(1)
	}))
	defer outside.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, outside.URL+"/capture", http.StatusFound)
	}))
	defer server.Close()
	c := NewClient(server.URL, nil, "secret-token")
	err := c.getJSON(context.Background(), testNow, "core", "/repos/octo/demo", nil, &struct{}{})
	var reqErr *requestError
	if !errors.As(err, &reqErr) || reqErr.status != http.StatusFound {
		t.Fatalf("redirect error = %v", err)
	}
	if outsideHit.Load() != 0 {
		t.Fatalf("off-origin redirect reached destination %d times", outsideHit.Load())
	}
}

func TestGetJSONSanitizesRateLimitErrorAndPauses(t *testing.T) {
	const token = "top-secret-token"
	const bodySecret = "private response body"
	var hit atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit.Add(1)
		w.Header().Set("Retry-After", "45")
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", fmt.Sprint(testNow.Add(time.Minute).Unix()))
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprintf(w, "%s %s", bodySecret, token)
	}))
	defer server.Close()

	c := NewClient(server.URL, nil, token)
	err := c.getJSON(context.Background(), testNow, "core", "/repos/octo/demo", nil, &struct{}{})
	var reqErr *requestError
	if !errors.As(err, &reqErr) || reqErr.status != http.StatusForbidden || reqErr.resource != "core" ||
		!reqErr.rateLimited || !reqErr.retryAt.Equal(testNow.Add(45*time.Second)) {
		t.Fatalf("unexpected request error: %#v (%v)", reqErr, err)
	}
	if strings.Contains(err.Error(), bodySecret) || strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "Authorization") {
		t.Fatalf("request error leaked sensitive data: %v", err)
	}
	if !errors.Is(c.getJSON(context.Background(), testNow.Add(time.Second), "core", "/repos/octo/demo", nil, &struct{}{}), errRequestBudget) {
		t.Fatal("rate limited core resource was not paused")
	}
	if hit.Load() != 1 {
		t.Fatalf("paused request reached server, hit count=%d", hit.Load())
	}
}

func TestGetJSONRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"ok":true}`+strings.Repeat(" ", (8<<20)+1))
	}))
	defer server.Close()
	c := NewClient(server.URL, nil, "")
	err := c.getJSON(context.Background(), testNow, "core", "/repos/octo/demo", nil, &struct{}{})
	if err == nil || !strings.Contains(err.Error(), "8 MiB") {
		t.Fatalf("oversized response error = %v", err)
	}
}

func TestGetJSONTagsInvalidResponseJSONWithoutLeakingBody(t *testing.T) {
	const privateBody = "private malformed response"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, privateBody)
	}))
	defer server.Close()

	client := NewClient(server.URL, nil, "")
	err := client.getJSON(context.Background(), testNow, "core", "/repos/octo/demo", nil, &struct{}{})
	if !errors.Is(err, errInvalidResponseJSON) {
		t.Fatalf("invalid response error=%v, want errInvalidResponseJSON", err)
	}
	if err.Error() != "github core response JSON is invalid" || strings.Contains(err.Error(), privateBody) {
		t.Fatalf("invalid response error must retain sanitized text: %v", err)
	}
}

func TestGetJSONPreservesContextCancellationWithoutLeakingURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("canceled request reached server")
	}))
	defer server.Close()
	c := NewClient(server.URL, nil, "secret-token")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := c.getJSON(ctx, testNow, "core", "/repos/octo/demo", url.Values{"q": {"secret-token"}}, &struct{}{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", err)
	}
	if strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), server.URL) {
		t.Fatalf("transport error leaked secret or URL: %v", err)
	}
}

func TestCoreBudgetStopsAtFortyRequestsPerHour(t *testing.T) {
	var hit atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit.Add(1)
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "5000")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	c := NewClient(server.URL, nil, "")
	for i := 0; i < 40; i++ {
		if err := c.getJSON(context.Background(), testNow.Add(time.Duration(i)*time.Second), "core", "/repos/octo/demo", nil, &struct{}{}); err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
	}
	err := c.getJSON(context.Background(), testNow.Add(40*time.Second), "core", "/repos/octo/demo", nil, &struct{}{})
	if !errors.Is(err, errRequestBudget) {
		t.Fatalf("41st request error = %v, want budget pause", err)
	}
	if hit.Load() != 40 {
		t.Fatalf("server saw %d requests, want 40", hit.Load())
	}
	if err := c.getJSON(context.Background(), testNow.Add(time.Hour), "core", "/repos/octo/demo", nil, &struct{}{}); err != nil {
		t.Fatalf("request after one hour: %v", err)
	}
}

func TestSearchAndCoreBudgetsAreIndependent(t *testing.T) {
	var coreHit, searchHit atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search/repositories" {
			searchHit.Add(1)
			w.Header().Set("X-RateLimit-Limit", "1")
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", fmt.Sprint(testNow.Add(time.Minute).Unix()))
		} else {
			coreHit.Add(1)
			w.Header().Set("X-RateLimit-Limit", "5000")
			w.Header().Set("X-RateLimit-Remaining", "4999")
			w.Header().Set("X-RateLimit-Reset", fmt.Sprint(testNow.Add(time.Hour).Unix()))
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	c := NewClient(server.URL, nil, "")
	if err := c.getJSON(context.Background(), testNow, "search", "/search/repositories", nil, &struct{}{}); err != nil {
		t.Fatalf("first search: %v", err)
	}
	if err := c.getJSON(context.Background(), testNow, "core", "/repos/octo/demo", nil, &struct{}{}); err != nil {
		t.Fatalf("core after search depletion: %v", err)
	}
	if err := c.getJSON(context.Background(), testNow.Add(time.Second), "search", "/search/repositories", nil, &struct{}{}); !errors.Is(err, errRequestBudget) {
		t.Fatalf("second search error = %v, want budget pause", err)
	}
	if coreHit.Load() != 1 || searchHit.Load() != 1 {
		t.Fatalf("core hits=%d search hits=%d", coreHit.Load(), searchHit.Load())
	}
}

func TestSearchRemainingAdmissionAllowsOnlyOneConcurrentRequest(t *testing.T) {
	var hit atomic.Int32
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit.Add(1)
		entered <- struct{}{}
		<-release
		w.Header().Set("X-RateLimit-Limit", "30")
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", fmt.Sprint(testNow.Add(time.Minute).Unix()))
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	c := NewClient(server.URL, nil, "")
	headers := make(http.Header)
	headers.Set("X-RateLimit-Limit", "30")
	headers.Set("X-RateLimit-Remaining", "1")
	headers.Set("X-RateLimit-Reset", fmt.Sprint(testNow.Add(time.Minute).Unix()))
	c.observe(testNow, "search", headers)

	start := make(chan struct{})
	ready := make(chan struct{}, 2)
	results := make(chan error, 2)
	for range 2 {
		go func() {
			ready <- struct{}{}
			<-start
			results <- c.getJSON(context.Background(), testNow, "search", "/search/repositories", nil, &struct{}{})
		}()
	}
	<-ready
	<-ready
	close(start)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("no search request reached server")
	}
	secondReachedServer := false
	select {
	case <-entered:
		secondReachedServer = true
	case <-time.After(100 * time.Millisecond):
	}
	close(release)

	var success, paused int
	for range 2 {
		err := <-results
		if err == nil {
			success++
		} else if errors.Is(err, errRequestBudget) {
			paused++
		} else {
			t.Fatalf("search request error = %v", err)
		}
	}
	if secondReachedServer || hit.Load() != 1 || success != 1 || paused != 1 {
		t.Fatalf("secondReachedServer=%v serverHits=%d success=%d paused=%d", secondReachedServer, hit.Load(), success, paused)
	}
}

func TestSearchRemainingCannotIncreaseFromLateSameWindowHeaders(t *testing.T) {
	c := NewClient("https://api.github.com", nil, "")
	reset := testNow.Add(time.Minute)
	newer := make(http.Header)
	newer.Set("X-RateLimit-Limit", "30")
	newer.Set("X-RateLimit-Remaining", "0")
	newer.Set("X-RateLimit-Reset", fmt.Sprint(reset.Unix()))
	c.observe(testNow, "search", newer)

	late := make(http.Header)
	late.Set("X-RateLimit-Limit", "30")
	late.Set("X-RateLimit-Remaining", "1")
	late.Set("X-RateLimit-Reset", fmt.Sprint(reset.Unix()))
	c.observe(testNow, "search", late)
	if c.searchRemaining != 0 || !c.searchReset.Equal(reset) {
		t.Fatalf("late same-window header restored capacity: remaining=%d reset=%s", c.searchRemaining, c.searchReset)
	}
	if err := c.allow(testNow, "search"); !errors.Is(err, errRequestBudget) {
		t.Fatalf("search admission after late header = %v, want budget pause", err)
	}

	olderWindow := make(http.Header)
	olderWindow.Set("X-RateLimit-Limit", "30")
	olderWindow.Set("X-RateLimit-Remaining", "15")
	olderWindow.Set("X-RateLimit-Reset", fmt.Sprint(testNow.Add(30*time.Second).Unix()))
	c.observe(testNow, "search", olderWindow)
	if c.searchRemaining != 0 || !c.searchReset.Equal(reset) {
		t.Fatalf("older-window header replaced newer state: remaining=%d reset=%s", c.searchRemaining, c.searchReset)
	}
}

func TestWaitingSearchAdmissionHonorsContextCancellation(t *testing.T) {
	var hit atomic.Int32
	firstEntered := make(chan struct{}, 1)
	secondEntered := make(chan struct{}, 1)
	releaseFirst := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hit.Add(1) == 1 {
			firstEntered <- struct{}{}
			<-releaseFirst
			w.Header().Set("X-RateLimit-Limit", "30")
			w.Header().Set("X-RateLimit-Remaining", "1")
			w.Header().Set("X-RateLimit-Reset", fmt.Sprint(testNow.Add(time.Minute).Unix()))
			_, _ = w.Write([]byte(`{}`))
			return
		}
		secondEntered <- struct{}{}
		<-r.Context().Done()
	}))
	defer server.Close()
	c := NewClient(server.URL, nil, "")
	headers := make(http.Header)
	headers.Set("X-RateLimit-Limit", "30")
	headers.Set("X-RateLimit-Remaining", "2")
	headers.Set("X-RateLimit-Reset", fmt.Sprint(testNow.Add(time.Minute).Unix()))
	c.observe(testNow, "search", headers)

	firstResult := make(chan error, 1)
	go func() {
		firstResult <- c.getJSON(context.Background(), testNow, "search", "/search/repositories", nil, &struct{}{})
	}()
	select {
	case <-firstEntered:
	case <-time.After(time.Second):
		t.Fatal("first search request did not reach server")
	}
	ctx, cancel := context.WithCancel(context.Background())
	secondResult := make(chan error, 1)
	go func() {
		secondResult <- c.getJSON(ctx, testNow, "search", "/search/repositories", nil, &struct{}{})
	}()
	secondReachedServer := false
	select {
	case <-secondEntered:
		secondReachedServer = true
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	if err := <-secondResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting search error = %v, want context cancellation", err)
	}
	close(releaseFirst)
	if err := <-firstResult; err != nil {
		t.Fatalf("first search error = %v", err)
	}
	if secondReachedServer || hit.Load() != 1 {
		t.Fatalf("secondReachedServer=%v serverHits=%d", secondReachedServer, hit.Load())
	}
}

func TestRetryTimeUsesRetryAfterThenResetThenFallback(t *testing.T) {
	if got := retryTime(testNow, http.Header{"Retry-After": []string{"25"}}); !got.Equal(testNow.Add(25 * time.Second)) {
		t.Fatalf("seconds Retry-After = %s", got)
	}
	if got := retryTime(testNow, http.Header{"Retry-After": []string{"9223372036854775807"}}); !got.After(testNow) {
		t.Fatalf("large Retry-After moved retry time backward: %s", got)
	}
	date := testNow.Add(90 * time.Second)
	if got := retryTime(testNow, http.Header{"Retry-After": []string{date.Format(http.TimeFormat)}}); !got.Equal(date) {
		t.Fatalf("date Retry-After = %s, want %s", got, date)
	}
	reset := testNow.Add(2 * time.Minute)
	if got := retryTime(testNow, http.Header{"X-Ratelimit-Reset": []string{fmt.Sprint(reset.Unix())}}); !got.Equal(reset) {
		t.Fatalf("reset fallback = %s, want %s", got, reset)
	}
	if got := retryTime(testNow, http.Header{}); !got.Equal(testNow.Add(time.Minute)) {
		t.Fatalf("default retry = %s", got)
	}
}

func TestAllowRejectsUnknownResourceWithoutConsumingBudget(t *testing.T) {
	c := NewClient("https://api.github.com", nil, "")
	if err := c.allow(testNow, "invalid"); err == nil {
		t.Fatal("allow accepted unknown resource")
	}
	if c.coreCount != 0 || c.searchCount != 0 {
		t.Fatalf("unknown resource consumed budget: core=%d search=%d", c.coreCount, c.searchCount)
	}
}

func TestAPIURLPreservesEscapedPathSeparators(t *testing.T) {
	u, err := apiURL("https://api.github.com", "/repos/octo/repo/git/trees/release%2F2026-10", url.Values{"recursive": {"1"}})
	if err != nil {
		t.Fatalf("apiURL: %v", err)
	}
	if got, want := u.EscapedPath(), "/repos/octo/repo/git/trees/release%2F2026-10"; got != want {
		t.Fatalf("escaped path = %q, want %q", got, want)
	}
	if got := u.Query().Get("recursive"); got != "1" {
		t.Fatalf("recursive query = %q", got)
	}
}

func TestAPIURLRejectsEncodedDotSegmentsAndBackslashes(t *testing.T) {
	for _, path := range []string{
		"/repos/octo/repo/contents/%2e/SKILL.md",
		"/repos/octo/repo/contents/%2e%2e/SKILL.md",
		"/repos/octo/repo/contents/%5c/SKILL.md",
	} {
		if _, err := apiURL("https://api.github.com", path, nil); err == nil {
			t.Fatalf("apiURL accepted unsafe path %q", path)
		}
	}
}
