package server

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"runtime"
	"strings"
	"time"
)

// FeedbackEndpoint / FeedbackToken 是公司私有的意见收集出口：GitLab 通用包的基址
// (如 .../packages/generic/feedback) 和一个作用域仅 write_package_registry 的
// Deploy Token。构建时从 gitignored 的 build/update.local 经 -X main.feedbackEndpoint
// / -X main.feedbackToken 注入；公开构建为空 → 反馈禁用，二进制不含公司信息。
var (
	FeedbackEndpoint string
	FeedbackToken    string
)

// feedbackEnabled 报告意见收集出口是否已配置。
func feedbackEnabled() bool { return FeedbackEndpoint != "" && FeedbackToken != "" }

const (
	feedbackMaxImages = 5
	feedbackMaxImage  = 5 << 20  // 单张 5 MiB
	feedbackMaxTotal  = 30 << 20 // multipart 总量 30 MiB
)

// feedbackImage 是一张截图：字节 + 由 content-type 推出的扩展名。
type feedbackImage struct {
	ext  string
	data []byte
}

// feedbackMeta 是每条提交附带的非隐私上下文。
type feedbackMeta struct {
	version string
	os      string
	at      time.Time
}

// feedbackVersion 生成每条提交唯一的通用包版本号 "<YYYYMMDD-HHMMSS>-<4 hex>"。
// now 作为参数便于测试。
func feedbackVersion(now time.Time) string {
	var b [2]byte
	_, _ = rand.Read(b[:])
	return now.Format("20060102-150405") + "-" + hex.EncodeToString(b[:])
}

// extFromContentType 把图片 content-type 映射成文件扩展名。
func extFromContentType(ct string) string {
	switch {
	case strings.HasPrefix(ct, "image/png"):
		return ".png"
	case strings.HasPrefix(ct, "image/jpeg"):
		return ".jpg"
	case strings.HasPrefix(ct, "image/gif"):
		return ".gif"
	case strings.HasPrefix(ct, "image/webp"):
		return ".webp"
	default:
		return ".bin"
	}
}

// buildFeedbackZip 把提交打成内存 zip：feedback.md(正文+元数据) + screenshot-N.<ext>。
func buildFeedbackZip(text string, imgs []feedbackImage, meta feedbackMeta) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	md := fmt.Sprintf("# 意见反馈\n\n%s\n\n---\n版本: %s\n系统: %s\n时间: %s\n",
		text, meta.version, meta.os, meta.at.Format(time.RFC3339))
	fw, err := zw.Create("feedback.md")
	if err != nil {
		return nil, err
	}
	if _, err := io.WriteString(fw, md); err != nil {
		return nil, err
	}
	for i, im := range imgs {
		w, err := zw.Create(fmt.Sprintf("screenshot-%d%s", i+1, im.ext))
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(im.data); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// uploadFeedback 把 zip PUT 到 GitLab 通用包仓的 version 下，用 Deploy Token 鉴权。
func uploadFeedback(ctx context.Context, zipBytes []byte, version string) error {
	url := strings.TrimRight(FeedbackEndpoint, "/") + "/" + version + "/feedback.zip"
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(zipBytes))
	if err != nil {
		return err
	}
	req.Header.Set("DEPLOY-TOKEN", FeedbackToken)
	req.Header.Set("Content-Type", "application/zip")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("gitlab upload failed: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// handleFeedback 接收一条匿名意见（multipart：text + 至多 feedbackMaxImages 个 images
// 文件），打包并上传到 GitLab 包仓。提交不带用户名/机器名——只带 app 版本、OS、时间。
// 未配置出口时禁用（501）。
func (s *Server) handleFeedback(w http.ResponseWriter, r *http.Request) {
	if !originGuard(w, r) { // CSRF: reject cross-origin POSTs (defense-in-depth beyond Bearer)
		return
	}
	if !feedbackEnabled() {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "未启用"})
		return
	}
	if err := r.ParseMultipartForm(feedbackMaxTotal); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "表单解析失败"})
		return
	}
	text := strings.TrimSpace(r.FormValue("text"))
	if text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "意见不能为空"})
		return
	}
	var files []*multipart.FileHeader
	if r.MultipartForm != nil {
		files = r.MultipartForm.File["images"]
	}
	if len(files) > feedbackMaxImages {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "截图最多 5 张"})
		return
	}
	var imgs []feedbackImage
	for _, fh := range files {
		if fh.Size > feedbackMaxImage {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "单张截图不能超过 5MB"})
			return
		}
		f, err := fh.Open()
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "读取截图失败"})
			return
		}
		data, err := io.ReadAll(io.LimitReader(f, feedbackMaxImage+1))
		f.Close()
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "读取截图失败"})
			return
		}
		imgs = append(imgs, feedbackImage{ext: extFromContentType(fh.Header.Get("Content-Type")), data: data})
	}
	now := time.Now()
	version := feedbackVersion(now)
	zipBytes, err := buildFeedbackZip(text, imgs, feedbackMeta{
		version: Version,
		os:      runtime.GOOS + "/" + runtime.GOARCH,
		at:      now,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "打包失败"})
		return
	}
	if err := uploadFeedback(r.Context(), zipBytes, version); err != nil {
		log.Printf("feedback: 上传失败: %v", err) // 记状态码供排查；对用户只回通用文案，不泄露上游细节
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "提交失败，请稍后重试"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
