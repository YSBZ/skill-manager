package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"skillmanage/internal/content"
)

func TestPersistenceAndSideStateRoundTrip(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "content.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	entry := content.Entry{
		ID:           "news_1",
		Kind:         "news",
		Title:        "A title",
		Summary:      "A summary",
		DiscoveredAt: "",
		Paragraphs:   []content.Paragraph{{Heading: "Intro", Text: "Body"}},
	}
	if err := s.Upsert(ctx, entry); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceCollection(ctx, "aihot:news:all:7d", []content.Entry{entry}); err != nil {
		t.Fatal(err)
	}

	at := time.Date(2026, 10, 28, 1, 2, 3, 4, time.FixedZone("offset", 2*60*60))
	if err := s.SaveSnapshot(ctx, 42, at, 7); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSnapshot(ctx, 42, at, 9); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSnapshot(ctx, 42, at.Add(100*time.Millisecond), 10); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSnapshot(ctx, 42, at.Add(time.Hour), 12); err != nil {
		t.Fatal(err)
	}

	coverage := content.Coverage{QueryVersion: "v1", Candidates: 12, Updated: 9, CompleteGrowth: 4}
	if err := s.SaveCoverage(ctx, coverage); err != nil {
		t.Fatal(err)
	}
	notices := []map[string]any{{"code": "partial", "count": json.Number("2")}}
	if err := s.SaveNotices(ctx, notices); err != nil {
		t.Fatal(err)
	}
	checkpoint := content.SourceCheckpoint{
		Name:         "aihot:news",
		ETag:         `"etag-1"`,
		LastModified: "Wed, 28 Oct 2026 00:00:00 GMT",
		CacheControl: "max-age=300",
		NextFetchAt:  time.Date(2026, 10, 28, 1, 10, 0, 0, time.UTC),
		FailureCount: 2,
		Status:       content.SourceStatus{Name: "AIHOT", State: "ok", LastSuccessAt: "2026-10-28T00:00:00Z"},
	}
	if err := s.SaveCheckpoint(ctx, checkpoint.Name, checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	gotEntry, err := s.Get(ctx, entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotEntry, entry) {
		t.Fatalf("Get() = %#v, want %#v", gotEntry, entry)
	}
	gotCollection, err := s.Collection(ctx, "aihot:news:all:7d")
	if err != nil || len(gotCollection) != 1 || gotCollection[0].ID != entry.ID {
		t.Fatalf("Collection() = %#v, %v", gotCollection, err)
	}

	snapshots, err := s.Snapshots(ctx, 42)
	if err != nil {
		t.Fatal(err)
	}
	wantSnapshots := []content.Snapshot{
		{RepositoryID: 42, At: at.UTC(), Stars: 9},
		{RepositoryID: 42, At: at.Add(100 * time.Millisecond).UTC(), Stars: 10},
		{RepositoryID: 42, At: at.Add(time.Hour).UTC(), Stars: 12},
	}
	if !reflect.DeepEqual(snapshots, wantSnapshots) {
		t.Fatalf("Snapshots() = %#v, want %#v", snapshots, wantSnapshots)
	}
	gotCoverage, err := s.LoadCoverage(ctx)
	if err != nil || gotCoverage != coverage {
		t.Fatalf("LoadCoverage() = %#v, %v; want %#v", gotCoverage, err, coverage)
	}
	gotNotices, err := s.LoadNotices(ctx)
	if err != nil || !reflect.DeepEqual(gotNotices, notices) {
		t.Fatalf("LoadNotices() = %#v, %v; want %#v", gotNotices, err, notices)
	}
	gotCheckpoints, err := s.Checkpoints(ctx)
	if err != nil || !reflect.DeepEqual(gotCheckpoints, []content.SourceCheckpoint{checkpoint}) {
		t.Fatalf("Checkpoints() = %#v, %v; want %#v", gotCheckpoints, err, checkpoint)
	}
}

func TestResetCalendarSnapshotPersistenceAndFullReplacement(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "reset-calendar.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	date := "2026-10-28"
	snapshot := content.ResetCalendarSnapshot{
		Timezone: "Asia/Shanghai", Today: &date, HistoryFrom: "2026-09-01T00:00:00+08:00",
		Events: []content.ResetCalendarEvent{{ID: "reset-1", Type: "direct_reset", DisplayLabel: "额度重置", Status: "confirmed", OccurredOn: &date, Posts: []content.ResetCalendarPost{}}},
		Meta:   content.ResetCalendarMeta{Stale: true, LastError: "transient"},
	}
	if err := store.SaveResetCalendar(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadResetCalendar(ctx)
	if err != nil || len(got.Events) != 1 || got.Events[0].ID != "reset-1" || got.Meta.Stale {
		t.Fatalf("snapshot round trip = %#v, %v", got, err)
	}
	if err := store.SaveResetCalendar(ctx, content.ResetCalendarSnapshot{Timezone: "Asia/Shanghai", Events: []content.ResetCalendarEvent{}}); err != nil {
		t.Fatal(err)
	}
	got, err = store.LoadResetCalendar(ctx)
	if err != nil || got.Events == nil || len(got.Events) != 0 {
		t.Fatalf("empty full snapshot replacement = %#v, %v", got, err)
	}
}

func TestNoticeUnknownNumbersSurvivePersistenceAndReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "content.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "9007199254740993"
	notices := []map[string]any{{"detail": map[string]any{"count": json.Number(want)}}}
	if err := store.SaveNotices(ctx, notices); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	got, err := store.LoadNotices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	detail, ok := got[0]["detail"].(map[string]any)
	if !ok {
		t.Fatalf("notice detail = %#v, want object", got[0]["detail"])
	}
	number, ok := detail["count"].(json.Number)
	if !ok || number.String() != want {
		t.Fatalf("notice number = %#v, want json.Number(%s)", detail["count"], want)
	}
}

func TestDeleteAndExpiryAreDistinct(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "content.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	news := content.Entry{ID: "news_1", Kind: "news", Title: "t", Paragraphs: []content.Paragraph{}}
	if err := s.Upsert(ctx, news); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceCollection(ctx, "aihot:news:all:7d", []content.Entry{news}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceCollection(ctx, "aihot:news:all:7d", []content.Entry{}); err != nil {
		t.Fatal(err)
	}
	if member, err := s.HasMembership(ctx, news.ID); err != nil || member {
		t.Fatalf("membership after window removal = %v, %v", member, err)
	}
	if _, err := s.Get(ctx, news.ID); err != nil {
		t.Fatalf("window removal must retain cache row: %v", err)
	}

	project := content.Entry{ID: "project_1", Kind: "project", Title: "p"}
	if err := s.Upsert(ctx, project); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Now().Add(time.Second)
	if err := s.ExpireUnreferenced(ctx, cutoff); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, news.ID); !errors.Is(err, content.ErrNotFound) {
		t.Fatalf("expired row = %v, want ErrNotFound", err)
	}
	if withdrawn, err := s.IsWithdrawn(ctx, news.ID); err != nil || withdrawn {
		t.Fatalf("expired row tombstone = %v, %v; want false", withdrawn, err)
	}
	if _, err := s.Get(ctx, project.ID); err != nil {
		t.Fatalf("project must be excluded from expiry: %v", err)
	}

	if err := s.Upsert(ctx, news); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, news.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, news.ID); !errors.Is(err, content.ErrWithdrawn) {
		t.Fatalf("explicit delete = %v, want ErrWithdrawn", err)
	}
	if withdrawn, err := s.IsWithdrawn(ctx, news.ID); err != nil || !withdrawn {
		t.Fatalf("explicit deletion tombstone = %v, %v; want true", withdrawn, err)
	}
}

func TestReplaceCollectionPreservesOrderAndUpdatesPayload(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "content.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	a := content.Entry{ID: "news_a", Kind: "news", Title: "A"}
	b := content.Entry{ID: "news_b", Kind: "news", Title: "B"}
	if err := s.ReplaceCollection(ctx, "feed", []content.Entry{a, b}); err != nil {
		t.Fatal(err)
	}
	b.Title = "B updated"
	if err := s.ReplaceCollection(ctx, "feed", []content.Entry{b, a}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Collection(ctx, "feed")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != b.ID || got[1].ID != a.ID {
		t.Fatalf("Collection() order = %#v", got)
	}
	updated, err := s.Get(ctx, b.ID)
	if err != nil || updated.Title != "B updated" {
		t.Fatalf("Get(%q) = %#v, %v; want updated payload", b.ID, updated, err)
	}
}

func TestReplaceCollectionRollsBackOnMembershipError(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "content.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	old := content.Entry{ID: "news_old", Kind: "news", Title: "old"}
	if err := s.ReplaceCollection(ctx, "feed", []content.Entry{old}); err != nil {
		t.Fatal(err)
	}
	versionBefore, err := s.Version(ctx)
	if err != nil {
		t.Fatal(err)
	}
	newEntry := content.Entry{ID: "news_new", Kind: "news", Title: "first"}
	duplicate := newEntry
	duplicate.Title = "second"
	if err := s.ReplaceCollection(ctx, "feed", []content.Entry{newEntry, duplicate}); err == nil {
		t.Fatal("ReplaceCollection() error = nil, want duplicate membership error")
	}
	if _, err := s.Get(ctx, newEntry.ID); !errors.Is(err, content.ErrNotFound) {
		t.Fatalf("new entry after rollback = %v, want ErrNotFound", err)
	}
	got, err := s.Collection(ctx, "feed")
	if err != nil || len(got) != 1 || got[0].ID != old.ID {
		t.Fatalf("collection after rollback = %#v, %v", got, err)
	}
	versionAfter, err := s.Version(ctx)
	if err != nil || versionAfter != versionBefore {
		t.Fatalf("version after rollback = %d, %v; want %d", versionAfter, err, versionBefore)
	}
}

func TestVersionTracksContentAndMembershipOnly(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "content.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	version := func() int64 {
		t.Helper()
		got, err := s.Version(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	start := version()
	e := content.Entry{ID: "news_1", Kind: "news", Title: "title"}
	if err := s.Upsert(ctx, e); err != nil {
		t.Fatal(err)
	}
	afterInsert := version()
	if afterInsert <= start {
		t.Fatalf("version after insert = %d, want > %d", afterInsert, start)
	}
	oldLastSeen := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano()
	if _, err := s.db.ExecContext(ctx, `UPDATE entries SET last_seen_at = ? WHERE id = ?`, oldLastSeen, e.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(ctx, e); err != nil {
		t.Fatal(err)
	}
	var refreshedLastSeen int64
	if err := s.db.QueryRowContext(ctx, `SELECT last_seen_at FROM entries WHERE id = ?`, e.ID).Scan(&refreshedLastSeen); err != nil {
		t.Fatal(err)
	}
	if refreshedLastSeen <= oldLastSeen {
		t.Fatalf("last_seen_at after identical upsert = %d, want > %d", refreshedLastSeen, oldLastSeen)
	}
	if got := version(); got != afterInsert {
		t.Fatalf("version after timestamp-only upsert = %d, want %d", got, afterInsert)
	}

	snapshotAt := time.Now()
	if err := s.SaveSnapshot(ctx, 1, snapshotAt, 3); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSnapshot(ctx, 1, snapshotAt, 4); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveCoverage(ctx, content.Coverage{Candidates: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveNotices(ctx, []map[string]any{{"notice": "x"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveCheckpoint(ctx, "source", content.SourceCheckpoint{Name: "source"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveCheckpoint(ctx, "source", content.SourceCheckpoint{Name: "source", FailureCount: 1}); err != nil {
		t.Fatal(err)
	}
	if got := version(); got != afterInsert {
		t.Fatalf("version after side state updates = %d, want %d", got, afterInsert)
	}

	e.Title = "updated"
	if err := s.Upsert(ctx, e); err != nil {
		t.Fatal(err)
	}
	afterPayloadChange := version()
	if afterPayloadChange <= afterInsert {
		t.Fatalf("version after payload update = %d, want > %d", afterPayloadChange, afterInsert)
	}
	if err := s.ReplaceCollection(ctx, "feed", []content.Entry{e}); err != nil {
		t.Fatal(err)
	}
	afterMembershipChange := version()
	if afterMembershipChange <= afterPayloadChange {
		t.Fatalf("version after membership change = %d, want > %d", afterMembershipChange, afterPayloadChange)
	}
	if err := s.ReplaceCollection(ctx, "feed", []content.Entry{e}); err != nil {
		t.Fatal(err)
	}
	if got := version(); got != afterMembershipChange {
		t.Fatalf("version after identical collection = %d, want %d", got, afterMembershipChange)
	}
	if err := s.Delete(ctx, e.ID); err != nil {
		t.Fatal(err)
	}
	afterDelete := version()
	if err := s.Delete(ctx, e.ID); err != nil {
		t.Fatal(err)
	}
	if got := version(); got != afterDelete {
		t.Fatalf("version after repeated delete = %d, want %d", got, afterDelete)
	}
}

func TestUpsertNormalizesEmptyCollectionsWithoutMutatingInput(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "content.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	e := content.Entry{ID: "project_1", Kind: "project", Project: &content.Project{}}
	if err := s.Upsert(ctx, e); err != nil {
		t.Fatal(err)
	}
	if e.Paragraphs != nil || e.Project.Scenes != nil || e.Project.Skills != nil {
		t.Fatalf("Upsert mutated input slices: %#v", e)
	}
	got, err := s.Get(ctx, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Paragraphs == nil || got.Project.Scenes == nil || got.Project.Skills == nil {
		t.Fatalf("stored empty collections must be non-nil: %#v", got)
	}
}

func TestListMethodsReturnStableNonNilSlices(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "content.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	for _, e := range []content.Entry{
		{ID: "z", Kind: "news", Title: "z"},
		{ID: "b", Kind: "topic", Title: "b"},
		{ID: "a", Kind: "news", Title: "a"},
	} {
		if err := s.Upsert(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.All(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[0].ID != "a" || all[1].ID != "b" || all[2].ID != "z" {
		t.Fatalf("All() order = %#v", all)
	}
	news, err := s.All(ctx, "news")
	if err != nil || len(news) != 2 || news[0].ID != "a" || news[1].ID != "z" {
		t.Fatalf("All(news) = %#v, %v", news, err)
	}
	empty, err := s.All(ctx, "missing")
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("All(missing) = %#v, %v; want non-nil empty slice", empty, err)
	}
	emptyCollection, err := s.Collection(ctx, "missing")
	if err != nil || emptyCollection == nil || len(emptyCollection) != 0 {
		t.Fatalf("Collection(missing) = %#v, %v; want non-nil empty slice", emptyCollection, err)
	}
}

func TestModelLeaderboardSnapshotsAreIndependentAndPreserveRawPrices(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "content.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	checkpoint := func(category, etag string) content.SourceCheckpoint {
		name := "aihot:model-leaderboard:" + category
		return content.SourceCheckpoint{Name: name, ETag: etag, NextFetchAt: time.Date(2026, 10, 29, 11, 0, 0, 0, time.UTC), Status: content.SourceStatus{Name: name, State: "fresh", LastSuccessAt: "2026-10-29T10:00:00Z"}}
	}
	first := content.ModelLeaderboard{
		Category: content.ModelLeaderboardOverall, Title: "AIHOT大模型排行榜", SourceURL: "https://aihot.news/leaderboard",
		UpdatedLabel: "39 项评测·7 家机构·10/29 14:06 更新", FetchedAt: "2026-10-29T10:00:00Z", Currency: "CNY", PriceUnit: "人民币 / 百万 Token",
		Entries:    []content.ModelLeaderboardEntry{{ID: "alpha", Rank: 1, Name: "Alpha", Provider: "Vendor", ReleasedOn: "2026-10-28", Score: 74.3, CachePrice: "待核验", InputPrice: "¥1.25", OutputPrice: "—", URL: "https://aihot.news/leaderboard/alpha"}},
		PriceNotes: []string{"缓存写入、存储及订阅费用另计。"}, Meta: content.ModelLeaderboardMeta{Stale: true, LastError: "ephemeral"},
	}
	if err := s.SaveModelLeaderboard(ctx, first, checkpoint(content.ModelLeaderboardOverall, `"overall-1"`)); err != nil {
		t.Fatal(err)
	}
	firstLoaded, err := s.LoadModelLeaderboard(ctx, content.ModelLeaderboardOverall)
	if err != nil {
		t.Fatal(err)
	}
	if firstLoaded.Entries[0].CachePrice != "待核验" || firstLoaded.Entries[0].OutputPrice != "—" || firstLoaded.Meta.Stale || firstLoaded.Meta.LastError != "" {
		t.Fatalf("stored source values or ephemeral status changed: %+v", firstLoaded)
	}

	coding := first
	coding.Category = content.ModelLeaderboardCoding
	coding.SourceURL = "https://aihot.news/leaderboard/category/coding"
	coding.Entries = []content.ModelLeaderboardEntry{{ID: "beta", Rank: 1, Name: "Beta", CachePrice: "¥2", InputPrice: "¥3", OutputPrice: "¥4"}}
	if err := s.SaveModelLeaderboard(ctx, coding, checkpoint(content.ModelLeaderboardCoding, `"coding-1"`)); err != nil {
		t.Fatal(err)
	}
	firstAgain, err := s.LoadModelLeaderboard(ctx, content.ModelLeaderboardOverall)
	if err != nil || firstAgain.Entries[0].ID != "alpha" {
		t.Fatalf("saving coding board changed overall board: %+v %v", firstAgain, err)
	}
	checkpoints, err := s.Checkpoints(ctx)
	if err != nil || len(checkpoints) != 2 {
		t.Fatalf("category checkpoints=%+v err=%v, want independent checkpoints", checkpoints, err)
	}
	if _, err := s.LoadModelLeaderboard(ctx, "unlisted"); !errors.Is(err, content.ErrBadQuery) {
		t.Fatalf("unknown category load error=%v", err)
	}
}
