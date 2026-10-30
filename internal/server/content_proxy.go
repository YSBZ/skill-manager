package server

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"skillmanage/internal/content"
	"skillmanage/internal/contentservice"
)

const (
	contentServiceURLEnv   = "SKILLMANAGER_CONTENT_URL"
	contentRequestTimeout  = 8 * time.Second
	maxContentResponseBody = 8 << 20
)

// ContentServiceURL is the build-time default for the shared content service.
// SKILLMANAGER_CONTENT_URL overrides it at daemon startup.
var ContentServiceURL string

func configuredContentServiceURL() (*url.URL, error) {
	raw := configuredContentServiceURLValue()
	if raw == "" {
		return nil, errors.New("content service URL is not configured")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("content service URL is invalid")
	}
	scheme := strings.ToLower(u.Scheme)
	if (scheme != "http" && scheme != "https") || u.Host == "" || u.Hostname() == "" || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") {
		return nil, errors.New("content service URL is invalid")
	}
	u.Scheme = scheme
	return u, nil
}

func configuredContentServiceURLValue() string {
	raw, configured := os.LookupEnv(contentServiceURLEnv)
	if !configured {
		raw = ContentServiceURL
	}
	return raw
}

func newContentHTTPClient() *http.Client {
	return &http.Client{
		Timeout: contentRequestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func (s *Server) handleContentFeed(w http.ResponseWriter, r *http.Request) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_query", "Invalid content query")
		return
	}
	query, err := validateContentFeedQuery(values)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_query", "Invalid content query")
		return
	}
	s.proxyContent(w, r, "/v1/feed", query)
}

func (s *Server) handleContentEntry(w http.ResponseWriter, r *http.Request) {
	if err := validateEmptyContentQuery(r.URL.RawQuery); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_query", "Invalid content query")
		return
	}
	id := r.PathValue("id")
	if err := content.ValidateID(id); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_id", "Invalid content entry ID")
		return
	}
	s.proxyContent(w, r, "/v1/entries/"+id, nil)
}

func (s *Server) handleContentStatus(w http.ResponseWriter, r *http.Request) {
	if err := validateEmptyContentQuery(r.URL.RawQuery); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_query", "Invalid content query")
		return
	}
	s.proxyContent(w, r, "/v1/status", nil)
}

func (s *Server) handleContentResetCalendar(w http.ResponseWriter, r *http.Request) {
	if err := validateEmptyContentQuery(r.URL.RawQuery); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_query", "Invalid content query")
		return
	}
	s.proxyContent(w, r, "/v1/reset-calendar", nil)
}

func (s *Server) handleContentModelLeaderboard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
		return
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_query", "查询参数无效")
		return
	}
	query, err := validateContentModelLeaderboardQuery(values)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_query", "查询参数无效")
		return
	}
	s.proxyContent(w, r, "/v1/model-leaderboard", query)
}

func validateContentModelLeaderboardQuery(src url.Values) (url.Values, error) {
	for key, values := range src {
		if key != "category" || len(values) != 1 || values[0] == "" {
			return nil, fmt.Errorf("invalid model leaderboard query parameter: %s", key)
		}
	}
	category := content.ModelLeaderboardOverall
	if _, present := src["category"]; present {
		var err error
		category, err = content.NormalizeModelLeaderboardCategory(src.Get("category"))
		if err != nil {
			return nil, err
		}
	}
	return url.Values{"category": {category}}, nil
}

func validateEmptyContentQuery(raw string) error {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return err
	}
	if len(values) != 0 {
		return errors.New("unexpected content query")
	}
	return nil
}

func (s *Server) proxyContent(w http.ResponseWriter, r *http.Request, endpoint string, query url.Values) {
	s.contentMu.Lock()
	localRuntime := s.localContent
	contentBaseURL := s.contentBaseURL
	contentHTTPClient := s.contentHTTPClient
	s.contentMu.Unlock()
	if localRuntime != nil {
		s.forwardLocalContent(localRuntime, w, r, endpoint, query)
		return
	}
	if contentBaseURL == nil || contentHTTPClient == nil {
		writeErr(w, http.StatusServiceUnavailable, "content_unavailable", "Content service is not configured")
		return
	}
	target := *contentBaseURL
	target.Path = strings.TrimRight(target.Path, "/") + endpoint
	target.RawPath = ""
	target.RawQuery = query.Encode()
	target.ForceQuery = false
	upstreamRequest, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target.String(), nil)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "content_upstream_error", "Content service request failed")
		return
	}
	upstreamRequest.Header.Set("Accept", "application/json")
	for _, value := range r.Header.Values("If-None-Match") {
		upstreamRequest.Header.Add("If-None-Match", value)
	}

	response, err := contentHTTPClient.Do(upstreamRequest)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "content_upstream_error", "Content service request failed")
		return
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusNotModified {
		writeContentResponse(w, response.StatusCode, response.Header, nil, nil)
		return
	}
	var body []byte
	var bodyErr error
	if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices &&
		isJSONContentType(response.Header.Get("Content-Type")) {
		body, bodyErr = readContentResponseBody(response.Body)
	}
	writeContentResponse(w, response.StatusCode, response.Header, body, bodyErr)
}

func (s *Server) forwardLocalContent(runtime *contentservice.LocalRuntime, w http.ResponseWriter, r *http.Request, endpoint string, query url.Values) {
	internalURL := &url.URL{Path: endpoint}
	if query != nil {
		internalURL.RawQuery = query.Encode()
	}
	internalRequest := r.Clone(r.Context())
	internalRequest.Method = http.MethodGet
	internalRequest.URL = internalURL
	internalRequest.RequestURI = internalURL.RequestURI()
	internalRequest.Host = ""
	internalRequest.RemoteAddr = ""
	internalRequest.Body = nil
	internalRequest.GetBody = nil
	internalRequest.ContentLength = 0
	internalRequest.TransferEncoding = nil
	internalRequest.Header = make(http.Header)
	acceptValues := r.Header.Values("Accept")
	if len(acceptValues) == 0 {
		internalRequest.Header.Set("Accept", "application/json")
	} else {
		for _, value := range acceptValues {
			internalRequest.Header.Add("Accept", value)
		}
	}
	for _, value := range r.Header.Values("If-None-Match") {
		internalRequest.Header.Add("If-None-Match", value)
	}
	capture := newLocalContentResponseWriter()
	runtime.ServeHTTP(capture, internalRequest)
	status, headers, body, bodyErr := capture.result()
	writeContentResponse(w, status, headers, body, bodyErr)
}

func readContentResponseBody(body io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxContentResponseBody+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxContentResponseBody {
		return nil, errors.New("content response exceeds size limit")
	}
	return data, nil
}

func writeContentResponse(w http.ResponseWriter, status int, headers http.Header, body []byte, bodyErr error) {
	if status == http.StatusNotModified {
		copyContentResponseHeaders(w.Header(), headers)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		writeContentUpstreamError(w, status)
		return
	}
	if !isJSONContentType(headers.Get("Content-Type")) {
		writeErr(w, http.StatusBadGateway, "content_upstream_error", "Content service returned an invalid response")
		return
	}
	if bodyErr != nil || len(body) > maxContentResponseBody {
		writeErr(w, http.StatusBadGateway, "content_upstream_error", "Content service returned an invalid response")
		return
	}
	copyContentResponseHeaders(w.Header(), headers)
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

type localContentResponseWriter struct {
	header         http.Header
	responseHeader http.Header
	status         int
	body           bytes.Buffer
	tooLarge       bool
}

func newLocalContentResponseWriter() *localContentResponseWriter {
	return &localContentResponseWriter{header: make(http.Header)}
}

func (w *localContentResponseWriter) Header() http.Header { return w.header }

func (w *localContentResponseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.responseHeader = w.header.Clone()
}

func (w *localContentResponseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if w.status >= http.StatusOK && w.status < http.StatusMultipleChoices &&
		isJSONContentType(w.responseHeader.Get("Content-Type")) {
		remaining := maxContentResponseBody + 1 - w.body.Len()
		if remaining > 0 {
			count := min(len(data), remaining)
			_, _ = w.body.Write(data[:count])
		}
		if len(data) > remaining {
			w.tooLarge = true
		}
	}
	return len(data), nil
}

func (w *localContentResponseWriter) result() (int, http.Header, []byte, error) {
	if w.status == 0 {
		w.status = http.StatusOK
		w.responseHeader = w.header.Clone()
	}
	if w.tooLarge {
		return w.status, w.responseHeader, w.body.Bytes(), errors.New("local content response exceeds size limit")
	}
	return w.status, w.responseHeader, w.body.Bytes(), nil
}

func writeContentUpstreamError(w http.ResponseWriter, status int) {
	code, message := "content_upstream_error", "Content service request failed"
	switch status {
	case http.StatusBadRequest:
		code, message = "bad_query", "查询参数无效"
	case http.StatusNotFound:
		code, message = "not_found", "内容不存在"
	case http.StatusConflict:
		code, message = "cursor_expired", "分页游标已失效"
	case http.StatusGone:
		code, message = "withdrawn", "内容已撤回"
	case http.StatusServiceUnavailable:
		code, message = "unavailable", "内容来源暂时不可用"
	default:
		status = http.StatusBadGateway
	}
	writeErr(w, status, code, message)
}

func copyContentResponseHeaders(dst, src http.Header) {
	if contentType := src.Get("Content-Type"); isJSONContentType(contentType) {
		dst.Set("Content-Type", contentType)
	}
	for _, name := range []string{"Cache-Control", "ETag"} {
		if value := src.Get(name); value != "" {
			dst.Set(name, value)
		}
	}
}

func isJSONContentType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return false
	}
	mediaType = strings.ToLower(mediaType)
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}

func validateContentFeedQuery(src url.Values) (url.Values, error) {
	allowed := map[string]bool{
		"kind": true, "view": true, "window": true, "q": true,
		"category": true, "scene": true, "limit": true, "cursor": true,
	}
	for key, values := range src {
		if !allowed[key] || len(values) != 1 {
			return nil, fmt.Errorf("invalid query parameter: %s", key)
		}
	}

	out := make(url.Values)
	kind := src.Get("kind")
	if kind == "" {
		kind = "news"
	}
	if !slices.Contains([]string{"news", "topic", "daily", "weekly", "monthly", "project"}, kind) {
		return nil, errors.New("invalid kind")
	}
	out.Set("kind", kind)

	view := strings.TrimSpace(src.Get("view"))
	if kind == "news" {
		if view == "" {
			view = "selected"
		}
		if !slices.Contains([]string{"selected", "all"}, view) {
			return nil, errors.New("invalid news view")
		}
		out.Set("view", view)
	}
	if kind == "project" {
		if view == "" {
			view = "weekly"
		}
		if !slices.Contains([]string{"weekly", "new", "scenarios"}, view) {
			return nil, errors.New("invalid project view")
		}
		out.Set("view", view)
	}
	if kind != "news" && kind != "project" && view != "" {
		return nil, errors.New("view not supported")
	}

	if kind == "news" {
		window := src.Get("window")
		if window == "" {
			window = "7d"
		}
		if !slices.Contains([]string{"24h", "7d"}, window) {
			return nil, errors.New("invalid window")
		}
		out.Set("window", window)
	} else if _, present := src["window"]; present {
		return nil, errors.New("window only supported for news")
	}

	q := strings.TrimSpace(src.Get("q"))
	if utf8.RuneCountInString(q) > 200 {
		return nil, errors.New("q too long")
	}
	if q != "" {
		out.Set("q", q)
	}
	for _, key := range []string{"category", "scene"} {
		if value := strings.TrimSpace(src.Get(key)); value != "" {
			out.Set(key, value)
		}
	}
	if values, present := src["cursor"]; present {
		out.Set("cursor", values[0])
	}

	limit := 20
	if values, present := src["limit"]; present {
		parsed, err := strconv.Atoi(values[0])
		if err != nil || parsed < 1 || parsed > 50 {
			return nil, errors.New("limit must be 1..50")
		}
		limit = parsed
	}
	out.Set("limit", strconv.Itoa(limit))
	return out, nil
}
