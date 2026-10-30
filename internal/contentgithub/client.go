package contentgithub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	maxResponseBytes = 8 << 20
	maxClientTimeout = 20 * time.Second
)

var (
	errRequestBudget       = errors.New("github request budget paused")
	errInvalidResponseJSON = errors.New("invalid github response JSON")
)

type responseJSONError struct{ resource string }

func (e responseJSONError) Error() string {
	return fmt.Sprintf("github %s response JSON is invalid", e.resource)
}

func (e responseJSONError) Unwrap() error { return errInvalidResponseJSON }

type requestError struct {
	status      int
	retryAt     time.Time
	resource    string
	rateLimited bool
}

func (e *requestError) Error() string {
	return fmt.Sprintf("github %s request returned HTTP %d", e.resource, e.status)
}

func NewClient(baseURL string, hc *http.Client, token string) *Client {
	if hc == nil {
		hc = &http.Client{}
	}
	clientCopy := *hc
	if clientCopy.Timeout <= 0 || clientCopy.Timeout > maxClientTimeout {
		clientCopy.Timeout = maxClientTimeout
	}
	previousRedirect := clientCopy.CheckRedirect
	clientCopy.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > 0 && !sameOrigin(via[0].URL, req.URL) {
			return http.ErrUseLastResponse
		}
		if previousRedirect != nil {
			return previousRedirect(req, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	hc = &clientCopy
	return &Client{
		baseURL:         strings.TrimRight(baseURL, "/"),
		hc:              hc,
		token:           strings.TrimSpace(token),
		coreBudget:      40,
		searchGate:      make(chan struct{}, 1),
		searchRemaining: -1,
	}
}

func (c *Client) getJSON(ctx context.Context, now time.Time, resource, path string, q url.Values, dst any) error {
	if resource != "core" && resource != "search" {
		return errors.New("invalid github resource")
	}
	u, err := apiURL(c.baseURL, path, q)
	if err != nil {
		return errors.New("invalid github API URL")
	}
	if err := ctx.Err(); err != nil {
		return sanitizedRequestError(resource, err)
	}
	if resource == "search" {
		if err := c.acquireSearch(ctx); err != nil {
			return err
		}
		defer func() { <-c.searchGate }()
	}
	if err := c.allow(now, resource); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return errors.New("cannot create github request")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "SkillManager-content-discovery")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return sanitizedRequestError(resource, err)
	}
	defer resp.Body.Close()
	c.observe(now, resource, resp.Header)
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		retryAt := retryTime(now, resp.Header)
		rateLimited := resp.StatusCode == http.StatusTooManyRequests ||
			(resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0")
		if rateLimited {
			c.setRetry(resource, retryAt)
		}
		return &requestError{
			status: resp.StatusCode, retryAt: retryAt,
			resource: resource, rateLimited: rateLimited,
		}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return sanitizedResponseReadError(resource, err)
	}
	if len(body) > maxResponseBytes {
		return fmt.Errorf("github %s response exceeds 8 MiB limit", resource)
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return responseJSONError{resource: resource}
	}
	return nil
}

func (c *Client) allow(now time.Time, resource string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch resource {
	case "core":
		if c.coreBucket.IsZero() || !now.Before(c.coreBucket.Add(time.Hour)) {
			c.coreBucket, c.coreCount = now, 0
		}
		if !c.coreReset.IsZero() && !now.Before(c.coreReset) {
			c.coreReset = time.Time{}
			c.coreRemaining = -1
		}
		if now.Before(c.coreRetryAt) || c.coreCount >= c.coreBudget ||
			(c.coreLimit > 0 && c.coreRemaining >= 0 &&
				c.coreRemaining <= max(0, c.coreLimit-c.coreBudget)) {
			return errRequestBudget
		}
		c.coreCount++
		return nil
	case "search":
		if c.searchBucket.IsZero() {
			c.searchBucket, c.searchCount = now, 0
		} else if !c.searchReset.IsZero() && !now.Before(c.searchReset) {
			c.searchBucket, c.searchCount = now, 0
			c.searchRemaining = c.searchLimit
			if c.searchLimit == 0 {
				c.searchRemaining = -1
			}
			c.searchReset = time.Time{}
		} else if c.searchReset.IsZero() && !now.Before(c.searchBucket.Add(time.Minute)) {
			c.searchBucket, c.searchCount = now, 0
			c.searchRemaining = c.searchLimit
			if c.searchLimit == 0 {
				c.searchRemaining = -1
			}
		}
		if now.Before(c.searchRetryAt) || (c.searchLimit > 0 && c.searchCount >= c.searchLimit) || c.searchRemaining == 0 {
			return errRequestBudget
		}
		c.searchCount++
		if c.searchRemaining > 0 {
			c.searchRemaining--
		}
		return nil
	default:
		return errors.New("invalid github resource")
	}
}

func (c *Client) observe(now time.Time, resource string, h http.Header) {
	limit, limitErr := strconv.Atoi(h.Get("X-RateLimit-Limit"))
	remaining, remainingErr := strconv.Atoi(h.Get("X-RateLimit-Remaining"))
	resetUnix, resetErr := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64)
	c.mu.Lock()
	defer c.mu.Unlock()
	if resource == "core" {
		if limitErr == nil && limit > 0 {
			c.coreLimit = limit
		}
		if remainingErr == nil && h.Get("X-RateLimit-Remaining") != "" {
			c.coreRemaining = remaining
		}
		if resetErr == nil && resetUnix > 0 {
			c.coreReset = time.Unix(resetUnix, 0)
		}
		return
	}
	if limitErr == nil && limit > 0 {
		c.searchLimit = limit
	}
	if resetErr == nil && resetUnix > 0 {
		reset := time.Unix(resetUnix, 0)
		if !c.searchLastReset.IsZero() && reset.Before(c.searchLastReset) {
			return
		}
		if !reset.After(now) && !c.searchLastReset.IsZero() && reset.Equal(c.searchLastReset) {
			return
		}
		if reset.After(c.searchLastReset) {
			c.searchLastReset = reset
			c.searchReset = reset
			c.searchRemaining = -1
			if remainingErr == nil && h.Get("X-RateLimit-Remaining") != "" {
				c.searchRemaining = remaining
			}
			return
		}
		c.searchReset = reset
	}
	if remainingErr == nil && h.Get("X-RateLimit-Remaining") != "" {
		if c.searchRemaining < 0 || remaining < c.searchRemaining {
			c.searchRemaining = remaining
		}
	}
}

func (c *Client) acquireSearch(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return sanitizedRequestError("search", err)
	}
	select {
	case c.searchGate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-c.searchGate
			return sanitizedRequestError("search", err)
		}
		return nil
	case <-ctx.Done():
		return sanitizedRequestError("search", ctx.Err())
	}
}

func (c *Client) setRetry(resource string, at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if resource == "core" {
		if at.After(c.coreRetryAt) {
			c.coreRetryAt = at
		}
		return
	}
	if at.After(c.searchRetryAt) {
		c.searchRetryAt = at
	}
}

func retryTime(now time.Time, h http.Header) time.Time {
	if seconds, err := strconv.Atoi(h.Get("Retry-After")); err == nil && seconds > 0 {
		maxSeconds := int64(^uint64(0)>>1) / int64(time.Second)
		if int64(seconds) > maxSeconds {
			seconds = int(maxSeconds)
		}
		return now.Add(time.Duration(seconds) * time.Second)
	}
	if at, err := http.ParseTime(h.Get("Retry-After")); err == nil && at.After(now) {
		return at
	}
	if reset, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); err == nil && reset > now.Unix() {
		return time.Unix(reset, 0)
	}
	return now.Add(time.Minute)
}

func apiURL(baseURL, path string, q url.Values) (*url.URL, error) {
	base, err := url.Parse(baseURL)
	if err != nil || base == nil || (base.Scheme != "http" && base.Scheme != "https") ||
		base.Hostname() == "" || base.User != nil || base.Opaque != "" || base.RawQuery != "" || base.ForceQuery || base.Fragment != "" {
		return nil, errors.New("invalid base URL")
	}
	for _, segment := range strings.Split(strings.Trim(base.Path, "/"), "/") {
		if segment == "." || segment == ".." {
			return nil, errors.New("invalid base path")
		}
	}
	if path == "" || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") ||
		strings.ContainsAny(path, "?#\\\r\n") {
		return nil, errors.New("invalid endpoint path")
	}
	endpoint, err := url.Parse(path)
	if err != nil || endpoint == nil || endpoint.IsAbs() || endpoint.Host != "" || endpoint.Opaque != "" ||
		endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" || !strings.HasPrefix(endpoint.Path, "/") {
		return nil, errors.New("invalid endpoint path")
	}
	escapedEndpointPath := endpoint.EscapedPath()
	rawSegments := strings.Split(strings.TrimPrefix(escapedEndpointPath, "/"), "/")
	branchSegment := len(rawSegments) == 6 && rawSegments[0] == "repos" &&
		rawSegments[3] == "git" && rawSegments[4] == "trees"
	for i, rawSegment := range rawSegments {
		segment, err := url.PathUnescape(rawSegment)
		if err != nil || strings.ContainsAny(segment, "\\\r\n\x00") {
			return nil, errors.New("invalid endpoint path")
		}
		decodedSegments := strings.Split(segment, "/")
		if strings.Contains(segment, "/") && !(branchSegment && i == 5) {
			return nil, errors.New("invalid endpoint path")
		}
		for _, decodedSegment := range decodedSegments {
			if decodedSegment == "." || decodedSegment == ".." {
				return nil, errors.New("invalid endpoint path")
			}
		}
	}
	u := *base
	u.Path = strings.TrimRight(base.Path, "/") + endpoint.Path
	escapedPath := strings.TrimRight(base.EscapedPath(), "/") + escapedEndpointPath
	u.RawPath = ""
	if escapedPath != u.Path {
		u.RawPath = escapedPath
	}
	u.RawQuery = q.Encode()
	u.ForceQuery = false
	return &u, nil
}

func sameOrigin(a, b *url.URL) bool {
	return a != nil && b != nil && strings.EqualFold(a.Scheme, b.Scheme) &&
		strings.EqualFold(a.Hostname(), b.Hostname()) && effectivePort(a) == effectivePort(b)
}

func effectivePort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}
	return "80"
}

func sanitizedRequestError(resource string, err error) error {
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("github %s request canceled: %w", resource, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("github %s request timed out: %w", resource, context.DeadlineExceeded)
	}
	return fmt.Errorf("github %s request failed", resource)
}

func sanitizedResponseReadError(resource string, err error) error {
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("github %s response read canceled: %w", resource, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("github %s response read timed out: %w", resource, context.DeadlineExceeded)
	}
	return fmt.Errorf("github %s response read failed", resource)
}
