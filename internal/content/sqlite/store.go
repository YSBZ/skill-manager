package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"skillmanage/internal/content"

	_ "modernc.org/sqlite"
)

const versionKey = "catalog_version"

// Store persists content entries and their catalog state in SQLite.
type Store struct {
	db *sql.DB
}

// Open opens or creates a content database at path.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open content database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	cleanup := func(err error) (*Store, error) {
		_ = db.Close()
		return nil, err
	}
	if err := db.Ping(); err != nil {
		return cleanup(fmt.Errorf("connect to content database: %w", err))
	}
	for _, pragma := range []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA foreign_keys=ON`,
		`PRAGMA busy_timeout=5000`,
	} {
		if _, err := db.Exec(pragma); err != nil {
			return cleanup(fmt.Errorf("configure content database: %w", err))
		}
	}
	if err := initSchema(context.Background(), db); err != nil {
		return cleanup(fmt.Errorf("initialize content database: %w", err))
	}
	return &Store{db: db}, nil
}

// Close releases the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

func initSchema(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	statements := []string{
		`CREATE TABLE IF NOT EXISTS entries (
			id TEXT PRIMARY KEY,
			kind TEXT NOT NULL,
			payload BLOB NOT NULL,
			last_seen_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS collections (
			name TEXT NOT NULL,
			position INTEGER NOT NULL,
			entry_id TEXT NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
			PRIMARY KEY (name, entry_id),
			UNIQUE (name, position)
		)`,
		`CREATE INDEX IF NOT EXISTS collections_by_entry ON collections(entry_id)`,
		`CREATE TABLE IF NOT EXISTS withdrawals (
			id TEXT PRIMARY KEY,
			withdrawn_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS star_snapshots (
			repo_id INTEGER NOT NULL,
			at INTEGER NOT NULL,
			stars INTEGER NOT NULL,
			PRIMARY KEY (repo_id, at)
		)`,
		`CREATE TABLE IF NOT EXISTS checkpoints (
			name TEXT PRIMARY KEY,
			payload BLOB NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS model_leaderboards (
			category TEXT PRIMARY KEY,
			payload BLOB NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS content_metadata (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Upsert(ctx context.Context, entry content.Entry) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin upsert content entry: %w", err)
	}
	defer tx.Rollback()

	changed, err := upsertEntry(ctx, tx, entry)
	if err != nil {
		return fmt.Errorf("upsert content entry %q: %w", entry.ID, err)
	}
	if changed {
		if err := incrementVersion(ctx, tx); err != nil {
			return fmt.Errorf("update content version: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit content entry: %w", err)
	}
	return nil
}

func upsertEntry(ctx context.Context, tx *sql.Tx, entry content.Entry) (bool, error) {
	entry = normalizeEntry(entry)
	payload, err := json.Marshal(entry)
	if err != nil {
		return false, fmt.Errorf("encode entry: %w", err)
	}

	var previous []byte
	err = tx.QueryRowContext(ctx, `SELECT payload FROM entries WHERE id = ?`, entry.ID).Scan(&previous)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}

	now := time.Now().UTC().UnixNano()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO entries (id, kind, payload, last_seen_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			kind = excluded.kind,
			payload = excluded.payload,
			last_seen_at = excluded.last_seen_at
	`, entry.ID, entry.Kind, payload, now); err != nil {
		return false, err
	}

	result, err := tx.ExecContext(ctx, `DELETE FROM withdrawals WHERE id = ?`, entry.ID)
	if err != nil {
		return false, err
	}
	withdrawalRemoved, err := rowsChanged(result)
	if err != nil {
		return false, err
	}
	return !exists || !bytes.Equal(previous, payload) || withdrawalRemoved > 0, nil
}

func normalizeEntry(entry content.Entry) content.Entry {
	if entry.Paragraphs == nil {
		entry.Paragraphs = []content.Paragraph{}
	}
	if entry.Project != nil {
		project := *entry.Project
		if project.Scenes == nil {
			project.Scenes = []string{}
		}
		if project.Skills == nil {
			project.Skills = []content.SkillRef{}
		}
		entry.Project = &project
	}
	return entry
}

func (s *Store) Delete(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin delete content entry: %w", err)
	}
	defer tx.Rollback()

	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM entries WHERE id = ?)`, id).Scan(&exists); err != nil {
		return fmt.Errorf("check content entry: %w", err)
	}
	var membershipExists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM collections WHERE entry_id = ?)`, id).Scan(&membershipExists); err != nil {
		return fmt.Errorf("check content membership: %w", err)
	}
	var withdrawn bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM withdrawals WHERE id = ?)`, id).Scan(&withdrawn); err != nil {
		return fmt.Errorf("check content withdrawal: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM collections WHERE entry_id = ?`, id); err != nil {
		return fmt.Errorf("remove content memberships: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM entries WHERE id = ?`, id); err != nil {
		return fmt.Errorf("remove content entry: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO withdrawals (id, withdrawn_at) VALUES (?, ?)
		ON CONFLICT(id) DO UPDATE SET withdrawn_at = excluded.withdrawn_at
	`, id, time.Now().UTC().UnixNano()); err != nil {
		return fmt.Errorf("record content withdrawal: %w", err)
	}
	if exists || membershipExists || !withdrawn {
		if err := incrementVersion(ctx, tx); err != nil {
			return fmt.Errorf("update content version: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit content deletion: %w", err)
	}
	return nil
}

func (s *Store) Get(ctx context.Context, id string) (content.Entry, error) {
	var payload []byte
	err := s.db.QueryRowContext(ctx, `SELECT payload FROM entries WHERE id = ?`, id).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		withdrawn, checkErr := s.IsWithdrawn(ctx, id)
		if checkErr != nil {
			return content.Entry{}, fmt.Errorf("check content withdrawal: %w", checkErr)
		}
		if withdrawn {
			return content.Entry{}, fmt.Errorf("content entry %q: %w", id, content.ErrWithdrawn)
		}
		return content.Entry{}, fmt.Errorf("content entry %q: %w", id, content.ErrNotFound)
	}
	if err != nil {
		return content.Entry{}, fmt.Errorf("read content entry %q: %w", id, err)
	}
	var entry content.Entry
	if err := json.Unmarshal(payload, &entry); err != nil {
		return content.Entry{}, fmt.Errorf("decode content entry %q: %w", id, err)
	}
	return entry, nil
}

func (s *Store) All(ctx context.Context, kind string) ([]content.Entry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT payload FROM entries
		WHERE ? = '' OR kind = ?
		ORDER BY id COLLATE BINARY
	`, kind, kind)
	if err != nil {
		return nil, fmt.Errorf("list content entries: %w", err)
	}
	defer rows.Close()

	entries := make([]content.Entry, 0)
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, fmt.Errorf("read content entry: %w", err)
		}
		var entry content.Entry
		if err := json.Unmarshal(payload, &entry); err != nil {
			return nil, fmt.Errorf("decode content entry: %w", err)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate content entries: %w", err)
	}
	return entries, nil
}

func (s *Store) ReplaceCollection(ctx context.Context, name string, entries []content.Entry) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin replace collection %q: %w", name, err)
	}
	defer tx.Rollback()

	var oldIDs []string
	rows, err := tx.QueryContext(ctx, `SELECT entry_id FROM collections WHERE name = ? ORDER BY position`, name)
	if err != nil {
		return fmt.Errorf("read collection %q: %w", name, err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("read collection %q membership: %w", name, err)
		}
		oldIDs = append(oldIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate collection %q: %w", name, err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close collection %q rows: %w", name, err)
	}

	changed := !sameIDs(oldIDs, entryIDs(entries))
	for _, entry := range entries {
		entryChanged, err := upsertEntry(ctx, tx, entry)
		if err != nil {
			return fmt.Errorf("upsert collection entry %q: %w", entry.ID, err)
		}
		changed = changed || entryChanged
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM collections WHERE name = ?`, name); err != nil {
		return fmt.Errorf("clear collection %q: %w", name, err)
	}
	for index, entry := range entries {
		if _, err := tx.ExecContext(ctx, `INSERT INTO collections (name, position, entry_id) VALUES (?, ?, ?)`, name, index, entry.ID); err != nil {
			return fmt.Errorf("insert collection %q member %q: %w", name, entry.ID, err)
		}
	}
	if changed {
		if err := incrementVersion(ctx, tx); err != nil {
			return fmt.Errorf("update content version: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit collection %q: %w", name, err)
	}
	return nil
}

func entryIDs(entries []content.Entry) []string {
	ids := make([]string, len(entries))
	for index := range entries {
		ids[index] = entries[index].ID
	}
	return ids
}

func sameIDs(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func (s *Store) Collection(ctx context.Context, name string) ([]content.Entry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.payload
		FROM collections c
		JOIN entries e ON e.id = c.entry_id
		WHERE c.name = ?
		ORDER BY c.position, c.entry_id COLLATE BINARY
	`, name)
	if err != nil {
		return nil, fmt.Errorf("read collection %q: %w", name, err)
	}
	defer rows.Close()

	entries := make([]content.Entry, 0)
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, fmt.Errorf("read collection %q entry: %w", name, err)
		}
		var entry content.Entry
		if err := json.Unmarshal(payload, &entry); err != nil {
			return nil, fmt.Errorf("decode collection %q entry: %w", name, err)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate collection %q: %w", name, err)
	}
	return entries, nil
}

func (s *Store) HasMembership(ctx context.Context, id string) (bool, error) {
	var exists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM collections WHERE entry_id = ?)`, id).Scan(&exists); err != nil {
		return false, fmt.Errorf("check content membership: %w", err)
	}
	return exists, nil
}

func (s *Store) ExpireUnreferenced(ctx context.Context, cutoff time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin content expiry: %w", err)
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, `
		DELETE FROM entries
		WHERE kind <> 'project'
		  AND last_seen_at < ?
		  AND NOT EXISTS (SELECT 1 FROM collections WHERE collections.entry_id = entries.id)
	`, cutoff.UTC().UnixNano())
	if err != nil {
		return fmt.Errorf("expire content entries: %w", err)
	}
	changed, err := rowsChanged(result)
	if err != nil {
		return fmt.Errorf("count expired content entries: %w", err)
	}
	if changed > 0 {
		if err := incrementVersion(ctx, tx); err != nil {
			return fmt.Errorf("update content version: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit content expiry: %w", err)
	}
	return nil
}

func (s *Store) IsWithdrawn(ctx context.Context, id string) (bool, error) {
	var withdrawn bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM withdrawals WHERE id = ?)`, id).Scan(&withdrawn); err != nil {
		return false, fmt.Errorf("check content withdrawal: %w", err)
	}
	return withdrawn, nil
}

func (s *Store) Version(ctx context.Context) (int64, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM content_metadata WHERE key = ?`, versionKey).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read content version: %w", err)
	}
	version, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse content version: %w", err)
	}
	return version, nil
}

func incrementVersion(ctx context.Context, tx *sql.Tx) error {
	var value string
	err := tx.QueryRowContext(ctx, `SELECT value FROM content_metadata WHERE key = ?`, versionKey).Scan(&value)
	version := int64(0)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		version, err = strconv.ParseInt(value, 10, 64)
		if err != nil {
			return fmt.Errorf("parse content version: %w", err)
		}
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO content_metadata (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value
	`, versionKey, strconv.FormatInt(version+1, 10))
	return err
}

func rowsChanged(result sql.Result) (int64, error) {
	return result.RowsAffected()
}

func (s *Store) SaveSnapshot(ctx context.Context, repositoryID int64, at time.Time, stars int) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO star_snapshots (repo_id, at, stars) VALUES (?, ?, ?)
		ON CONFLICT(repo_id, at) DO UPDATE SET stars = excluded.stars
	`, repositoryID, at.UTC().UnixNano(), stars)
	if err != nil {
		return fmt.Errorf("save star snapshot: %w", err)
	}
	return nil
}

func (s *Store) Snapshots(ctx context.Context, repositoryID int64) ([]content.Snapshot, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT repo_id, at, stars FROM star_snapshots WHERE repo_id = ? ORDER BY at ASC`, repositoryID)
	if err != nil {
		return nil, fmt.Errorf("read star snapshots: %w", err)
	}
	defer rows.Close()

	snapshots := make([]content.Snapshot, 0)
	for rows.Next() {
		var row content.Snapshot
		var at int64
		if err := rows.Scan(&row.RepositoryID, &at, &row.Stars); err != nil {
			return nil, fmt.Errorf("read star snapshot: %w", err)
		}
		row.At = time.Unix(0, at).UTC()
		snapshots = append(snapshots, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate star snapshots: %w", err)
	}
	return snapshots, nil
}

func (s *Store) SaveCoverage(ctx context.Context, coverage content.Coverage) error {
	return s.saveMetadata(ctx, "coverage", coverage)
}

func (s *Store) LoadCoverage(ctx context.Context) (content.Coverage, error) {
	var coverage content.Coverage
	err := s.loadMetadata(ctx, "coverage", &coverage)
	if errors.Is(err, sql.ErrNoRows) {
		return content.Coverage{}, nil
	}
	if err != nil {
		return content.Coverage{}, fmt.Errorf("load content coverage: %w", err)
	}
	return coverage, nil
}

func (s *Store) SaveNotices(ctx context.Context, notices []map[string]any) error {
	if notices == nil {
		notices = []map[string]any{}
	}
	return s.saveMetadata(ctx, "notices", notices)
}

func (s *Store) LoadNotices(ctx context.Context) ([]map[string]any, error) {
	var notices []map[string]any
	err := s.loadMetadata(ctx, "notices", &notices)
	if errors.Is(err, sql.ErrNoRows) {
		return []map[string]any{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load content notices: %w", err)
	}
	if notices == nil {
		notices = []map[string]any{}
	}
	return notices, nil
}

func (s *Store) SaveResetCalendar(ctx context.Context, snapshot content.ResetCalendarSnapshot) error {
	// Refresh metadata is deliberately kept out of the stored upstream snapshot;
	// the catalog combines it from the calendar-only source checkpoint.
	snapshot.Meta = content.ResetCalendarMeta{}
	return s.saveMetadata(ctx, "codex_reset_calendar", snapshot)
}

func (s *Store) LoadResetCalendar(ctx context.Context) (content.ResetCalendarSnapshot, error) {
	var snapshot content.ResetCalendarSnapshot
	if err := s.loadMetadata(ctx, "codex_reset_calendar", &snapshot); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return content.ResetCalendarSnapshot{}, content.ErrUnavailable
		}
		return content.ResetCalendarSnapshot{}, fmt.Errorf("load reset calendar: %w", err)
	}
	if snapshot.Events == nil {
		snapshot.Events = []content.ResetCalendarEvent{}
	}
	return snapshot, nil
}

func (s *Store) SaveModelLeaderboard(ctx context.Context, snapshot content.ModelLeaderboard, checkpoint content.SourceCheckpoint) error {
	category, err := content.NormalizeModelLeaderboardCategory(snapshot.Category)
	if err != nil || category != snapshot.Category {
		return content.ErrBadQuery
	}
	checkpointName := "aihot:model-leaderboard:" + category
	if checkpoint.Name != checkpointName {
		return content.ErrBadQuery
	}
	snapshot.Meta = content.ModelLeaderboardMeta{}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("encode model leaderboard %q: %w", category, err)
	}
	checkpointPayload, err := json.Marshal(checkpoint)
	if err != nil {
		return fmt.Errorf("encode model leaderboard checkpoint %q: %w", category, err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin model leaderboard replacement: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO model_leaderboards (category, payload) VALUES (?, ?)
		ON CONFLICT(category) DO UPDATE SET payload = excluded.payload
	`, category, payload); err != nil {
		return fmt.Errorf("save model leaderboard %q: %w", category, err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO checkpoints (name, payload) VALUES (?, ?)
		ON CONFLICT(name) DO UPDATE SET payload = excluded.payload
	`, checkpointName, checkpointPayload); err != nil {
		return fmt.Errorf("save model leaderboard checkpoint %q: %w", category, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit model leaderboard %q: %w", category, err)
	}
	return nil
}

func (s *Store) LoadModelLeaderboard(ctx context.Context, category string) (content.ModelLeaderboard, error) {
	category, err := content.NormalizeModelLeaderboardCategory(category)
	if err != nil {
		return content.ModelLeaderboard{}, content.ErrBadQuery
	}
	var payload []byte
	if err := s.db.QueryRowContext(ctx, `SELECT payload FROM model_leaderboards WHERE category = ?`, category).Scan(&payload); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return content.ModelLeaderboard{}, content.ErrNotFound
		}
		return content.ModelLeaderboard{}, fmt.Errorf("load model leaderboard %q: %w", category, err)
	}
	var snapshot content.ModelLeaderboard
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		return content.ModelLeaderboard{}, fmt.Errorf("decode model leaderboard %q: %w", category, err)
	}
	if snapshot.Category != category {
		return content.ModelLeaderboard{}, fmt.Errorf("decode model leaderboard %q: category mismatch", category)
	}
	if snapshot.Entries == nil {
		snapshot.Entries = []content.ModelLeaderboardEntry{}
	}
	if snapshot.PriceNotes == nil {
		snapshot.PriceNotes = []string{}
	}
	return snapshot, nil
}

func (s *Store) saveMetadata(ctx context.Context, key string, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode content metadata %q: %w", key, err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO content_metadata (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value
	`, key, payload)
	if err != nil {
		return fmt.Errorf("save content metadata %q: %w", key, err)
	}
	return nil
}

func (s *Store) loadMetadata(ctx context.Context, key string, value any) error {
	var payload []byte
	err := s.db.QueryRowContext(ctx, `SELECT value FROM content_metadata WHERE key = ?`, key).Scan(&payload)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("decode content metadata %q: %w", key, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("unexpected trailing JSON value")
		}
		return fmt.Errorf("decode content metadata %q: %w", key, err)
	}
	return nil
}

func (s *Store) SaveCheckpoint(ctx context.Context, name string, checkpoint content.SourceCheckpoint) error {
	payload, err := json.Marshal(checkpoint)
	if err != nil {
		return fmt.Errorf("encode source checkpoint %q: %w", name, err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO checkpoints (name, payload) VALUES (?, ?)
		ON CONFLICT(name) DO UPDATE SET payload = excluded.payload
	`, name, payload)
	if err != nil {
		return fmt.Errorf("save source checkpoint %q: %w", name, err)
	}
	return nil
}

func (s *Store) Checkpoints(ctx context.Context) ([]content.SourceCheckpoint, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT payload FROM checkpoints ORDER BY name COLLATE BINARY`)
	if err != nil {
		return nil, fmt.Errorf("read source checkpoints: %w", err)
	}
	defer rows.Close()

	checkpoints := make([]content.SourceCheckpoint, 0)
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, fmt.Errorf("read source checkpoint: %w", err)
		}
		var checkpoint content.SourceCheckpoint
		if err := json.Unmarshal(payload, &checkpoint); err != nil {
			return nil, fmt.Errorf("decode source checkpoint: %w", err)
		}
		checkpoints = append(checkpoints, checkpoint)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate source checkpoints: %w", err)
	}
	return checkpoints, nil
}
