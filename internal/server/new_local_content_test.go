package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"skillmanage/internal/content"
	"skillmanage/internal/content/sqlite"
	"skillmanage/internal/contentservice"
)

func TestLocalContentForwardingKeepsAuthAtOuterHandler(t *testing.T) {
	t.Setenv(contentServiceURLEnv, "")
	server := newTestServer(t)
	calls := 0
	var request *http.Request
	server.localContent = contentservice.NewLocalRuntime(context.Background(), localContentTestCloser{}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		request = r
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `"local-v1"`)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{}`)
	}))
	unauthenticated := httptest.NewRecorder()
	server.Handler().ServeHTTP(unauthenticated, req(http.MethodGet, "/api/content/feed", "", nil))
	if unauthenticated.Code != http.StatusUnauthorized || calls != 0 {
		t.Fatalf("unauthenticated local feed status=%d internal calls=%d, want 401 and no internal call", unauthenticated.Code, calls)
	}

	badQuery := httptest.NewRecorder()
	server.Handler().ServeHTTP(badQuery, req(http.MethodGet, "/api/content/feed?unknown=value", server.token, nil))
	if badQuery.Code != http.StatusBadRequest || calls != 0 {
		t.Fatalf("invalid query status=%d internal calls=%d, want 400 and no internal call", badQuery.Code, calls)
	}

	feedReq := req(http.MethodGet, "/api/content/feed?kind=project", server.token, nil)
	feedReq.Header.Set("Accept", "application/vnd.skillmanage.test+json")
	feedReq.Header.Set("If-None-Match", `"prior-v1"`)
	feedReq.Header.Set("Cookie", "private=local")
	feedReq.Header.Set("X-Local-Only", "must-not-pass")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, feedReq)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"local-v1"` || response.Body.String() != `{}` {
		t.Fatalf("local response status=%d headers=%v body=%q", response.Code, response.Header(), response.Body.String())
	}
	if calls != 1 || request == nil || request.Method != http.MethodGet || request.URL.Path != "/v1/feed" {
		t.Fatalf("internal request=%v calls=%d, want one GET /v1/feed", request, calls)
	}
	if got := request.URL.Query(); got.Get("kind") != "project" || got.Get("view") != "weekly" || got.Get("limit") != "20" {
		t.Fatalf("forwarded query=%v, want validated defaults", got)
	}
	if request.Header.Get("Accept") != "application/vnd.skillmanage.test+json" || request.Header.Get("If-None-Match") != `"prior-v1"` {
		t.Fatalf("allowed internal headers=%v", request.Header)
	}
	for _, forbidden := range []string{"Authorization", "Cookie", "X-Local-Only"} {
		if got := request.Header.Get(forbidden); got != "" {
			t.Errorf("internal %s header=%q, want omitted", forbidden, got)
		}
	}
}

func TestLocalContentUnexpectedHandlerErrorMatchesRemoteSanitization(t *testing.T) {
	server := newTestServer(t)
	server.localContent = contentservice.NewLocalRuntime(
		context.Background(),
		localContentTestCloser{},
		contentservice.NewHandler(failingLocalContentReader{err: errors.New("secret sqlite path and stack trace")}),
	)

	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, req(http.MethodGet, "/api/content/feed", server.token, nil))
	if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), `"error_code":"content_upstream_error"`) {
		t.Fatalf("unexpected local handler error status=%d body=%q, want sanitized 502/content_upstream_error", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "internal_error") || strings.Contains(response.Body.String(), "secret sqlite") || strings.Contains(response.Body.String(), "stack trace") {
		t.Fatalf("local handler error leaked internal details: %q", response.Body.String())
	}
}

func TestLocalContentHTTPErrorStatusesMatchRemoteMapping(t *testing.T) {
	statusCode := http.StatusInternalServerError
	server := newTestServer(t)
	server.localContent = contentservice.NewLocalRuntime(context.Background(), localContentTestCloser{}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		_, _ = io.WriteString(w, "secret upstream details")
	}))

	for _, tc := range []struct {
		upstreamStatus int
		wantStatus     int
		wantCode       string
	}{
		{upstreamStatus: http.StatusBadRequest, wantStatus: http.StatusBadRequest, wantCode: "bad_query"},
		{upstreamStatus: http.StatusNotFound, wantStatus: http.StatusNotFound, wantCode: "not_found"},
		{upstreamStatus: http.StatusConflict, wantStatus: http.StatusConflict, wantCode: "cursor_expired"},
		{upstreamStatus: http.StatusGone, wantStatus: http.StatusGone, wantCode: "withdrawn"},
		{upstreamStatus: http.StatusServiceUnavailable, wantStatus: http.StatusServiceUnavailable, wantCode: "unavailable"},
		{upstreamStatus: http.StatusInternalServerError, wantStatus: http.StatusBadGateway, wantCode: "content_upstream_error"},
		{upstreamStatus: http.StatusFound, wantStatus: http.StatusBadGateway, wantCode: "content_upstream_error"},
	} {
		statusCode = tc.upstreamStatus
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, req(http.MethodGet, "/api/content/status", server.token, nil))
		if response.Code != tc.wantStatus || !strings.Contains(response.Body.String(), `"error_code":"`+tc.wantCode+`"`) {
			t.Errorf("local status %d mapped to %d body=%q, want %d/%s", tc.upstreamStatus, response.Code, response.Body.String(), tc.wantStatus, tc.wantCode)
		}
		if strings.Contains(response.Body.String(), "secret upstream") {
			t.Errorf("local status %d leaked the internal response body: %q", tc.upstreamStatus, response.Body.String())
		}
	}
}

func TestLocalContentResponsePreserves304AndSafeHeaders(t *testing.T) {
	server := newTestServer(t)
	server.localContent = contentservice.NewLocalRuntime(context.Background(), localContentTestCloser{}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
		w.Header().Set("ETag", `"local-v1"`)
		w.Header().Set("Set-Cookie", "private=must-not-pass")
		w.Header().Set("X-Internal-Debug", "must-not-pass")
		w.WriteHeader(http.StatusNotModified)
		_, _ = io.WriteString(w, "must-not-pass")
	}))

	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, req(http.MethodGet, "/api/content/status", server.token, nil))
	if response.Code != http.StatusNotModified || response.Body.Len() != 0 {
		t.Fatalf("local conditional response status=%d body=%q, want empty 304", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Type") != "application/json; charset=utf-8" ||
		response.Header().Get("Cache-Control") == "" || response.Header().Get("ETag") != `"local-v1"` {
		t.Fatalf("safe local response headers were not preserved: %v", response.Header())
	}
	if response.Header().Get("Set-Cookie") != "" || response.Header().Get("X-Internal-Debug") != "" {
		t.Fatalf("unsafe local response headers leaked: %v", response.Header())
	}
}

func TestLocalContentRejectsInvalidAndOversizedSuccessResponses(t *testing.T) {
	oversized := false
	server := newTestServer(t)
	server.localContent = contentservice.NewLocalRuntime(context.Background(), localContentTestCloser{}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if oversized {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":"`+strings.Repeat("x", maxContentResponseBody)+`"}`)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "not JSON")
	}))

	invalid := httptest.NewRecorder()
	server.Handler().ServeHTTP(invalid, req(http.MethodGet, "/api/content/status", server.token, nil))
	if invalid.Code != http.StatusBadGateway || !strings.Contains(invalid.Body.String(), `"error_code":"content_upstream_error"`) {
		t.Fatalf("invalid content type status=%d body=%q, want sanitized 502", invalid.Code, invalid.Body.String())
	}
	oversized = true
	large := httptest.NewRecorder()
	server.Handler().ServeHTTP(large, req(http.MethodGet, "/api/content/status", server.token, nil))
	if large.Code != http.StatusBadGateway || strings.Contains(large.Body.String(), "xxxx") {
		t.Fatalf("oversized local response status=%d body prefix=%q, want bounded 502", large.Code, large.Body.String()[:min(80, large.Body.Len())])
	}
}

func TestStartLocalContentUsesCentralCacheAndClosesWithoutListener(t *testing.T) {
	previousBuildURL := ContentServiceURL
	ContentServiceURL = ""
	t.Cleanup(func() { ContentServiceURL = previousBuildURL })
	t.Setenv(contentServiceURLEnv, "")
	listenerWasAlreadyPresent := loopbackListenerPresent(t, "127.0.0.1:18088")

	upstreamStarted := make(chan string, 1)
	upstreamCanceled := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case upstreamStarted <- r.URL.Path:
		default:
		}
		<-r.Context().Done()
		select {
		case <-upstreamCanceled:
		default:
			close(upstreamCanceled)
		}
	}))
	defer upstream.Close()

	server := newTestServer(t)
	if _, err := os.Stat(filepath.Join(server.centralDir, "content", "content.db")); !os.IsNotExist(err) {
		t.Fatalf("server.New created the content cache before explicit startup: stat error=%v", err)
	}
	server.localContentUpstreams.aihotURL = upstream.URL
	server.localContentUpstreams.githubURL = upstream.URL
	dbPath := filepath.Join(server.centralDir, "content", "content.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatalf("create content cache directory: %v", err)
	}
	seed, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("open seed database: %v", err)
	}
	entry := content.Entry{
		ID:           "cached_news",
		Kind:         "news",
		Title:        "Cached story",
		Summary:      "Available offline",
		Source:       content.Source{Name: "Fixture"},
		CanonicalURL: "https://example.test/cached-news",
		Selected:     true,
		Paragraphs:   []content.Paragraph{},
	}
	if err := seed.ReplaceCollection(context.Background(), "aihot:news:selected:7d", []content.Entry{entry}); err != nil {
		t.Fatalf("seed cached feed: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed database: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server.SetBaseContext(ctx)
	if err := server.StartLocalContent(ctx); err != nil {
		t.Fatalf("StartLocalContent(): %v", err)
	}
	select {
	case path := <-upstreamStarted:
		if path != "/api/v1/items" {
			t.Fatalf("first refresh path=%q, want /api/v1/items", path)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("asynchronous initial refresh did not reach the fake upstream")
	}

	if !listenerWasAlreadyPresent && loopbackListenerPresent(t, "127.0.0.1:18088") {
		t.Fatal("local content startup opened the standalone contentd port 18088")
	}

	request := req(http.MethodGet, "/api/content/feed?kind=news", server.token, nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Cached story") {
		t.Fatalf("cached feed status=%d body=%q", response.Code, response.Body.String())
	}
	var page content.Page
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode cached feed: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "cached_news" {
		t.Fatalf("cached feed items=%+v, want seeded entry", page.Items)
	}
	conditional := req(http.MethodGet, "/api/content/feed?kind=news", server.token, nil)
	conditional.Header.Set("If-None-Match", response.Header().Get("ETag"))
	notModified := httptest.NewRecorder()
	server.Handler().ServeHTTP(notModified, conditional)
	if notModified.Code != http.StatusNotModified || notModified.Body.Len() != 0 {
		t.Fatalf("conditional cached feed status=%d body=%q, want 304", notModified.Code, notModified.Body.String())
	}

	missing := httptest.NewRecorder()
	server.Handler().ServeHTTP(missing, req(http.MethodGet, "/api/content/entries/missing_entry", server.token, nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing cached entry status=%d body=%q, want 404", missing.Code, missing.Body.String())
	}

	cancel()
	select {
	case <-upstreamCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("context cancellation did not stop the in-flight source request")
	}
	if err := server.Close(); err != nil {
		t.Fatalf("Server.Close(): %v", err)
	}
	reopened, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen content database after close: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close reopened content database: %v", err)
	}
}

func loopbackListenerPresent(t *testing.T, address string) bool {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func TestStartLocalContentPreservesExplicitRemoteAndRejectsInvalidURL(t *testing.T) {
	remoteCalls := 0
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remoteCalls++
		if r.URL.Path != "/v1/status" {
			t.Errorf("remote path=%q, want /v1/status", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}))
	defer remote.Close()
	t.Setenv(contentServiceURLEnv, remote.URL)
	server := newTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := server.StartLocalContent(ctx); err != nil {
		t.Fatalf("StartLocalContent(remote): %v", err)
	}
	if server.localContent != nil {
		t.Fatal("explicit remote mode created an in-process local runtime")
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, req(http.MethodGet, "/api/content/status", server.token, nil))
	if response.Code != http.StatusOK || remoteCalls != 1 {
		t.Fatalf("remote response status=%d calls=%d body=%q", response.Code, remoteCalls, response.Body.String())
	}

	t.Setenv(contentServiceURLEnv, "ftp://invalid.example")
	invalidServer := newTestServer(t)
	if err := invalidServer.StartLocalContent(ctx); err == nil {
		t.Fatal("StartLocalContent() accepted an invalid explicit remote URL")
	}
	if invalidServer.localContent != nil {
		t.Fatal("invalid explicit remote URL silently started a local runtime")
	}
	invalidResponse := httptest.NewRecorder()
	invalidServer.Handler().ServeHTTP(invalidResponse, req(http.MethodGet, "/api/content/status", invalidServer.token, nil))
	if invalidResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("invalid remote content status=%d, want 503", invalidResponse.Code)
	}
}

type localContentTestCloser struct{}

func (localContentTestCloser) Close() error { return nil }

type failingLocalContentReader struct{ err error }

func (r failingLocalContentReader) Feed(context.Context, content.Query) (content.Page, error) {
	return content.Page{}, r.err
}

func (r failingLocalContentReader) Entry(context.Context, string) (content.EntryResult, error) {
	return content.EntryResult{}, r.err
}

func (r failingLocalContentReader) Status(context.Context) (content.Meta, error) {
	return content.Meta{}, r.err
}
