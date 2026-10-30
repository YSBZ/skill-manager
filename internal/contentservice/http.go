package contentservice

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"skillmanage/internal/content"
)

// NewCatalogHandler assembles the catalog and its read-only API handler for
// both the embedded runtime and standalone contentd.
func NewCatalogHandler(store content.CatalogStore, detailLoader content.DetailLoader) http.Handler {
	catalog := content.NewCatalog(store, nil)
	catalog.SetDetailLoader(detailLoader)
	return NewHandler(catalog)
}

func NewHandler(reader content.Reader) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/feed", func(w http.ResponseWriter, r *http.Request) {
		values, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			writeAPIError(w, content.ErrBadQuery)
			return
		}
		q, err := parseQuery(values)
		if err != nil {
			writeAPIError(w, err)
			return
		}
		page, err := reader.Feed(r.Context(), q)
		if err != nil {
			writeAPIError(w, err)
			return
		}
		writeJSON(w, r, page)
	})
	mux.HandleFunc("GET /v1/entries/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if err := content.ValidateID(id); err != nil {
			writeAPIError(w, err)
			return
		}
		result, err := reader.Entry(r.Context(), id)
		if err != nil {
			writeAPIError(w, err)
			return
		}
		writeJSON(w, r, result)
	})
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		meta, err := reader.Status(r.Context())
		if err != nil {
			writeAPIError(w, err)
			return
		}
		writeJSON(w, r, meta)
	})
	mux.HandleFunc("GET /v1/reset-calendar", func(w http.ResponseWriter, r *http.Request) {
		values, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(values) != 0 {
			writeAPIError(w, content.ErrBadQuery)
			return
		}
		calendarReader, ok := reader.(content.ResetCalendarReader)
		if !ok {
			writeAPIError(w, content.ErrUnavailable)
			return
		}
		calendar, err := calendarReader.ResetCalendar(r.Context())
		if err != nil {
			writeAPIError(w, err)
			return
		}
		writeJSON(w, r, calendar)
	})
	mux.HandleFunc("/v1/model-leaderboard", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusMethodNotAllowed)
			_ = json.NewEncoder(w).Encode(content.APIError{ErrorCode: "method_not_allowed", Error: "请求方法不支持"})
			return
		}
		values, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			writeAPIError(w, content.ErrBadQuery)
			return
		}
		category, err := parseModelLeaderboardCategory(values)
		if err != nil {
			writeAPIError(w, err)
			return
		}
		leaderboardReader, ok := reader.(content.ModelLeaderboardReader)
		if !ok {
			writeAPIError(w, content.ErrUnavailable)
			return
		}
		leaderboard, err := leaderboardReader.ModelLeaderboard(r.Context(), category)
		if err != nil {
			writeAPIError(w, err)
			return
		}
		writeJSON(w, r, leaderboard)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	})
	return mux
}

func parseModelLeaderboardCategory(values url.Values) (string, error) {
	for key, entries := range values {
		if key != "category" || len(entries) != 1 || entries[0] == "" {
			return "", content.ErrBadQuery
		}
	}
	if _, present := values["category"]; !present {
		return content.ModelLeaderboardOverall, nil
	}
	category, err := content.NormalizeModelLeaderboardCategory(values.Get("category"))
	if err != nil {
		return "", content.ErrBadQuery
	}
	return category, nil
}

func parseQuery(v url.Values) (content.Query, error) {
	for _, key := range []string{"kind", "view", "window", "q", "category", "scene", "limit", "cursor"} {
		if len(v[key]) > 1 {
			return content.Query{}, content.ErrBadQuery
		}
	}
	q := content.Query{
		Kind:     v.Get("kind"),
		View:     v.Get("view"),
		Window:   v.Get("window"),
		Q:        v.Get("q"),
		Category: v.Get("category"),
		Scene:    v.Get("scene"),
		Cursor:   v.Get("cursor"),
	}
	if values, present := v["limit"]; present {
		if len(values) != 1 {
			return content.Query{}, content.ErrBadQuery
		}
		n, err := strconv.Atoi(values[0])
		if err != nil || n < 1 || n > 50 {
			return content.Query{}, content.ErrBadQuery
		}
		q.Limit = n
	} else {
		q.Limit = 20
	}
	return q, nil
}

func writeJSON(w http.ResponseWriter, r *http.Request, value any) {
	b, err := json.Marshal(value)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	h := sha256.Sum256(b)
	etag := `"` + hex.EncodeToString(h[:]) + `"`
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

func writeAPIError(w http.ResponseWriter, err error) {
	status, code, message := http.StatusInternalServerError, "internal_error", "内容服务暂时不可用"
	switch {
	case errors.Is(err, content.ErrBadQuery):
		status, code, message = http.StatusBadRequest, "bad_query", "查询参数无效"
	case errors.Is(err, content.ErrCursorExpired):
		status, code, message = http.StatusConflict, "cursor_expired", "分页游标已失效"
	case errors.Is(err, content.ErrWithdrawn):
		status, code, message = http.StatusGone, "withdrawn", "内容已撤回"
	case errors.Is(err, content.ErrNotFound):
		status, code, message = http.StatusNotFound, "not_found", "内容不存在"
	case errors.Is(err, content.ErrUnavailable):
		status, code, message = http.StatusServiceUnavailable, "unavailable", "内容来源暂时不可用"
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(content.APIError{ErrorCode: code, Error: message})
}
