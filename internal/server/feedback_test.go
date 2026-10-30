package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
	"time"
)

func TestFeedbackVersionUnique(t *testing.T) {
	now := time.Date(2026, 7, 30, 15, 30, 0, 0, time.UTC)
	a, b := feedbackVersion(now), feedbackVersion(now)
	if !strings.HasPrefix(a, "20260730-153000-") {
		t.Fatalf("version = %q, want prefix 20260730-153000-", a)
	}
	if a == b {
		t.Fatalf("two versions equal (%q) — random suffix not applied", a)
	}
}

func TestBuildFeedbackZip(t *testing.T) {
	meta := feedbackMeta{version: "6.0.13", os: "darwin/arm64", at: time.Date(2026, 7, 30, 15, 30, 0, 0, time.UTC)}
	z, err := buildFeedbackZip("界面太挤", []feedbackImage{{ext: ".png", data: []byte("PNGDATA")}}, meta)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(z), int64(len(z)))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		got[f.Name] = string(b)
	}
	if !strings.Contains(got["feedback.md"], "界面太挤") || !strings.Contains(got["feedback.md"], "6.0.13") {
		t.Fatalf("feedback.md missing text/version: %q", got["feedback.md"])
	}
	if got["screenshot-1.png"] != "PNGDATA" {
		t.Fatalf("screenshot-1.png = %q, want PNGDATA", got["screenshot-1.png"])
	}
}

func statusFeedback(t *testing.T, s *Server) bool {
	t.Helper()
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req("GET", "/api/status", s.token, nil))
	if w.Code != 200 {
		t.Fatalf("status HTTP %d", w.Code)
	}
	var body struct {
		Feedback bool `json:"feedback"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Feedback
}

// setFeedbackSink 设置反馈出口的 package 全局，返回还原函数（表驱动测试用）。
func setFeedbackSink(endpoint, token string) func() {
	oe, ot := FeedbackEndpoint, FeedbackToken
	FeedbackEndpoint, FeedbackToken = endpoint, token
	return func() { FeedbackEndpoint, FeedbackToken = oe, ot }
}

func TestStatusFeedbackFlag(t *testing.T) {
	s := newTestServer(t)
	restore := setFeedbackSink("", "")
	defer restore()
	if statusFeedback(t, s) {
		t.Fatal("feedback 应为 false（未配置出口）")
	}
	restore2 := setFeedbackSink("http://x/packages/generic/feedback", "tok")
	defer restore2()
	if !statusFeedback(t, s) {
		t.Fatal("feedback 应为 true（已配置出口）")
	}
}

// multipartFeedback 构造 /api/feedback 的 multipart body。filename=="" 表示不带图。
func multipartFeedback(t *testing.T, text, filename, ct string, data []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("text", text)
	if filename != "" {
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="images"; filename=%q`, filename))
		h.Set("Content-Type", ct)
		fw, _ := mw.CreatePart(h)
		_, _ = fw.Write(data)
	}
	_ = mw.Close()
	return &buf, mw.FormDataContentType()
}

func postFeedback(t *testing.T, s *Server, body *bytes.Buffer, ct string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/api/feedback", body)
	r.Host = "localhost:7799"
	r.Header.Set("Authorization", "Bearer "+s.token)
	r.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func TestHandleFeedbackUploads(t *testing.T) {
	s := newTestServer(t)
	var gotPath, gotToken string
	var gotZip []byte
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotToken = r.Header.Get("DEPLOY-TOKEN")
		gotZip, _ = io.ReadAll(r.Body)
		w.WriteHeader(201)
	}))
	defer fake.Close()
	defer setFeedbackSink(fake.URL+"/packages/generic/feedback", "deploytok")()

	body, ct := multipartFeedback(t, "太挤了", "shot.png", "image/png", []byte("PNG"))
	w := postFeedback(t, s, body, ct)

	if w.Code != 200 {
		t.Fatalf("status = %d body=%s, want 200", w.Code, w.Body.String())
	}
	if !strings.HasSuffix(gotPath, "/feedback.zip") || !strings.Contains(gotPath, "/packages/generic/feedback/") {
		t.Fatalf("upload path = %q", gotPath)
	}
	if gotToken != "deploytok" {
		t.Fatalf("DEPLOY-TOKEN = %q, want deploytok", gotToken)
	}
	zr, err := zip.NewReader(bytes.NewReader(gotZip), int64(len(gotZip)))
	if err != nil || len(zr.File) != 2 {
		t.Fatalf("uploaded zip invalid or wrong file count: err=%v files=%d", err, len(zr.File))
	}
}

func TestHandleFeedbackDisabled(t *testing.T) {
	s := newTestServer(t)
	defer setFeedbackSink("", "")()
	body, ct := multipartFeedback(t, "hi", "", "", nil)
	w := postFeedback(t, s, body, ct)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", w.Code)
	}
}

func TestHandleFeedbackValidation(t *testing.T) {
	s := newTestServer(t)
	defer setFeedbackSink("http://x/packages/generic/feedback", "tok")()
	// 空文本 → 400
	b1, ct1 := multipartFeedback(t, "   ", "", "", nil)
	if w := postFeedback(t, s, b1, ct1); w.Code != http.StatusBadRequest {
		t.Fatalf("empty text status = %d, want 400", w.Code)
	}
}

type fbImg struct {
	name, ct string
	data     []byte
}

// multipartImages builds a /api/feedback body with N image parts.
func multipartImages(t *testing.T, text string, images []fbImg) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("text", text)
	for _, im := range images {
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="images"; filename=%q`, im.name))
		h.Set("Content-Type", im.ct)
		fw, _ := mw.CreatePart(h)
		_, _ = fw.Write(im.data)
	}
	_ = mw.Close()
	return &buf, mw.FormDataContentType()
}

// TestHandleFeedbackUploadFailure verifies a non-2xx from configured service becomes a 502
// with the generic message — the "never silently drop" guarantee.
func TestHandleFeedbackUploadFailure(t *testing.T) {
	s := newTestServer(t)
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer fake.Close()
	defer setFeedbackSink(fake.URL+"/packages/generic/feedback", "tok")()
	body, ct := multipartFeedback(t, "崩了", "shot.png", "image/png", []byte("PNG"))
	w := postFeedback(t, s, body, ct)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", w.Code)
	}
	if !strings.Contains(w.Body.String(), "提交失败") {
		t.Fatalf("body = %q, want generic 提交失败 message", w.Body.String())
	}
}

// TestHandleFeedbackTooManyImages verifies >5 images is rejected with 400.
func TestHandleFeedbackTooManyImages(t *testing.T) {
	s := newTestServer(t)
	defer setFeedbackSink("http://x/packages/generic/feedback", "tok")()
	imgs := make([]fbImg, 6)
	for i := range imgs {
		imgs[i] = fbImg{name: "s.png", ct: "image/png", data: []byte("P")}
	}
	body, ct := multipartImages(t, "太多图", imgs)
	if w := postFeedback(t, s, body, ct); w.Code != http.StatusBadRequest {
		t.Fatalf("6 images status = %d, want 400", w.Code)
	}
}

// TestHandleFeedbackOversizedImage verifies a single image over the per-image cap is 400.
func TestHandleFeedbackOversizedImage(t *testing.T) {
	s := newTestServer(t)
	defer setFeedbackSink("http://x/packages/generic/feedback", "tok")()
	big := bytes.Repeat([]byte("x"), feedbackMaxImage+1)
	body, ct := multipartImages(t, "太大", []fbImg{{name: "big.png", ct: "image/png", data: big}})
	if w := postFeedback(t, s, body, ct); w.Code != http.StatusBadRequest {
		t.Fatalf("oversized image status = %d, want 400", w.Code)
	}
}
