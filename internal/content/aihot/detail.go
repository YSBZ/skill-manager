package aihot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"skillmanage/internal/content"
)

var errDetailRetryPending = errors.New("AIHOT report detail is stale pending retry")

func (r *Refresher) ResolveDetail(ctx context.Context, id string) (content.Entry, error) {
	return r.resolveDetailAt(ctx, id, time.Now())
}

func (r *Refresher) resolveDetailAt(ctx context.Context, id string, now time.Time) (content.Entry, error) {
	if r == nil || r.client == nil || r.store == nil {
		return content.Entry{}, errors.New("AIHOT detail resolver unavailable")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := content.ValidateID(id); err != nil {
		return content.Entry{}, fmt.Errorf("invalid AIHOT entry ID: %w", err)
	}
	entry, err := r.store.Get(ctx, id)
	if err != nil {
		return content.Entry{}, err
	}
	member, err := r.store.HasMembership(ctx, id)
	if err != nil {
		return content.Entry{}, fmt.Errorf("check AIHOT report membership: %w", err)
	}
	if !member {
		withdrawn, err := r.store.IsWithdrawn(ctx, id)
		if err != nil {
			return content.Entry{}, fmt.Errorf("check AIHOT report withdrawal: %w", err)
		}
		if withdrawn {
			return content.Entry{}, content.ErrWithdrawn
		}
		return content.Entry{}, content.ErrNotFound
	}
	endpoint, kind, period, err := detailEndpoint(entry, id)
	if err != nil {
		return content.Entry{}, err
	}
	name := "aihot:detail:" + id
	previous, exists, err := r.checkpoint(ctx, name)
	if err != nil {
		return content.Entry{}, fmt.Errorf("read AIHOT detail checkpoint: %w", err)
	}
	if exists && previous.NextFetchAt.After(now) {
		if previous.Status.State == "fresh" && previous.Status.LastSuccessAt != "" {
			return entry, nil
		}
		return content.Entry{}, errDetailRetryPending
	}
	status, headers, body, err := r.client.get(ctx, endpoint, nil, previous.ETag)
	if err != nil {
		return content.Entry{}, r.failedSource(ctx, name, previous, exists, now, err)
	}
	if status == http.StatusNotFound || status == http.StatusGone {
		if err := r.store.Delete(ctx, id); err != nil {
			return content.Entry{}, fmt.Errorf("record withdrawn AIHOT report: %w", err)
		}
		return content.Entry{}, content.ErrWithdrawn
	}
	if status == http.StatusNotModified {
		if !exists || previous.Status.LastSuccessAt == "" {
			return content.Entry{}, r.failedSource(ctx, name, previous, exists, now, errNoCachedResponse)
		}
		if err := r.saveNotices(ctx, nil); err != nil {
			return content.Entry{}, fmt.Errorf("save AIHOT notices: %w", err)
		}
		if err := r.successSource(ctx, name, previous, headers.Get("ETag"), headers.Get("Cache-Control"), headers.Get("Last-Modified"), now, true); err != nil {
			return content.Entry{}, fmt.Errorf("save AIHOT detail checkpoint: %w", err)
		}
		return entry, nil
	}
	if status != http.StatusOK {
		return content.Entry{}, r.failedSource(ctx, name, previous, exists, now, &upstreamError{Status: status, RetryAfter: headers.Get("Retry-After")})
	}
	loaded, notice, err := normalizeDetailedReport(kind, body)
	if err != nil || loaded.ID != id || loaded.Kind != kind || loaded.Period != period {
		if err == nil {
			err = errInvalidReport
		}
		return content.Entry{}, r.failedSource(ctx, name, previous, exists, now, err)
	}
	if err := r.store.Upsert(ctx, loaded); err != nil {
		return content.Entry{}, fmt.Errorf("save AIHOT report detail %q: %w", id, err)
	}
	if err := r.saveNotices(ctx, notice); err != nil {
		return content.Entry{}, fmt.Errorf("save AIHOT notices: %w", err)
	}
	if err := r.successSource(ctx, name, previous, headers.Get("ETag"), headers.Get("Cache-Control"), headers.Get("Last-Modified"), now, false); err != nil {
		return content.Entry{}, fmt.Errorf("save AIHOT detail checkpoint: %w", err)
	}
	return loaded, nil
}

func detailEndpoint(entry content.Entry, id string) (string, string, string, error) {
	kind, period := entry.Kind, entry.Period
	if !validPeriod(kind, period) {
		return "", "", "", errInvalidReport
	}
	prefix := kind + "_"
	if kind == "daily" {
		prefix = "daily_"
	}
	if kind == "weekly" {
		prefix = "weekly_"
	}
	if kind == "monthly" {
		prefix = "monthly_"
	}
	if id != prefix+period {
		return "", "", "", errInvalidReport
	}
	switch kind {
	case "daily":
		return "/api/v1/dailies/" + period, kind, period, nil
	case "weekly":
		return "/api/v1/weeklies/" + period, kind, period, nil
	case "monthly":
		return "/api/v1/monthlies/" + period, kind, period, nil
	default:
		return "", "", "", errInvalidReport
	}
}

func normalizeDetailedReport(kind string, body []byte) (content.Entry, []map[string]any, error) {
	switch kind {
	case "daily":
		var response dailyResponseWire
		if err := json.Unmarshal(body, &response); err != nil || response.SchemaVersion != 1 {
			return content.Entry{}, nil, errInvalidReport
		}
		entry, err := normalizeDaily(response.Report)
		if err != nil {
			return content.Entry{}, nil, err
		}
		notices, err := appendNotice(nil, response.Notice)
		return entry, notices, err
	case "weekly", "monthly":
		var response periodResponseWire
		if err := json.Unmarshal(body, &response); err != nil || response.SchemaVersion != 1 {
			return content.Entry{}, nil, errInvalidReport
		}
		entry, err := normalizePeriod(kind, response.Report)
		if err != nil {
			return content.Entry{}, nil, err
		}
		notices, err := appendNotice(nil, response.Notice)
		return entry, notices, err
	default:
		return content.Entry{}, nil, errInvalidReport
	}
}
