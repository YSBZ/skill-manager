package aihot

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"skillmanage/internal/content"
)

type resetCalendarWire struct {
	SchemaVersion int                      `json:"schemaVersion"`
	Timezone      string                   `json:"timezone"`
	Today         *string                  `json:"today"`
	CheckedAt     *string                  `json:"checkedAt"`
	HistoryFrom   string                   `json:"historyFrom"`
	Count         int                      `json:"count"`
	Events        []resetCalendarEventWire `json:"events"`
}

type resetCalendarEventWire struct {
	ID           string                     `json:"id"`
	Type         string                     `json:"type"`
	Label        string                     `json:"label"`
	DisplayLabel string                     `json:"displayLabel"`
	Status       string                     `json:"status"`
	Title        string                     `json:"title"`
	Scope        string                     `json:"scope"`
	CreatedAt    string                     `json:"createdAt"`
	UpdatedAt    string                     `json:"updatedAt"`
	ConfirmedAt  *string                    `json:"confirmedAt"`
	OccurredOn   *string                    `json:"occurredOn"`
	Schedule     *resetCalendarScheduleWire `json:"schedule"`
	Posts        []resetCalendarPostWire    `json:"posts"`
	URL          string                     `json:"url"`
}

type resetCalendarScheduleWire struct {
	Precision string  `json:"precision"`
	From      *string `json:"from"`
	Through   *string `json:"through"`
	Label     string  `json:"label"`
}

type resetCalendarPostWire struct {
	ID          string `json:"id"`
	PublishedAt string `json:"publishedAt"`
	Stage       string `json:"stage"`
	Text        string `json:"text"`
	URL         string `json:"url"`
}

type resetCalendarSnapshotResult struct {
	Snapshot     content.ResetCalendarSnapshot
	ETag         string
	CacheControl string
	LastModified string
	NotModified  bool
}

func (c *Client) FetchResetCalendar(ctx context.Context, etag string) (resetCalendarSnapshotResult, error) {
	status, headers, body, err := c.get(ctx, "/api/v1/codex-resets", nil, etag)
	if err != nil {
		return resetCalendarSnapshotResult{}, err
	}
	out := resetCalendarSnapshotResult{
		ETag: headers.Get("ETag"), CacheControl: headers.Get("Cache-Control"), LastModified: headers.Get("Last-Modified"),
	}
	if status == 304 {
		out.NotModified = true
		return out, nil
	}
	if status != 200 {
		return resetCalendarSnapshotResult{}, &upstreamError{Status: status, RetryAfter: headers.Get("Retry-After")}
	}
	var wire resetCalendarWire
	if err := json.Unmarshal(body, &wire); err != nil {
		return resetCalendarSnapshotResult{}, errIncompletePage
	}
	snapshot, err := normalizeResetCalendar(wire)
	if err != nil {
		return resetCalendarSnapshotResult{}, err
	}
	out.Snapshot = snapshot
	return out, nil
}

func normalizeResetCalendar(wire resetCalendarWire) (content.ResetCalendarSnapshot, error) {
	if wire.SchemaVersion != 1 || wire.Timezone != "Asia/Shanghai" || wire.HistoryFrom == "" ||
		!validCalendarTimestamp(wire.HistoryFrom) || wire.Events == nil || wire.Count != len(wire.Events) || wire.Count > 1000 {
		return content.ResetCalendarSnapshot{}, errIncompletePage
	}
	if wire.Today != nil && !validCalendarDate(*wire.Today) || wire.CheckedAt != nil && !validCalendarTimestamp(*wire.CheckedAt) {
		return content.ResetCalendarSnapshot{}, errIncompletePage
	}
	snapshot := content.ResetCalendarSnapshot{
		Timezone: wire.Timezone, Today: wire.Today, CheckedAt: wire.CheckedAt, HistoryFrom: wire.HistoryFrom,
		Events: make([]content.ResetCalendarEvent, 0, len(wire.Events)),
	}
	seen := make(map[string]bool, len(wire.Events))
	for _, event := range wire.Events {
		if strings.TrimSpace(event.ID) == "" || len(event.ID) > 160 || seen[event.ID] ||
			(event.Type != "direct_reset" && event.Type != "reset_credit") ||
			(event.Status != "announced" && event.Status != "confirmed") || event.Title == "" || len(event.Title) > 8000 ||
			(event.Label != "全员重置" && event.Label != "发重置卡") || event.CreatedAt == "" || event.UpdatedAt == "" ||
			!validCalendarTimestamp(event.CreatedAt) || !validCalendarTimestamp(event.UpdatedAt) || !validCalendarURL(event.URL) {
			return content.ResetCalendarSnapshot{}, errIncompletePage
		}
		if (event.Type == "direct_reset" && event.Label != "全员重置") || (event.Type == "reset_credit" && event.Label != "发重置卡") {
			return content.ResetCalendarSnapshot{}, errIncompletePage
		}
		seen[event.ID] = true
		if event.ConfirmedAt != nil && !validCalendarTimestamp(*event.ConfirmedAt) || event.OccurredOn != nil && !validCalendarDate(*event.OccurredOn) {
			return content.ResetCalendarSnapshot{}, errIncompletePage
		}
		label := strings.TrimSpace(event.DisplayLabel)
		if label == "" {
			if event.Type == "reset_credit" {
				label = "重置卡"
			} else {
				label = "额度重置"
			}
		}
		if len(label) > 100 || len(event.Scope) > 2000 {
			return content.ResetCalendarSnapshot{}, errIncompletePage
		}
		var schedule *content.ResetCalendarSchedule
		if event.Schedule != nil {
			if len(event.Schedule.Label) > 1000 || (event.Schedule.Precision != "exact" && event.Schedule.Precision != "approximate" && event.Schedule.Precision != "deadline" && event.Schedule.Precision != "date" && event.Schedule.Precision != "window") ||
				event.Schedule.From == nil || !validCalendarTimestamp(*event.Schedule.From) ||
				event.Schedule.Through == nil || !validCalendarTimestamp(*event.Schedule.Through) {
				return content.ResetCalendarSnapshot{}, errIncompletePage
			}
			schedule = &content.ResetCalendarSchedule{Precision: event.Schedule.Precision, From: event.Schedule.From, Through: event.Schedule.Through, Label: event.Schedule.Label}
		}
		posts := make([]content.ResetCalendarPost, 0, len(event.Posts))
		postIDs := make(map[string]bool, len(event.Posts))
		for _, post := range event.Posts {
			if post.ID == "" || len(post.ID) > 160 || postIDs[post.ID] || !validCalendarTimestamp(post.PublishedAt) || len(post.Stage) > 100 || len(post.Text) > 8000 || !validCalendarURL(post.URL) {
				return content.ResetCalendarSnapshot{}, errIncompletePage
			}
			postIDs[post.ID] = true
			posts = append(posts, content.ResetCalendarPost{ID: post.ID, PublishedAt: post.PublishedAt, Stage: post.Stage, Text: post.Text, URL: post.URL})
		}
		if event.Posts == nil || len(posts) > 500 {
			return content.ResetCalendarSnapshot{}, errIncompletePage
		}
		out := content.ResetCalendarEvent{
			ID: event.ID, Type: event.Type, DisplayLabel: label, Status: event.Status, Title: event.Title, Scope: event.Scope,
			OccurredOn: event.OccurredOn, ConfirmedAt: event.ConfirmedAt, CreatedAt: event.CreatedAt, UpdatedAt: event.UpdatedAt,
			Schedule: schedule, Posts: posts,
		}
		snapshot.Events = append(snapshot.Events, out)
	}
	return snapshot, nil
}

func validCalendarDate(value string) bool {
	parsed, err := time.Parse("2006-01-02", value)
	return err == nil && parsed.Format("2006-01-02") == value
}

func validCalendarTimestamp(value string) bool {
	if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
		return false
	}
	return true
}

func validCalendarURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && (parsed.Scheme == "https" || parsed.Scheme == "http") && parsed.Host != "" && parsed.User == nil
}
