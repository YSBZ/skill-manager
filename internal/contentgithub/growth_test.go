package contentgithub

import (
	"testing"
	"time"

	"skillmanage/internal/content"
)

func TestSevenDayGrowthEndsAtCurrentSourceBoundary(t *testing.T) {
	now := time.Unix(200*86400+12*3600, 0)
	rows := []starHistoryWeek{
		{Week: 189 * 86400, Total: 28, Days: []int{1, 2, 3, 4, 5, 6, 7}},
		{Week: 196 * 86400, Total: 77, Days: []int{8, 9, 10, 11, 12, 13, 14}},
	}

	got := sevenDayGrowth(rows, now)
	wantStart := time.Unix(193*86400, 0).UTC().Format(time.RFC3339)
	wantEnd := time.Unix(200*86400, 0).UTC().Format(time.RFC3339)
	if got == nil || got.Metric != "stars_added" || got.Value != 56 ||
		got.WindowStart != wantStart || got.WindowEnd != wantEnd ||
		got.ObservedDays != 7 || !got.Complete {
		t.Fatalf("growth must cover the latest seven closed source days: got %+v, want value 56, [%s, %s)", got, wantStart, wantEnd)
	}

	if stale := sevenDayGrowth(rows[:1], now); stale != nil {
		t.Fatalf("stale history participated in ranking: %+v", stale)
	}
}

func TestSevenDayGrowthRejectsInvalidHistory(t *testing.T) {
	base := []starHistoryWeek{
		{Week: 189 * 86400, Days: []int{1, 2, 3, 4, 5, 6, 7}},
		{Week: 196 * 86400, Days: []int{8, 9, 10, 11, 12, 13, 14}},
	}
	now := time.Unix(200*86400+12*3600, 0)

	cases := []struct {
		name string
		rows []starHistoryWeek
	}{
		{name: "empty rows"},
		{name: "missing day", rows: []starHistoryWeek{{Week: 196 * 86400, Days: []int{1, 2, 3, 4, 5, 6}}}},
		{name: "negative count", rows: []starHistoryWeek{{Week: 196 * 86400, Days: []int{1, 2, 3, 4, 5, 6, -1}}}},
		{name: "conflicting overlapping day", rows: []starHistoryWeek{
			{Week: 196 * 86400, Days: []int{1, 2, 3, 4, 5, 6, 7}},
			{Week: 196 * 86400, Days: []int{1, 2, 3, 4, 5, 6, 8}},
		}},
		{name: "broken anchor continuity", rows: []starHistoryWeek{
			base[0], {Week: 197 * 86400, Days: []int{8, 9, 10, 11, 12, 13, 14}},
		}},
		{name: "not enough days", rows: []starHistoryWeek{{Week: 196 * 86400, Days: []int{1, 2, 3, 4, 5, 6, 7}}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sevenDayGrowth(tc.rows, now); got != nil {
				t.Fatalf("invalid or incomplete history returned growth: %+v", got)
			}
		})
	}
}

func TestSevenDayGrowthAllowsZeroCounts(t *testing.T) {
	rows := []starHistoryWeek{{Week: 193 * 86400, Days: []int{0, 0, 0, 0, 0, 0, 0}}}
	now := time.Unix(200*86400+12*3600, 0)

	got := sevenDayGrowth(rows, now)
	if got == nil || got.Value != 0 || !got.Complete || got.ObservedDays != 7 {
		t.Fatalf("zero-count closed days must be valid growth: %+v", got)
	}
}

func TestSnapshotGrowthUsesNearestBaselineAtLeastSevenDaysOld(t *testing.T) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	rows := []content.Snapshot{
		{RepositoryID: 4, At: start, Stars: 100},
		{RepositoryID: 4, At: start.Add(7 * 24 * time.Hour), Stars: 110},
		{RepositoryID: 4, At: start.Add(7*24*time.Hour + 10*time.Minute), Stars: 111},
	}

	got := snapshotGrowth(rows, rows[2].At)
	if got == nil || got.Metric != "stars_net" || got.Value != 11 ||
		got.WindowStart != start.Format(time.RFC3339) ||
		got.WindowEnd != rows[2].At.Format(time.RFC3339) ||
		got.ObservedDays != 7 || got.Complete {
		t.Fatalf("wrong baseline or completeness: %+v", got)
	}
}

func TestSnapshotGrowthFallsBackToEarliestObservation(t *testing.T) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	rows := []content.Snapshot{
		{RepositoryID: 4, At: start, Stars: 100},
		{RepositoryID: 4, At: start.Add(2 * 24 * time.Hour), Stars: 105},
		{RepositoryID: 4, At: start.Add(3 * 24 * time.Hour), Stars: 109},
		{RepositoryID: 4, At: start.Add(4 * 24 * time.Hour), Stars: 999}, // future relative to now
	}

	got := snapshotGrowth(rows, rows[2].At)
	if got == nil || got.Value != 9 || got.WindowStart != start.Format(time.RFC3339) ||
		got.WindowEnd != rows[2].At.Format(time.RFC3339) || got.ObservedDays != 3 || got.Complete {
		t.Fatalf("should show the actual short observation interval: %+v", got)
	}
}

func TestSnapshotGrowthMarksOnlyExactlySevenDaysComplete(t *testing.T) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		span     time.Duration
		wantDays int
		wantNet  int
		complete bool
	}{
		{name: "exactly seven days", span: 7 * 24 * time.Hour, wantDays: 7, wantNet: 4, complete: true},
		{name: "eight day decrease", span: 8 * 24 * time.Hour, wantDays: 8, wantNet: -10},
		{name: "seven days plus one second", span: 7*24*time.Hour + time.Second, wantDays: 7, wantNet: 4},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows := []content.Snapshot{
				{RepositoryID: 4, At: start, Stars: 100},
				{RepositoryID: 4, At: start.Add(tc.span), Stars: 100 + tc.wantNet},
			}
			got := snapshotGrowth(rows, rows[1].At)
			if got == nil || got.Value != tc.wantNet || got.ObservedDays != tc.wantDays || got.Complete != tc.complete {
				t.Fatalf("unexpected net growth over %s: %+v", tc.span, got)
			}
		})
	}
}

func TestSevenDayGrowthHonorsNonMidnightSourceBoundary(t *testing.T) {
	const offset = 5*3600 + 30*60
	rows := []starHistoryWeek{
		{Week: 189*86400 + offset, Days: []int{1, 2, 3, 4, 5, 6, 7}},
		{Week: 196*86400 + offset, Days: []int{8, 9, 10, 11, 12, 13, 14}},
	}
	boundary := time.Unix(200*86400+offset, 0)
	for _, tc := range []struct {
		name  string
		now   time.Time
		end   time.Time
		value int
	}{
		{"immediately before", boundary.Add(-time.Second), boundary.Add(-24 * time.Hour), 49},
		{"exactly at", boundary, boundary, 56},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := sevenDayGrowth(rows, tc.now)
			if got == nil || got.WindowEnd != tc.end.UTC().Format(time.RFC3339) || got.WindowStart != tc.end.Add(-7*24*time.Hour).UTC().Format(time.RFC3339) || got.Value != tc.value || !got.Complete {
				t.Fatalf("offset source boundary: %+v", got)
			}
		})
	}
}
