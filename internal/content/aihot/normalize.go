package aihot

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"path"
	"strings"
	"time"

	"skillmanage/internal/content"
)

var (
	errInvalidNotice = errors.New("invalid AIHOT notice")
	errInvalidItem   = errors.New("invalid AIHOT item")
	errInvalidReport = errors.New("invalid AIHOT report")
	errInvalidTopic  = errors.New("invalid AIHOT topic")
)

func validHTTPURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil || u.Opaque != "" {
		return ""
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	return u.String()
}

func validAttribution(w *attributionWire) bool {
	return w == nil || (w.Name != "" && validHTTPURL(w.URL) != "")
}

func appendNotice(dst []map[string]any, raw json.RawMessage) ([]map[string]any, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return dst, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var notice map[string]any
	if err := dec.Decode(&notice); err != nil || notice == nil {
		return nil, errInvalidNotice
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errInvalidNotice
	}
	return append(dst, notice), nil
}

func normalizeItem(w itemWire) (content.Entry, error) {
	if w.ID == "" || w.Title == "" || w.Source.Name == "" || !validAttribution(w.Attribution) {
		return content.Entry{}, errInvalidItem
	}
	if w.DiscoveredAt != "" && !validDateTime(w.DiscoveredAt) {
		return content.Entry{}, errInvalidItem
	}
	if w.PublishedAt != nil && *w.PublishedAt != "" && !validDateTime(*w.PublishedAt) {
		return content.Entry{}, errInvalidItem
	}
	if validHTTPURL(w.Links.Original) == "" {
		return content.Entry{}, errInvalidItem
	}
	attr := content.Source{Name: "AIHOT", URL: "https://aihot.news"}
	if w.Attribution != nil {
		attr = content.Source{Name: w.Attribution.Name, URL: validHTTPURL(w.Attribution.URL)}
	}
	e := content.Entry{
		ID: "news_" + w.ID, Kind: "news", Title: w.Title,
		Source: content.Source{Name: w.Source.Name}, Attribution: attr,
		CanonicalURL: validHTTPURL(w.Links.AIHOT), OriginalURL: validHTTPURL(w.Links.Original),
		DiscoveredAt: w.DiscoveredAt, Selected: w.Selected, Paragraphs: []content.Paragraph{},
	}
	if w.Summary != nil {
		e.Summary = *w.Summary
	}
	if w.Reason != nil {
		e.Reason = *w.Reason
	}
	if w.Category != nil {
		e.Category = *w.Category
	}
	if w.PublishedAt != nil {
		e.PublishedAt = *w.PublishedAt
	}
	if validContentID(e.ID) != nil || e.CanonicalURL == "" {
		return content.Entry{}, errInvalidItem
	}
	return e, nil
}

func normalizeArticle(heading string, w articleWire) content.Paragraph {
	u := validHTTPURL(w.Links.Original)
	if u == "" && w.Links.AIHOT != nil {
		u = validHTTPURL(*w.Links.AIHOT)
	}
	var src, attr *content.Source
	if w.Source.Name != "" {
		src = &content.Source{Name: w.Source.Name}
	}
	if w.Attribution != nil {
		attr = &content.Source{Name: w.Attribution.Name, URL: validHTTPURL(w.Attribution.URL)}
	}
	return content.Paragraph{Heading: heading, Text: w.Summary, URL: u, Source: src, Attribution: attr}
}

func validArticle(w articleWire) bool {
	if w.Title == "" || w.Source.Name == "" || !validAttribution(w.Attribution) {
		return false
	}
	if w.PublishedAt != nil && *w.PublishedAt != "" && !validDateTime(*w.PublishedAt) {
		return false
	}
	return validHTTPURL(w.Links.Original) != "" || (w.Links.AIHOT != nil && validHTTPURL(*w.Links.AIHOT) != "")
}

func normalizeDaily(w dailyWire) (content.Entry, error) {
	if !validDate(w.Date) || !validDateTime(w.GeneratedAt) || validHTTPURL(w.Links.AIHOT) == "" || !validAttribution(w.Attribution) || w.Sections == nil || w.Flashes == nil {
		return content.Entry{}, errInvalidReport
	}
	if w.Lead != nil && (w.Lead.Title == "" || w.Lead.LeadParagraph == "") {
		return content.Entry{}, errInvalidReport
	}
	attr := archiveAttribution(w.Attribution)
	e := content.Entry{ID: "daily_" + w.Date, Kind: "daily", Title: w.Date, Period: w.Date,
		PublishedAt: w.GeneratedAt, Source: content.Source{Name: "AIHOT", URL: "https://aihot.news"},
		Attribution: attr, CanonicalURL: validHTTPURL(w.Links.AIHOT), Paragraphs: []content.Paragraph{}}
	if w.Lead != nil {
		e.Title = w.Lead.Title
		e.Summary = w.Lead.LeadParagraph
	}
	for _, section := range w.Sections {
		if section.Items == nil {
			return content.Entry{}, errInvalidReport
		}
		for _, item := range section.Items {
			if !validArticle(item) {
				return content.Entry{}, errInvalidReport
			}
			e.Paragraphs = append(e.Paragraphs, normalizeArticle(section.Label+" / "+item.Title, item))
		}
	}
	for _, flash := range w.Flashes {
		if flash.Title == "" || flash.Source.Name == "" || !validDateTime(flash.PublishedAt) || !validAttribution(flash.Attribution) ||
			(validHTTPURL(flash.Links.Original) == "" && (flash.Links.AIHOT == nil || validHTTPURL(*flash.Links.AIHOT) == "")) {
			return content.Entry{}, errInvalidReport
		}
		item := articleWire{Title: flash.Title, Source: flash.Source, Links: flash.Links, Attribution: flash.Attribution}
		e.Paragraphs = append(e.Paragraphs, normalizeArticle(flash.Title, item))
	}
	return e, nil
}

func normalizePeriod(kind string, w periodWire) (content.Entry, error) {
	period, idPrefix := w.Week, "weekly_"
	if kind == "monthly" {
		period, idPrefix = w.Month, "monthly_"
	}
	if (kind != "weekly" && kind != "monthly") || !validPeriod(kind, period) || !validDateTime(w.GeneratedAt) || validHTTPURL(w.Links.AIHOT) == "" || !validAttribution(w.Attribution) || w.Sections == nil {
		return content.Entry{}, errInvalidReport
	}
	title, summary := period, ""
	if w.Headline != nil && *w.Headline != "" {
		title = *w.Headline
	}
	if w.Overview != nil {
		summary = *w.Overview
	}
	e := content.Entry{ID: idPrefix + period, Kind: kind, Title: title, Summary: summary, Period: period,
		PublishedAt: w.GeneratedAt, Source: content.Source{Name: "AIHOT", URL: "https://aihot.news"},
		Attribution: archiveAttribution(w.Attribution), CanonicalURL: validHTTPURL(w.Links.AIHOT), Paragraphs: []content.Paragraph{}}
	for _, section := range w.Sections {
		if section.Items == nil {
			return content.Entry{}, errInvalidReport
		}
		if section.Summary != nil && *section.Summary != "" {
			e.Paragraphs = append(e.Paragraphs, content.Paragraph{Heading: section.Label, Text: *section.Summary})
		}
		for _, item := range section.Items {
			if !validArticle(item) {
				return content.Entry{}, errInvalidReport
			}
			e.Paragraphs = append(e.Paragraphs, normalizeArticle(section.Label+" / "+item.Title, item))
		}
	}
	return e, nil
}

func archiveAttribution(w *attributionWire) content.Source {
	if w == nil {
		return content.Source{Name: "AIHOT", URL: "https://aihot.news"}
	}
	return content.Source{Name: w.Name, URL: validHTTPURL(w.URL)}
}

func normalizeDailyArchiveItem(w dailyArchiveItemWire) (content.Entry, error) {
	if !validDate(w.Date) || !validDateTime(w.GeneratedAt) || validHTTPURL(w.Links.AIHOT) == "" || !validAttribution(w.Attribution) {
		return content.Entry{}, errInvalidReport
	}
	title, summary := w.Date, ""
	if w.LeadTitle != nil && *w.LeadTitle != "" {
		title = *w.LeadTitle
	}
	if w.LeadParagraph != nil {
		summary = *w.LeadParagraph
	}
	return content.Entry{ID: "daily_" + w.Date, Kind: "daily", Title: title, Summary: summary, Period: w.Date,
		PublishedAt: w.GeneratedAt, Source: content.Source{Name: "AIHOT", URL: "https://aihot.news"},
		Attribution: archiveAttribution(w.Attribution), CanonicalURL: validHTTPURL(w.Links.AIHOT), Paragraphs: []content.Paragraph{}}, nil
}

func validISOWeek(period string) bool {
	if len(period) != 8 || period[4:6] != "-W" {
		return false
	}
	year, errYear := parseDigits(period[:4])
	week, errWeek := parseDigits(period[6:])
	if errYear != nil || errWeek != nil || week < 1 || week > 53 {
		return false
	}
	jan4 := time.Date(year, time.January, 4, 0, 0, 0, 0, time.UTC)
	weekday := int(jan4.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	weekOneMonday := jan4.AddDate(0, 0, 1-weekday)
	actualYear, actualWeek := weekOneMonday.AddDate(0, 0, 7*(week-1)).ISOWeek()
	return actualYear == year && actualWeek == week
}

func normalizePeriodArchiveItem(kind, period, generatedAt string, headline *string, links issueLinksWire, attribution *attributionWire) (content.Entry, error) {
	prefix := "weekly_"
	if kind == "monthly" {
		prefix = "monthly_"
	}
	if !validPeriod(kind, period) || !validDateTime(generatedAt) || validHTTPURL(links.AIHOT) == "" || !validAttribution(attribution) {
		return content.Entry{}, errInvalidReport
	}
	title := period
	if headline != nil && *headline != "" {
		title = *headline
	}
	return content.Entry{ID: prefix + period, Kind: kind, Title: title, Period: period, PublishedAt: generatedAt,
		Source: content.Source{Name: "AIHOT", URL: "https://aihot.news"}, Attribution: archiveAttribution(attribution),
		CanonicalURL: validHTTPURL(links.AIHOT), Paragraphs: []content.Paragraph{}}, nil
}

func normalizeTopic(h hotTopicWire, story *storyWire) (content.Entry, error) {
	if h.ID == "" || h.Title == "" || h.Source.Name == "" || !validDateTime(h.LatestAt) || validHTTPURL(h.Links.AIHOT) == "" || validContentID("topic_"+h.ID) != nil {
		return content.Entry{}, errInvalidTopic
	}
	e := content.Entry{ID: "topic_" + h.ID, Kind: "topic", Title: h.Title,
		Source: content.Source{Name: h.Source.Name}, Attribution: content.Source{Name: "AIHOT", URL: "https://aihot.news"},
		CanonicalURL: validHTTPURL(h.Links.AIHOT), OriginalURL: validHTTPURL(h.Links.Original),
		PublishedAt: h.LatestAt, Paragraphs: []content.Paragraph{}}
	if story == nil {
		return e, nil
	}
	if story.Title == "" || story.PublicID == "" || validHTTPURL(story.Links.AIHOT) == "" || story.Reports == nil {
		return content.Entry{}, errInvalidTopic
	}
	e.Title, e.Summary = story.Title, story.Latest
	if story.Digest != nil {
		e.Summary = *story.Digest
	}
	for _, r := range story.Reports {
		if r.Title == "" || r.Source.Name == "" || !validDateTime(r.PublishedAt) || validHTTPURL(r.Links.AIHOT) == "" {
			return content.Entry{}, errInvalidTopic
		}
		summary, link := "", validHTTPURL(r.Links.AIHOT)
		if r.Summary != nil {
			summary = *r.Summary
		}
		if r.Links.Original != nil && validHTTPURL(*r.Links.Original) != "" {
			link = validHTTPURL(*r.Links.Original)
		}
		src := &content.Source{Name: r.Source.Name}
		attr := &content.Source{Name: "AIHOT", URL: "https://aihot.news"}
		e.Paragraphs = append(e.Paragraphs, content.Paragraph{Heading: r.Title, Text: summary, URL: link, Source: src, Attribution: attr})
	}
	return e, nil
}

func storyPublicID(raw string) (string, bool) {
	raw = validHTTPURL(raw)
	if raw == "" {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	escaped := strings.TrimRight(u.EscapedPath(), "/")
	if escaped == "" {
		return "", false
	}
	segment, err := url.PathUnescape(path.Base(escaped))
	if err != nil || segment == "" || strings.ContainsAny(segment, "/\\") || len(segment) > 128 {
		return "", false
	}
	return segment, true
}

func validPeriod(kind, period string) bool {
	switch kind {
	case "daily":
		return validDate(period)
	case "weekly":
		return validISOWeek(period)
	case "monthly":
		parsed, err := time.Parse("2006-01", period)
		return err == nil && parsed.Format("2006-01") == period
	default:
		return false
	}
}

func validDate(value string) bool {
	parsed, err := time.Parse("2006-01-02", value)
	return err == nil && parsed.Format("2006-01-02") == value
}

func validDateTime(value string) bool {
	if value == "" {
		return false
	}
	_, err := time.Parse(time.RFC3339, value)
	return err == nil
}

func parseDigits(value string) (int, error) {
	if value == "" {
		return 0, errInvalidReport
	}
	var n int
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return 0, errInvalidReport
		}
		n = n*10 + int(digit-'0')
	}
	return n, nil
}

func validContentID(id string) error { return content.ValidateID(id) }
