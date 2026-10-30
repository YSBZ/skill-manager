package content

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	catalogReadAttempts = 3
	detailRetryInterval = 30 * time.Minute
)

type DetailLoader func(context.Context, string) (Entry, error)

type Catalog struct {
	store        CatalogStore
	now          func() time.Time
	detailLoader DetailLoader
}

func NewCatalog(store CatalogStore, now func() time.Time) *Catalog {
	if now == nil {
		now = time.Now
	}
	return &Catalog{store: store, now: now}
}

func (c *Catalog) SetDetailLoader(loader DetailLoader) {
	c.detailLoader = loader
}

func (c *Catalog) Feed(ctx context.Context, query Query) (Page, error) {
	q, err := normalizeQuery(query)
	if err != nil {
		return Page{}, err
	}
	if c == nil || c.store == nil {
		return Page{}, ErrUnavailable
	}

	for attempt := 0; attempt < catalogReadAttempts; attempt++ {
		version, err := c.store.Version(ctx)
		if err != nil {
			return Page{}, fmt.Errorf("read content catalog version: %w", err)
		}
		offset := 0
		if q.Cursor != "" {
			offset, err = decodeCursor(q.Cursor, q, version)
			if err != nil {
				return Page{}, err
			}
		}

		entries, err := c.readFeedEntries(ctx, q)
		if err != nil {
			return Page{}, err
		}
		checkpoints, coverage, notices, err := c.readMetaInputs(ctx)
		if err != nil {
			return Page{}, err
		}
		finalVersion, err := c.store.Version(ctx)
		if err != nil {
			return Page{}, fmt.Errorf("confirm content catalog version: %w", err)
		}
		if finalVersion != version {
			continue
		}

		sourceNames := sourceNamesForQuery(q)
		checkpointIndex := indexCheckpoints(checkpoints)
		if !hasCachedSourceData(entries, sourceNames, checkpointIndex) {
			return Page{}, fmt.Errorf("content source %q: %w", sourceNames[0], ErrUnavailable)
		}

		rows := orderFeedEntries(entries, q, c.now())
		rows = filterEntries(rows, q)
		if offset > len(rows) {
			return Page{}, ErrCursorExpired
		}
		end := offset + q.Limit
		if end > len(rows) {
			end = len(rows)
		}
		items := append([]Entry{}, rows[offset:end]...)
		meta := buildMeta(version, checkpoints, coverage, notices, sourceNames, c.now())
		page := Page{Items: items, Meta: meta}
		if end < len(rows) {
			page.NextCursor, err = encodeCursor(q, version, end)
			if err != nil {
				return Page{}, err
			}
		}
		return page, nil
	}
	return Page{}, ErrCursorExpired
}

func (c *Catalog) Entry(ctx context.Context, id string) (EntryResult, error) {
	if err := ValidateID(id); err != nil {
		return EntryResult{}, fmt.Errorf("invalid content entry ID: %w", err)
	}
	if c == nil || c.store == nil {
		return EntryResult{}, ErrUnavailable
	}

	for attempt := 0; attempt < catalogReadAttempts; attempt++ {
		version, err := c.store.Version(ctx)
		if err != nil {
			return EntryResult{}, fmt.Errorf("read content catalog version: %w", err)
		}
		entry, lookupErr := c.readEntry(ctx, id)
		if lookupErr != nil {
			finalVersion, versionErr := c.store.Version(ctx)
			if versionErr != nil {
				return EntryResult{}, fmt.Errorf("confirm content catalog version: %w", versionErr)
			}
			if finalVersion != version {
				continue
			}
			return EntryResult{}, lookupErr
		}

		checkpoints, coverage, notices, err := c.readMetaInputs(ctx)
		if err != nil {
			return EntryResult{}, err
		}
		finalVersion, err := c.store.Version(ctx)
		if err != nil {
			return EntryResult{}, fmt.Errorf("confirm content catalog version: %w", err)
		}
		if finalVersion != version {
			continue
		}
		sources := sourceNamesForEntry(entry, indexCheckpoints(checkpoints))
		return EntryResult{Entry: entry, Meta: buildMeta(version, checkpoints, coverage, notices, sources, c.now())}, nil
	}
	return EntryResult{}, ErrCursorExpired
}

func (c *Catalog) Status(ctx context.Context) (Meta, error) {
	if c == nil || c.store == nil {
		return Meta{}, ErrUnavailable
	}
	for attempt := 0; attempt < catalogReadAttempts; attempt++ {
		version, err := c.store.Version(ctx)
		if err != nil {
			return Meta{}, fmt.Errorf("read content catalog version: %w", err)
		}
		checkpoints, coverage, notices, err := c.readMetaInputs(ctx)
		if err != nil {
			return Meta{}, err
		}
		finalVersion, err := c.store.Version(ctx)
		if err != nil {
			return Meta{}, fmt.Errorf("confirm content catalog version: %w", err)
		}
		if finalVersion != version {
			continue
		}
		return buildMeta(version, checkpoints, coverage, notices, checkpointNames(checkpoints), c.now()), nil
	}
	return Meta{}, ErrCursorExpired
}

// ResetCalendar reads the independent public calendar cache. Its checkpoint
// is not included in ordinary feed metadata, so calendar failures do not mark
// AI news stale.
func (c *Catalog) ResetCalendar(ctx context.Context) (ResetCalendarSnapshot, error) {
	if c == nil || c.store == nil {
		return ResetCalendarSnapshot{}, ErrUnavailable
	}
	calendarStore, ok := c.store.(ResetCalendarStore)
	if !ok {
		return ResetCalendarSnapshot{}, ErrUnavailable
	}
	snapshot, err := calendarStore.LoadResetCalendar(ctx)
	if err != nil {
		return ResetCalendarSnapshot{}, err
	}
	checkpoints, err := c.store.Checkpoints(ctx)
	if err != nil {
		return ResetCalendarSnapshot{}, fmt.Errorf("read reset calendar checkpoint: %w", err)
	}
	for _, checkpoint := range checkpoints {
		if checkpoint.Name != "aihot:codex-resets" {
			continue
		}
		status := sourceStatusForCheckpoint("aihot:codex-resets", checkpoint, true, c.now())
		snapshot.Meta.Stale = status.State != "fresh" || status.LastSuccessAt == ""
		if snapshot.Meta.Stale {
			snapshot.Meta.LastError = "日历数据同步失败，正在稍后重试。"
		}
		return snapshot, nil
	}
	snapshot.Meta.Stale = true
	snapshot.Meta.LastError = "日历数据同步状态暂不可用。"
	return snapshot, nil
}

// ModelLeaderboard returns a cached category snapshot with freshness derived
// from its independent source checkpoint. It does not add leaderboard state
// to the ordinary news status response.
func (c *Catalog) ModelLeaderboard(ctx context.Context, category string) (ModelLeaderboard, error) {
	category, err := NormalizeModelLeaderboardCategory(category)
	if err != nil {
		return ModelLeaderboard{}, ErrBadQuery
	}
	if c == nil || c.store == nil {
		return ModelLeaderboard{}, ErrUnavailable
	}
	store, ok := c.store.(ModelLeaderboardStore)
	if !ok {
		return ModelLeaderboard{}, ErrUnavailable
	}
	snapshot, err := store.LoadModelLeaderboard(ctx, category)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return ModelLeaderboard{}, ErrUnavailable
		}
		return ModelLeaderboard{}, fmt.Errorf("read model leaderboard %q: %w", category, err)
	}
	checkpoints, err := c.store.Checkpoints(ctx)
	if err != nil {
		return ModelLeaderboard{}, fmt.Errorf("read model leaderboard checkpoint: %w", err)
	}
	checkpointName := "aihot:model-leaderboard:" + category
	checkpoint, exists := findCheckpoint(checkpoints, checkpointName)
	status := sourceStatusForCheckpoint(checkpointName, checkpoint, exists, c.now())
	snapshot.Meta.Stale = !exists || status.State != "fresh"
	if !exists || checkpoint.Status.State != "fresh" {
		snapshot.Meta.LastError = "模型榜同步失败，当前显示缓存数据。"
	} else {
		snapshot.Meta.LastError = ""
	}
	return snapshot, nil
}

func (c *Catalog) readFeedEntries(ctx context.Context, q Query) ([]Entry, error) {
	if q.Kind == "project" {
		entries, err := c.store.All(ctx, "project")
		if err != nil {
			return nil, fmt.Errorf("read project catalog: %w", err)
		}
		active := make([]Entry, 0, len(entries))
		for _, entry := range entries {
			if entry.Project != nil && !entry.Project.Inactive {
				active = append(active, entry)
			}
		}
		return active, nil
	}
	name := sourceNamesForQuery(q)[0]
	entries, err := c.store.Collection(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("read content collection %q: %w", name, err)
	}
	if entries == nil {
		entries = []Entry{}
	}
	return entries, nil
}

func (c *Catalog) readMetaInputs(ctx context.Context) ([]SourceCheckpoint, Coverage, []map[string]any, error) {
	checkpoints, err := c.store.Checkpoints(ctx)
	if err != nil {
		return nil, Coverage{}, nil, fmt.Errorf("read content source checkpoints: %w", err)
	}
	coverage, err := c.store.LoadCoverage(ctx)
	if err != nil {
		return nil, Coverage{}, nil, fmt.Errorf("read content coverage: %w", err)
	}
	notices, err := c.store.LoadNotices(ctx)
	if err != nil {
		return nil, Coverage{}, nil, fmt.Errorf("read content notices: %w", err)
	}
	if checkpoints == nil {
		checkpoints = []SourceCheckpoint{}
	}
	if notices == nil {
		notices = []map[string]any{}
	}
	return checkpoints, coverage, notices, nil
}

func (c *Catalog) readEntry(ctx context.Context, id string) (Entry, error) {
	entry, err := c.store.Get(ctx, id)
	if err != nil {
		return Entry{}, err
	}
	if entry.Kind == "project" && entry.Project != nil && entry.Project.Inactive {
		return Entry{}, fmt.Errorf("content entry %q: %w", id, ErrNotFound)
	}
	if entry.Kind != "project" {
		member, err := c.store.HasMembership(ctx, id)
		if err != nil {
			return Entry{}, fmt.Errorf("check content membership: %w", err)
		}
		if !member {
			withdrawn, err := c.store.IsWithdrawn(ctx, id)
			if err != nil {
				return Entry{}, fmt.Errorf("check content withdrawal: %w", err)
			}
			if withdrawn {
				return Entry{}, fmt.Errorf("content entry %q: %w", id, ErrWithdrawn)
			}
			return Entry{}, fmt.Errorf("content entry %q: %w", id, ErrNotFound)
		}
	}
	if isReportKind(entry.Kind) && c.detailLoader != nil {
		return c.loadReportDetail(ctx, entry)
	}
	return entry, nil
}

func (c *Catalog) loadReportDetail(ctx context.Context, cached Entry) (Entry, error) {
	name := detailCheckpointName(cached.ID)
	checkpoints, err := c.store.Checkpoints(ctx)
	if err != nil {
		return Entry{}, fmt.Errorf("read report detail checkpoint: %w", err)
	}
	if checkpoint, ok := findCheckpoint(checkpoints, name); ok && checkpoint.NextFetchAt.After(c.now()) {
		return cached, nil
	}

	loaded, loadErr := c.detailLoader(ctx, cached.ID)
	if errors.Is(loadErr, ErrWithdrawn) {
		return Entry{}, loadErr
	}
	if loadErr != nil {
		if err := c.markDetailStale(ctx, name, c.now()); err != nil {
			return Entry{}, fmt.Errorf("record stale report detail: %w", err)
		}
		return cached, nil
	}
	if loaded.ID != cached.ID || loaded.Kind != cached.Kind {
		if err := c.markDetailStale(ctx, name, c.now()); err != nil {
			return Entry{}, fmt.Errorf("record invalid report detail: %w", err)
		}
		return cached, nil
	}
	if loaded.Paragraphs == nil {
		loaded.Paragraphs = []Paragraph{}
	}
	if err := c.store.Upsert(ctx, loaded); err != nil {
		return Entry{}, fmt.Errorf("store report detail %q: %w", loaded.ID, err)
	}
	if err := c.markDetailFresh(ctx, name, c.now()); err != nil {
		return Entry{}, fmt.Errorf("record fresh report detail: %w", err)
	}
	return loaded, nil
}

func (c *Catalog) markDetailStale(ctx context.Context, name string, now time.Time) error {
	checkpoints, err := c.store.Checkpoints(ctx)
	if err != nil {
		return err
	}
	checkpoint, ok := findCheckpoint(checkpoints, name)
	checkpoint.Name = name
	checkpoint.Status.Name = name
	if !ok || checkpoint.Status.State != "stale" {
		checkpoint.FailureCount++
	}
	checkpoint.Status.State = "stale"
	if checkpoint.NextFetchAt.IsZero() || !checkpoint.NextFetchAt.After(now) {
		checkpoint.NextFetchAt = now.Add(detailRetryInterval)
		checkpoint.Status.RetryAt = checkpoint.NextFetchAt.UTC().Format(time.RFC3339)
	}
	return c.store.SaveCheckpoint(ctx, name, checkpoint)
}

func (c *Catalog) markDetailFresh(ctx context.Context, name string, now time.Time) error {
	checkpoints, err := c.store.Checkpoints(ctx)
	if err != nil {
		return err
	}
	checkpoint, _ := findCheckpoint(checkpoints, name)
	checkpoint.Name = name
	checkpoint.Status.Name = name
	checkpoint.Status.State = "fresh"
	checkpoint.Status.LastSuccessAt = now.UTC().Format(time.RFC3339)
	checkpoint.Status.RetryAt = ""
	checkpoint.FailureCount = 0
	if checkpoint.NextFetchAt.IsZero() || !checkpoint.NextFetchAt.After(now) {
		checkpoint.NextFetchAt = now.Add(detailRetryInterval)
	}
	return c.store.SaveCheckpoint(ctx, name, checkpoint)
}

func sourceNamesForQuery(q Query) []string {
	switch q.Kind {
	case "news":
		return []string{fmt.Sprintf("aihot:news:%s:%s", q.View, q.Window)}
	case "topic":
		return []string{"aihot:topics"}
	case "daily", "weekly", "monthly":
		return []string{"aihot:" + q.Kind}
	case "project":
		return []string{"github"}
	default:
		return []string{}
	}
}

func sourceNamesForEntry(entry Entry, checkpoints map[string]SourceCheckpoint) []string {
	switch entry.Kind {
	case "project":
		return []string{"github"}
	case "news":
		view := "all"
		if entry.Selected {
			view = "selected"
		}
		candidates := []string{"aihot:news:" + view + ":24h", "aihot:news:" + view + ":7d"}
		available := make([]string, 0, len(candidates))
		for _, name := range candidates {
			if _, ok := checkpoints[name]; ok {
				available = append(available, name)
			}
		}
		if len(available) == 0 {
			return []string{"aihot:news:" + view + ":7d"}
		}
		sort.SliceStable(available, func(i, j int) bool {
			return checkpointSuccessTime(checkpoints[available[i]]).After(checkpointSuccessTime(checkpoints[available[j]]))
		})
		return []string{available[0]}
	case "topic":
		return []string{"aihot:topics"}
	case "daily", "weekly", "monthly":
		return []string{"aihot:" + entry.Kind, detailCheckpointName(entry.ID)}
	default:
		return []string{"aihot:" + entry.Kind}
	}
}

func hasCachedSourceData(entries []Entry, sourceNames []string, checkpoints map[string]SourceCheckpoint) bool {
	if len(entries) > 0 {
		return true
	}
	for _, name := range sourceNames {
		if checkpoint, ok := checkpoints[name]; ok && checkpoint.Status.LastSuccessAt != "" {
			return true
		}
	}
	return false
}

func indexCheckpoints(checkpoints []SourceCheckpoint) map[string]SourceCheckpoint {
	indexed := make(map[string]SourceCheckpoint, len(checkpoints))
	for _, checkpoint := range checkpoints {
		name := checkpoint.Name
		if name == "" {
			name = checkpoint.Status.Name
		}
		if strings.HasPrefix(name, "aihot:model-leaderboard:") {
			continue
		}
		if name != "" {
			checkpoint.Name = name
			indexed[name] = checkpoint
		}
	}
	return indexed
}

func checkpointNames(checkpoints []SourceCheckpoint) []string {
	names := make([]string, 0, len(checkpoints))
	for _, checkpoint := range checkpoints {
		name := checkpoint.Name
		if name == "" {
			name = checkpoint.Status.Name
		}
		if strings.HasPrefix(name, "aihot:model-leaderboard:") {
			continue
		}
		if name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return uniqueStrings(names)
}

func buildMeta(version int64, checkpoints []SourceCheckpoint, coverage Coverage, notices []map[string]any, sourceNames []string, now time.Time) Meta {
	indexed := indexCheckpoints(checkpoints)
	if notices == nil {
		notices = []map[string]any{}
	}
	names := append([]string{}, sourceNames...)
	sort.Strings(names)
	names = uniqueStrings(names)
	sources := make([]SourceStatus, 0, len(names))
	for _, name := range names {
		checkpoint, ok := indexed[name]
		sources = append(sources, sourceStatusForCheckpoint(name, checkpoint, ok, now))
	}

	meta := Meta{
		Version:  strconv.FormatInt(version, 10),
		Coverage: coverage,
		Sources:  sources,
		Notices:  notices,
	}
	var latestFetch time.Time
	var latestFetchString string
	var latestUpdate time.Time
	var latestUpdateString string
	for _, status := range sources {
		if status.State != "fresh" {
			meta.Stale = true
		}
		if parsed, ok := parseTimestamp(status.LastSuccessAt); ok && parsed.After(latestFetch) {
			latestFetch = parsed
			latestFetchString = status.LastSuccessAt
		}
		if checkpoint, ok := indexed[status.Name]; ok {
			if parsed, err := http.ParseTime(checkpoint.LastModified); err == nil && parsed.After(latestUpdate) {
				latestUpdate = parsed
				latestUpdateString = parsed.UTC().Format(time.RFC3339)
			}
		}
	}
	meta.FetchedAt = latestFetchString
	meta.SourceUpdatedAt = latestUpdateString
	return meta
}

func sourceStatusForCheckpoint(name string, checkpoint SourceCheckpoint, exists bool, now time.Time) SourceStatus {
	if !exists {
		return SourceStatus{Name: name, State: "unavailable"}
	}
	status := checkpoint.Status
	status.Name = name
	if status.State == "" {
		status.State = "unavailable"
	}
	if status.State == "fresh" && !checkpoint.NextFetchAt.IsZero() && !checkpoint.NextFetchAt.After(now) {
		status.State = "stale"
	}
	return status
}

func findCheckpoint(checkpoints []SourceCheckpoint, name string) (SourceCheckpoint, bool) {
	for _, checkpoint := range checkpoints {
		if checkpoint.Name == name || (checkpoint.Name == "" && checkpoint.Status.Name == name) {
			return checkpoint, true
		}
	}
	return SourceCheckpoint{}, false
}

func detailCheckpointName(id string) string { return "aihot:detail:" + id }

func isReportKind(kind string) bool { return kind == "daily" || kind == "weekly" || kind == "monthly" }

func checkpointSuccessTime(checkpoint SourceCheckpoint) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, checkpoint.Status.LastSuccessAt)
	return parsed
}

func parseTimestamp(value string) (time.Time, bool) {
	if value == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return parsed, err == nil
}

func uniqueStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	out := values[:1]
	for _, value := range values[1:] {
		if value != out[len(out)-1] {
			out = append(out, value)
		}
	}
	return out
}

func orderFeedEntries(entries []Entry, q Query, now time.Time) []Entry {
	if q.Kind != "project" {
		return entries
	}
	switch q.View {
	case "weekly":
		return weeklyProjects(entries)
	case "new":
		return newProjects(entries, now)
	case "scenarios":
		out := make([]Entry, 0, len(entries))
		for _, entry := range entries {
			if entry.Project != nil {
				out = append(out, entry)
			}
		}
		sort.SliceStable(out, func(i, j int) bool {
			if out[i].Project.Repo != out[j].Project.Repo {
				return out[i].Project.Repo < out[j].Project.Repo
			}
			if out[i].Project.RepositoryID != out[j].Project.RepositoryID {
				return out[i].Project.RepositoryID < out[j].Project.RepositoryID
			}
			return out[i].ID < out[j].ID
		})
		return out
	default:
		return []Entry{}
	}
}

type growthWindow struct {
	start time.Time
	end   time.Time
}

func weeklyProjects(entries []Entry) []Entry {
	if added := weeklyProjectsByMetric(entries, "stars_added"); len(added) > 0 {
		return added
	}
	return weeklyProjectsByMetric(entries, "stars_net")
}

func weeklyProjectsByMetric(entries []Entry, metric string) []Entry {
	eligible := make([]Entry, 0, len(entries))
	var latest growthWindow
	hasLatest := false
	for _, entry := range entries {
		if entry.Project == nil || entry.Project.Growth == nil {
			continue
		}
		growth := entry.Project.Growth
		if !growth.Complete || growth.Metric != metric || growth.ObservedDays != 7 {
			continue
		}
		start, startOK := parseTimestamp(growth.WindowStart)
		end, endOK := parseTimestamp(growth.WindowEnd)
		if !startOK || !endOK || !end.After(start) {
			continue
		}
		window := growthWindow{start: start, end: end}
		eligible = append(eligible, entry)
		if !hasLatest || window.end.After(latest.end) || (window.end.Equal(latest.end) && window.start.After(latest.start)) {
			latest, hasLatest = window, true
		}
	}
	if !hasLatest {
		return []Entry{}
	}
	out := make([]Entry, 0, len(eligible))
	for _, entry := range eligible {
		growth := entry.Project.Growth
		start, _ := parseTimestamp(growth.WindowStart)
		end, _ := parseTimestamp(growth.WindowEnd)
		if start.Equal(latest.start) && end.Equal(latest.end) {
			out = append(out, entry)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		left, right := out[i].Project.Growth, out[j].Project.Growth
		if left.Value != right.Value {
			return left.Value > right.Value
		}
		if out[i].Project.RepositoryID != out[j].Project.RepositoryID {
			return out[i].Project.RepositoryID < out[j].Project.RepositoryID
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func newProjects(entries []Entry, now time.Time) []Entry {
	cutoff := now.Add(-30 * 24 * time.Hour)
	eligible := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		if entry.Project == nil || entry.Project.Growth == nil {
			continue
		}
		created, ok := parseTimestamp(entry.Project.CreatedAt)
		if !ok || created.Before(cutoff) || created.After(now) {
			continue
		}
		eligible = append(eligible, entry)
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		left, _ := parseTimestamp(eligible[i].Project.CreatedAt)
		right, _ := parseTimestamp(eligible[j].Project.CreatedAt)
		if !left.Equal(right) {
			return left.After(right)
		}
		if eligible[i].Project.RepositoryID != eligible[j].Project.RepositoryID {
			return eligible[i].Project.RepositoryID < eligible[j].Project.RepositoryID
		}
		return eligible[i].ID < eligible[j].ID
	})
	return eligible
}

func filterEntries(entries []Entry, q Query) []Entry {
	filtered := make([]Entry, 0, len(entries))
	needle := strings.ToLower(q.Q)
	for _, entry := range entries {
		if q.Category != "" && entry.Category != q.Category {
			continue
		}
		if q.Scene != "" && !entryHasScene(entry, q.Scene) {
			continue
		}
		if needle != "" && !entryMatches(entry, needle) {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func entryHasScene(entry Entry, scene string) bool {
	if entry.Project == nil {
		return false
	}
	for _, candidate := range entry.Project.Scenes {
		if strings.EqualFold(candidate, scene) {
			return true
		}
	}
	return false
}

func entryMatches(entry Entry, needle string) bool {
	fields := []string{entry.ID, entry.Title, entry.Summary, entry.Reason, entry.Category, entry.Source.Name, entry.Attribution.Name}
	if entry.Project != nil {
		fields = append(fields, entry.Project.Repo, entry.Project.Type)
		fields = append(fields, entry.Project.Scenes...)
		for _, skill := range entry.Project.Skills {
			fields = append(fields, skill.Repo, skill.Path, skill.Name)
		}
	}
	for _, field := range fields {
		if strings.Contains(strings.ToLower(field), needle) {
			return true
		}
	}
	return false
}
