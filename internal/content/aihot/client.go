package aihot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"skillmanage/internal/content"
)

const (
	requestTimeout  = 15 * time.Second
	maxResponseBody = 8 << 20
)

var (
	errInvalidBaseURL   = errors.New("invalid AIHOT base URL")
	errResponseTooLarge = errors.New("AIHOT response too large")
	errIncompletePage   = errors.New("incomplete AIHOT response")
	errInvalidQuery     = errors.New("invalid AIHOT query")
	errNoCachedResponse = errors.New("AIHOT returned not-modified without a cached response")
)

type Client struct {
	baseURL    *url.URL
	httpClient *http.Client
}

type upstreamError struct {
	Status     int
	RetryAfter string
}

func (e *upstreamError) Error() string {
	return fmt.Sprintf("AIHOT HTTP status %d", e.Status)
}

type itemsSnapshot struct {
	Entries      []content.Entry
	Notices      []map[string]any
	ETag         string
	CacheControl string
	LastModified string
	NotModified  bool
}

func NewClient(baseURL string, hc *http.Client) *Client {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed == nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		parsed = nil
	}
	var client http.Client
	if hc != nil {
		client = *hc
	}
	if client.Timeout == 0 || client.Timeout > requestTimeout {
		client.Timeout = requestTimeout
	}
	return &Client{baseURL: parsed, httpClient: &client}
}

func (c *Client) get(ctx context.Context, endpoint string, q url.Values, etag string) (int, http.Header, []byte, error) {
	return c.getWithAccept(ctx, endpoint, q, etag, "application/json")
}

func (c *Client) getHTML(ctx context.Context, endpoint string, q url.Values, etag string) (int, http.Header, []byte, error) {
	return c.getWithAccept(ctx, endpoint, q, etag, "text/html")
}

func (c *Client) getWithAccept(ctx context.Context, endpoint string, q url.Values, etag, accept string) (int, http.Header, []byte, error) {
	if c == nil || c.baseURL == nil || c.httpClient == nil {
		return 0, nil, nil, errInvalidBaseURL
	}
	if err := ctx.Err(); err != nil {
		return 0, nil, nil, err
	}
	u := *c.baseURL
	u.Path = strings.TrimRight(c.baseURL.Path, "/") + "/" + strings.TrimLeft(endpoint, "/")
	u.RawPath = ""
	u.RawQuery = ""
	if q != nil {
		u.RawQuery = q.Encode()
	}
	u.Fragment = ""
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return 0, nil, nil, errInvalidBaseURL
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "SkillManager-contentd/1.0")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return 0, nil, nil, ctxErr
		}
		return 0, nil, nil, errors.New("AIHOT request failed")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody+1))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return 0, nil, nil, ctxErr
		}
		return 0, nil, nil, errors.New("AIHOT response read failed")
	}
	if len(body) > maxResponseBody {
		return 0, nil, nil, errResponseTooLarge
	}
	return resp.StatusCode, resp.Header.Clone(), body, nil
}

func (c *Client) FetchItems(ctx context.Context, mode, window, etag string) (itemsSnapshot, error) {
	if (mode != "selected" && mode != "all") || (window != "24h" && window != "7d") {
		return itemsSnapshot{}, errInvalidQuery
	}
	out := itemsSnapshot{Entries: []content.Entry{}, Notices: []map[string]any{}}
	seenCursors := map[string]bool{}
	seenIDs := map[string]bool{}
	cursor := ""
	for pageNo := 0; pageNo < 100; pageNo++ {
		q := url.Values{"mode": {mode}, "window": {window}, "by": {"timeline"}, "limit": {"100"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		conditionalETag := ""
		if pageNo == 0 {
			conditionalETag = etag
		}
		status, headers, body, err := c.get(ctx, "/api/v1/items", q, conditionalETag)
		if err != nil {
			return itemsSnapshot{}, err
		}
		if pageNo == 0 && status == http.StatusNotModified {
			out.NotModified = true
			out.ETag = headers.Get("ETag")
			out.CacheControl = headers.Get("Cache-Control")
			out.LastModified = headers.Get("Last-Modified")
			return out, nil
		}
		if status != http.StatusOK {
			return itemsSnapshot{}, &upstreamError{Status: status, RetryAfter: headers.Get("Retry-After")}
		}
		var page itemsResponseWire
		if err := json.Unmarshal(body, &page); err != nil {
			return itemsSnapshot{}, errIncompletePage
		}
		if page.SchemaVersion != 1 || page.Items == nil || page.Page == nil || page.Query.Mode != mode || page.Query.Window != window || page.Query.By != "timeline" ||
			page.Query.Ordering != "timelineDesc" || page.Query.Category != nil || page.Query.Q != nil || page.Page.Count != len(page.Items) {
			return itemsSnapshot{}, errIncompletePage
		}
		if pageNo == 0 {
			out.ETag = headers.Get("ETag")
			out.CacheControl = headers.Get("Cache-Control")
			out.LastModified = headers.Get("Last-Modified")
		}
		for _, wire := range page.Items {
			entry, err := normalizeItem(wire)
			if err != nil || seenIDs[entry.ID] {
				return itemsSnapshot{}, errIncompletePage
			}
			seenIDs[entry.ID] = true
			out.Entries = append(out.Entries, entry)
		}
		out.Notices, err = appendNotice(out.Notices, page.Notice)
		if err != nil {
			return itemsSnapshot{}, errIncompletePage
		}
		if !page.Page.HasMore {
			return out, nil
		}
		if page.Page.NextCursor == nil || *page.Page.NextCursor == "" || seenCursors[*page.Page.NextCursor] {
			return itemsSnapshot{}, errIncompletePage
		}
		cursor = *page.Page.NextCursor
		seenCursors[cursor] = true
	}
	return itemsSnapshot{}, errIncompletePage
}
