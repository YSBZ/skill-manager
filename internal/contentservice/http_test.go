package contentservice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"skillmanage/internal/content"
)

type readerStub struct {
	got       content.Query
	gotID     string
	feedCalls int
	page      content.Page
	entry     content.EntryResult
	meta      content.Meta
	feedErr   error
	entryErr  error
	statusErr error
}

type calendarReaderStub struct {
	*readerStub
	calendar    content.ResetCalendarSnapshot
	calendarErr error
}

type modelLeaderboardReaderStub struct {
	*readerStub
	category    string
	leaderboard content.ModelLeaderboard
	boardErr    error
}

func (s *modelLeaderboardReaderStub) ModelLeaderboard(_ context.Context, category string) (content.ModelLeaderboard, error) {
	s.category = category
	return s.leaderboard, s.boardErr
}

func (s *calendarReaderStub) ResetCalendar(context.Context) (content.ResetCalendarSnapshot, error) {
	return s.calendar, s.calendarErr
}

func (s *readerStub) Feed(_ context.Context, q content.Query) (content.Page, error) {
	s.feedCalls++
	s.got = q
	return s.page, s.feedErr
}

func (s *readerStub) Entry(_ context.Context, id string) (content.EntryResult, error) {
	s.gotID = id
	return s.entry, s.entryErr
}

func (s *readerStub) Status(context.Context) (content.Meta, error) {
	return s.meta, s.statusErr
}

func TestHandlerDefaultLimitETagAndBounds(t *testing.T) {
	r := &readerStub{page: content.Page{Items: []content.Entry{}, Meta: emptyMeta()}}
	h := NewHandler(r)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/feed", nil))
	if w.Code != http.StatusOK || r.got.Limit != 20 {
		t.Fatalf("default: status=%d limit=%d", w.Code, r.got.Limit)
	}

	for _, target := range []string{
		"/v1/feed?limit=", "/v1/feed?limit=0", "/v1/feed?limit=-1",
		"/v1/feed?limit=abc", "/v1/feed?limit=51", "/v1/feed?limit=20&limit=21",
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s status=%d body=%q, want 400", target, w.Code, w.Body.String())
		}
	}

	first := httptest.NewRecorder()
	h.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/v1/feed", nil))
	bodyHash := sha256.Sum256(first.Body.Bytes())
	wantETag := `"` + hex.EncodeToString(bodyHash[:]) + `"`
	if first.Header().Get("ETag") != wantETag {
		t.Fatalf("ETag=%q, want body hash %q", first.Header().Get("ETag"), wantETag)
	}
	if first.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type=%q", first.Header().Get("Content-Type"))
	}
	if first.Header().Get("Cache-Control") != "private, max-age=0, must-revalidate" {
		t.Fatalf("Cache-Control=%q", first.Header().Get("Cache-Control"))
	}

	secondReq := httptest.NewRequest(http.MethodGet, "/v1/feed", nil)
	secondReq.Header.Set("If-None-Match", wantETag)
	second := httptest.NewRecorder()
	h.ServeHTTP(second, secondReq)
	if second.Code != http.StatusNotModified || second.Body.Len() != 0 || second.Header().Get("ETag") != wantETag {
		t.Fatalf("304 response = status %d, ETag %q, body %q", second.Code, second.Header().Get("ETag"), second.Body.String())
	}
}

func TestResetCalendarHandlerIsOptionalAndRejectsQueryParameters(t *testing.T) {
	base := &readerStub{}
	for _, target := range []string{"/v1/reset-calendar?month=2026-10", "/v1/reset-calendar?x=%zz"} {
		w := httptest.NewRecorder()
		NewHandler(base).ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s status=%d, want 400", target, w.Code)
		}
	}
	missing := httptest.NewRecorder()
	NewHandler(base).ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/v1/reset-calendar", nil))
	if missing.Code != http.StatusServiceUnavailable {
		t.Fatalf("reader without optional calendar status=%d, want 503", missing.Code)
	}
	failed := &calendarReaderStub{readerStub: &readerStub{}, calendarErr: content.ErrUnavailable}
	failedResponse := httptest.NewRecorder()
	NewHandler(failed).ServeHTTP(failedResponse, httptest.NewRequest(http.MethodGet, "/v1/reset-calendar", nil))
	if failedResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("calendar without initial cache status=%d, want 503", failedResponse.Code)
	}
}

func TestResetCalendarHandlerReturnsETagAnd304(t *testing.T) {
	reader := &calendarReaderStub{readerStub: &readerStub{}, calendar: content.ResetCalendarSnapshot{Timezone: "Asia/Shanghai", Events: []content.ResetCalendarEvent{}}}
	h := NewHandler(reader)
	first := httptest.NewRecorder()
	h.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/v1/reset-calendar", nil))
	if first.Code != http.StatusOK || first.Header().Get("ETag") == "" || !strings.Contains(first.Body.String(), `"timezone":"Asia/Shanghai"`) {
		t.Fatalf("calendar response status=%d headers=%v body=%q", first.Code, first.Header(), first.Body.String())
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/reset-calendar", nil)
	request.Header.Set("If-None-Match", first.Header().Get("ETag"))
	second := httptest.NewRecorder()
	h.ServeHTTP(second, request)
	if second.Code != http.StatusNotModified || second.Body.Len() != 0 {
		t.Fatalf("calendar conditional response status=%d body=%q", second.Code, second.Body.String())
	}
}

func TestModelLeaderboardHandlerValidatesCategoryAndUsesOptionalReader(t *testing.T) {
	h := NewHandler(&readerStub{})

	missing := httptest.NewRecorder()
	h.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/v1/model-leaderboard", nil))
	if missing.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing optional model leaderboard reader status=%d body=%q, want 503", missing.Code, missing.Body.String())
	}

	for _, target := range []string{
		"/v1/model-leaderboard?category=overall&category=coding",
		"/v1/model-leaderboard?category=unknown",
		"/v1/model-leaderboard?category=coding&extra=1",
		"/v1/model-leaderboard?category=",
		"/v1/model-leaderboard?category=%zz",
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s status=%d body=%q, want 400", target, w.Code, w.Body.String())
		}
	}

	wrongMethod := httptest.NewRecorder()
	h.ServeHTTP(wrongMethod, httptest.NewRequest(http.MethodPost, "/v1/model-leaderboard", nil))
	if wrongMethod.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST model leaderboard status=%d body=%q, want 405", wrongMethod.Code, wrongMethod.Body.String())
	}
	head := httptest.NewRecorder()
	h.ServeHTTP(head, httptest.NewRequest(http.MethodHead, "/v1/model-leaderboard", nil))
	if head.Code != http.StatusMethodNotAllowed {
		t.Fatalf("HEAD model leaderboard status=%d, want 405", head.Code)
	}
}

func TestModelLeaderboardHandlerReturnsCategorySnapshotAndETag(t *testing.T) {
	reader := &modelLeaderboardReaderStub{
		readerStub: &readerStub{},
		leaderboard: content.ModelLeaderboard{
			Category: "overall", Title: "AIHOT大模型排行榜", SourceURL: "https://aihot.news/leaderboard",
			UpdatedLabel: "39 项评测·7 家机构·10/29 14:06 更新", FetchedAt: "2026-10-29T10:00:00Z",
			Currency: "CNY", PriceUnit: "人民币 / 百万 Token", Entries: []content.ModelLeaderboardEntry{}, PriceNotes: []string{},
		},
	}
	h := NewHandler(reader)
	first := httptest.NewRecorder()
	h.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/v1/model-leaderboard", nil))
	if first.Code != http.StatusOK || reader.category != "overall" || first.Header().Get("ETag") == "" || !strings.Contains(first.Body.String(), `"priceUnit":"人民币 / 百万 Token"`) {
		t.Fatalf("default leaderboard response status=%d category=%q headers=%v body=%q", first.Code, reader.category, first.Header(), first.Body.String())
	}
	conditional := httptest.NewRequest(http.MethodGet, "/v1/model-leaderboard", nil)
	conditional.Header.Set("If-None-Match", first.Header().Get("ETag"))
	second := httptest.NewRecorder()
	h.ServeHTTP(second, conditional)
	if second.Code != http.StatusNotModified || second.Body.Len() != 0 {
		t.Fatalf("conditional response status=%d body=%q", second.Code, second.Body.String())
	}

	coding := httptest.NewRecorder()
	h.ServeHTTP(coding, httptest.NewRequest(http.MethodGet, "/v1/model-leaderboard?category=coding", nil))
	if coding.Code != http.StatusOK || reader.category != "coding" {
		t.Fatalf("coding leaderboard response status=%d category=%q body=%q", coding.Code, reader.category, coding.Body.String())
	}
}

func TestHandlerRejectsMalformedRawQuery(t *testing.T) {
	r := &readerStub{page: content.Page{Items: []content.Entry{}, Meta: emptyMeta()}}
	h := NewHandler(r)
	for _, target := range []string{
		"/v1/feed?limit=%zz",
		"/v1/feed?limit=1&limit=%zz",
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
		if w.Code != http.StatusBadRequest || r.feedCalls != 0 {
			t.Errorf("%s: status=%d feed calls=%d body=%q, want 400 before reader", target, w.Code, r.feedCalls, w.Body.String())
		}
	}
}

func TestHandlerParsesFeedQueryAndRejectsDuplicateParameters(t *testing.T) {
	r := &readerStub{page: content.Page{Items: []content.Entry{}, Meta: emptyMeta()}}
	h := NewHandler(r)
	target := "/v1/feed?kind=project&view=scenarios&window=24h&q=agent&category=tools&scene=review&limit=7&cursor=opaque"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
	want := content.Query{Kind: "project", View: "scenarios", Window: "24h", Q: "agent", Category: "tools", Scene: "review", Limit: 7, Cursor: "opaque"}
	if r.got != want {
		t.Fatalf("query=%+v, want %+v", r.got, want)
	}

	for _, target := range []string{
		"/v1/feed?kind=news&kind=topic",
		"/v1/feed?view=all&view=selected",
		"/v1/feed?window=24h&window=7d",
		"/v1/feed?q=a&q=b",
		"/v1/feed?category=a&category=b",
		"/v1/feed?scene=a&scene=b",
		"/v1/feed?cursor=a&cursor=b",
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s status=%d body=%q, want 400", target, w.Code, w.Body.String())
		}
	}
}

func TestHandlerRoutesAndEntryIDValidation(t *testing.T) {
	r := &readerStub{
		entry: content.EntryResult{Entry: content.Entry{ID: "news_123", Paragraphs: []content.Paragraph{}}, Meta: emptyMeta()},
		meta:  emptyMeta(),
		page:  content.Page{Items: []content.Entry{}, Meta: emptyMeta()},
	}
	h := NewHandler(r)

	validEntry := httptest.NewRecorder()
	h.ServeHTTP(validEntry, httptest.NewRequest(http.MethodGet, "/v1/entries/news_123", nil))
	if validEntry.Code != http.StatusOK || r.gotID != "news_123" {
		t.Fatalf("entry response status=%d id=%q body=%q", validEntry.Code, r.gotID, validEntry.Body.String())
	}

	for _, id := range []string{"bad.id", "../secret", strings.Repeat("a", 161)} {
		r.gotID = ""
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/entries/"+url.PathEscape(id), nil))
		if w.Code != http.StatusBadRequest || r.gotID != "" {
			t.Errorf("id %q: status=%d reader id=%q body=%q, want rejected before reader", id, w.Code, r.gotID, w.Body.String())
		}
	}

	status := httptest.NewRecorder()
	h.ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/v1/status", nil))
	if status.Code != http.StatusOK {
		t.Fatalf("status route code=%d body=%q", status.Code, status.Body.String())
	}

	health := httptest.NewRecorder()
	h.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK || health.Body.String() != `{"status":"ok"}` {
		t.Fatalf("health response=%d %q", health.Code, health.Body.String())
	}

	wrongMethod := httptest.NewRecorder()
	h.ServeHTTP(wrongMethod, httptest.NewRequest(http.MethodPost, "/v1/feed", nil))
	if wrongMethod.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /v1/feed status=%d body=%q, want 405", wrongMethod.Code, wrongMethod.Body.String())
	}
}

func TestHandlerMapsErrorsToSanitizedStableResponses(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		status  int
		code    string
		message string
	}{
		{name: "bad query", err: content.ErrBadQuery, status: http.StatusBadRequest, code: "bad_query", message: "查询参数无效"},
		{name: "expired cursor", err: content.ErrCursorExpired, status: http.StatusConflict, code: "cursor_expired", message: "分页游标已失效"},
		{name: "withdrawn", err: content.ErrWithdrawn, status: http.StatusGone, code: "withdrawn", message: "内容已撤回"},
		{name: "not found", err: content.ErrNotFound, status: http.StatusNotFound, code: "not_found", message: "内容不存在"},
		{name: "unavailable", err: content.ErrUnavailable, status: http.StatusServiceUnavailable, code: "unavailable", message: "内容来源暂时不可用"},
		{name: "internal", err: errors.New("secret token /private/db/path stack trace"), status: http.StatusInternalServerError, code: "internal_error", message: "内容服务暂时不可用"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &readerStub{feedErr: fmt.Errorf("upstream detail: %w", tc.err)}
			w := httptest.NewRecorder()
			NewHandler(r).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/feed", nil))
			if w.Code != tc.status {
				t.Fatalf("status=%d, want %d body=%q", w.Code, tc.status, w.Body.String())
			}
			if w.Header().Get("Content-Type") != "application/json; charset=utf-8" {
				t.Fatalf("Content-Type=%q", w.Header().Get("Content-Type"))
			}
			var got content.APIError
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode API error: %v; body=%q", err, w.Body.String())
			}
			if got.ErrorCode != tc.code || got.Error != tc.message {
				t.Fatalf("API error=%+v, want code=%q message=%q", got, tc.code, tc.message)
			}
			if strings.Contains(w.Body.String(), "secret") || strings.Contains(w.Body.String(), "/private") || strings.Contains(w.Body.String(), "stack trace") {
				t.Fatalf("response leaked internal error: %q", w.Body.String())
			}
		})
	}
}

func emptyMeta() content.Meta {
	return content.Meta{Sources: []content.SourceStatus{}, Notices: []map[string]any{}}
}
