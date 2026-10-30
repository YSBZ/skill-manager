package aihot

import (
	"encoding/json"
	"testing"
)

func TestNormalizeItemUsesUpstreamTimestampsAndAttribution(t *testing.T) {
	published := (*string)(nil)
	discovered := "2026-10-28T10:00:00Z"
	category := "ai-products"
	reason := "Selected by editors"
	summary := "Summary"
	w := itemWire{
		ID: "p1", Title: "Agent tools", Summary: &summary,
		Reason: &reason, Category: &category, Selected: true, PublishedAt: published,
		DiscoveredAt: discovered, Source: sourceWire{Name: "Publisher"},
		Links:       itemLinksWire{AIHOT: "https://aihot.news/read/p1", Original: "https://publisher.example/p1"},
		Attribution: &attributionWire{Name: "AIHOT", URL: "https://aihot.news"},
	}
	e, err := normalizeItem(w)
	if err != nil {
		t.Fatal(err)
	}
	if e.DiscoveredAt != discovered || e.PublishedAt != "" {
		t.Fatalf("times = %q / %q", e.DiscoveredAt, e.PublishedAt)
	}
	if e.Source.Name != "Publisher" || e.Attribution.Name != "AIHOT" || !e.Selected {
		t.Fatalf("entry = %#v", e)
	}
}

func TestNormalizeDailyAndWeeklyPreservesParagraphAttribution(t *testing.T) {
	aihot, original := "https://aihot.news/daily/2026-10-28", "https://publisher.example/item"
	summary := "daily lead"
	daily, err := normalizeDaily(dailyWire{
		Date: "2026-10-28", GeneratedAt: "2026-10-28T08:00:00Z", Links: issueLinksWire{AIHOT: aihot},
		Lead: &dailyLeadWire{Title: "Daily lead", LeadParagraph: summary},
		Sections: []dailySectionWire{{Label: "Models", Items: []articleWire{{
			Title: "Model item", Summary: "item summary", Source: sourceWire{Name: "Publisher"},
			Links:       articleLinksWire{AIHOT: &aihot, Original: original},
			Attribution: &attributionWire{Name: "AIHOT", URL: "https://aihot.news"},
		}}}},
		Flashes: []dailyFlashWire{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if daily.ID != "daily_2026-10-28" || daily.DiscoveredAt != "" || daily.Summary != summary {
		t.Fatalf("daily = %#v", daily)
	}
	if len(daily.Paragraphs) != 1 || daily.Paragraphs[0].Source == nil || daily.Paragraphs[0].Source.Name != "Publisher" || daily.Paragraphs[0].Attribution == nil || daily.Paragraphs[0].Attribution.Name != "AIHOT" {
		t.Fatalf("daily paragraphs = %#v", daily.Paragraphs)
	}
	sectionSummary := "weekly section summary"
	week, err := normalizePeriod("weekly", periodWire{Week: "2026-W44", GeneratedAt: "2026-10-28T08:00:00Z", Links: issueLinksWire{AIHOT: "https://aihot.news/week/2026-W44"},
		Sections: []periodSectionWire{{Label: "Highlights", Summary: &sectionSummary, Items: []articleWire{}}}})
	if err != nil || week.ID != "weekly_2026-W44" || week.Period != "2026-W44" {
		t.Fatalf("weekly = %#v, %v", week, err)
	}
	if len(week.Paragraphs) != 1 || week.Paragraphs[0].Heading != "Highlights" || week.Paragraphs[0].Text != sectionSummary {
		t.Fatalf("weekly section summary = %#v", week.Paragraphs)
	}
}

func TestNormalizeArchiveIndexPreservesUpstreamTitles(t *testing.T) {
	title, lead := "Daily title", "Daily lead"
	daily, err := normalizeDailyArchiveItem(dailyArchiveItemWire{Date: "2026-10-28", GeneratedAt: "2026-10-28T08:00:00Z", LeadTitle: &title, LeadParagraph: &lead,
		Links: issueLinksWire{AIHOT: "https://aihot.news/dailies/2026-10-28"}})
	if err != nil || daily.Title != title || daily.Summary != lead || daily.Period != "2026-10-28" {
		t.Fatalf("daily index = %#v, %v", daily, err)
	}
	weekTitle := "Weekly headline"
	weekly, err := normalizePeriodArchiveItem("weekly", "2026-W44", "2026-10-28T08:00:00Z", &weekTitle,
		issueLinksWire{AIHOT: "https://aihot.news/weeklies/2026-W44"}, nil)
	if err != nil || weekly.Title != weekTitle || weekly.ID != "weekly_2026-W44" {
		t.Fatalf("weekly index = %#v, %v", weekly, err)
	}
}

func TestAppendNoticeAcceptsOnlySingularObjectAndPreservesNumbers(t *testing.T) {
	var raw json.RawMessage = []byte(`{"type":"domain_move","url":"https://aihot.news","extra":9007199254740993}`)
	notices, err := appendNotice(nil, raw)
	if err != nil || len(notices) != 1 {
		t.Fatalf("appendNotice() = %#v, %v", notices, err)
	}
	if notices[0]["extra"].(json.Number).String() != "9007199254740993" {
		t.Fatalf("notice number = %#v", notices[0]["extra"])
	}
	if got, err := appendNotice(notices, json.RawMessage(`null`)); err != nil || len(got) != 1 {
		t.Fatalf("null notice = %#v, %v", got, err)
	}
	if _, err := appendNotice(nil, json.RawMessage(`[]`)); err == nil {
		t.Fatal("array notice accepted")
	}
	if _, err := appendNotice(nil, json.RawMessage(`{"type":"domain_move"} {"extra":true}`)); err == nil {
		t.Fatal("notice with trailing JSON accepted")
	}
}

func TestNormalizePeriodRejectsInvalidPeriodLabels(t *testing.T) {
	if _, err := normalizePeriodArchiveItem("weekly", "2025-W53", "2026-10-28T08:00:00Z", nil, issueLinksWire{AIHOT: "https://aihot.news/weeklies/2025-W53"}, nil); err == nil {
		t.Fatal("nonexistent ISO week accepted")
	}
	if _, err := normalizePeriodArchiveItem("monthly", "2026-13", "2026-10-28T08:00:00Z", nil, issueLinksWire{AIHOT: "https://aihot.news/monthlies/2026-13"}, nil); err == nil {
		t.Fatal("invalid month accepted")
	}
}

func TestNormalizeArticleUsesFirstSafeLinkAndDistinctPublisher(t *testing.T) {
	unsafeOriginal := "javascript:alert(1)"
	aihot := "https://aihot.news/read/item"
	p := normalizeArticle("Models / update", articleWire{
		Title: "update", Summary: "summary", Source: sourceWire{Name: "Publisher"},
		Links:       articleLinksWire{AIHOT: &aihot, Original: unsafeOriginal},
		Attribution: &attributionWire{Name: "AIHOT", URL: "https://aihot.news"},
	})
	if p.Heading != "Models / update" || p.URL != aihot || p.Source == nil || p.Source.Name != "Publisher" || p.Attribution == nil || p.Attribution.Name != "AIHOT" {
		t.Fatalf("paragraph = %#v", p)
	}
}

func TestNormalizeTopicUsesStoryDigestAndReportAttribution(t *testing.T) {
	digest := "story digest"
	original := "https://publisher.example/report"
	entry, err := normalizeTopic(hotTopicWire{
		ID: "story-1", Title: "Hot topic", LatestAt: "2026-10-28T10:00:00Z", Source: sourceWire{Name: "Publisher"},
		Links: topicLinksWire{AIHOT: "https://aihot.news/hot/story-1", Original: "https://publisher.example/topic"},
	}, &storyWire{
		PublicID: "story-1", Title: "Story title", Latest: "latest summary", Digest: &digest,
		Links: issueLinksWire{AIHOT: "https://aihot.news/story/story-1"},
		Reports: []storyReportWire{{Title: "Report", Summary: &digest, Source: storyReportSourceWire{Name: "Publisher"},
			PublishedAt: "2026-10-28T09:00:00Z", Links: storyReportLinksWire{AIHOT: "https://aihot.news/report/1", Original: &original}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "topic_story-1" || entry.Summary != digest || entry.Attribution.Name != "AIHOT" || len(entry.Paragraphs) != 1 {
		t.Fatalf("topic = %#v", entry)
	}
	paragraph := entry.Paragraphs[0]
	if paragraph.Source == nil || paragraph.Source.Name != "Publisher" || paragraph.Attribution == nil || paragraph.Attribution.Name != "AIHOT" || paragraph.URL != original {
		t.Fatalf("story report = %#v", paragraph)
	}
}
