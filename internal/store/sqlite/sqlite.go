// Package sqlite is the hub's persistent store: a single SQLite database
// (modernc.org/sqlite, pure Go) at <data_dir>/asterism.db in WAL mode with a
// small forward-only migration system. M4 adds metric tiers on top of it.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// FileName is the database file inside the data directory.
const FileName = "asterism.db"

// DB wraps the database handle.
type DB struct {
	db   *sql.DB
	path string
}

// migration is one forward-only schema step. Versions are dense from 1.
type migration struct {
	version int
	name    string
	sql     string
}

// migrations is the schema history. Never edit an applied migration; add one.
var migrations = []migration{
	{1, "alerts and notifications", `
CREATE TABLE settings (
	key        TEXT PRIMARY KEY,
	value      BLOB NOT NULL,
	updated_ms INTEGER NOT NULL
);
CREATE TABLE alert_rules (
	rule_id    TEXT PRIMARY KEY,
	data       BLOB NOT NULL,
	updated_ms INTEGER NOT NULL
);
CREATE TABLE alert_state (
	fingerprint TEXT PRIMARY KEY,
	rule_id     TEXT NOT NULL,
	state       INTEGER NOT NULL,
	updated_ms  INTEGER NOT NULL,
	data        BLOB NOT NULL
);
CREATE INDEX alert_state_by_state ON alert_state (state, updated_ms);
CREATE TABLE silences (
	id      TEXT PRIMARY KEY,
	ends_ms INTEGER NOT NULL,
	data    BLOB NOT NULL
);
CREATE TABLE channels (
	id         TEXT PRIMARY KEY,
	type       INTEGER NOT NULL,
	created_ms INTEGER NOT NULL,
	data       BLOB NOT NULL,
	secret     BLOB
);
CREATE TABLE deliveries (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	channel_id TEXT NOT NULL,
	ts_ms      INTEGER NOT NULL,
	data       BLOB NOT NULL
);
CREATE INDEX deliveries_by_channel ON deliveries (channel_id, id);
CREATE TABLE push_subscriptions (
	id            TEXT PRIMARY KEY,
	endpoint_hash TEXT NOT NULL UNIQUE,
	created_ms    INTEGER NOT NULL,
	data          BLOB NOT NULL,
	secret        BLOB NOT NULL
);
`},
	{2, "browser sessions", `
CREATE TABLE sessions (
	token_sha256 TEXT PRIMARY KEY,
	email        TEXT NOT NULL,
	credential   TEXT NOT NULL,
	created_ms   INTEGER NOT NULL,
	expires_ms   INTEGER NOT NULL
);
CREATE INDEX sessions_by_expiry ON sessions (expires_ms);
`},
}

// Open opens (creating with mode 0600 when missing) and migrates the
// database at path.
func Open(ctx context.Context, path string) (*DB, error) {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		f, createErr := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if createErr != nil {
			return nil, fmt.Errorf("create %s: %w", path, createErr)
		}
		_ = f.Close()
	}
	q := url.Values{}
	for _, p := range []string{"journal_mode(WAL)", "synchronous(NORMAL)", "busy_timeout(5000)", "foreign_keys(1)"} {
		q.Add("_pragma", p)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	// One connection: writes are rare and small, and a single writer never
	// sees SQLITE_BUSY from itself.
	db.SetMaxOpenConns(1)
	d := &DB{db: db, path: path}
	if err := d.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	// WAL side files inherit the process umask; keep them private too.
	for _, side := range []string{path, path + "-wal", path + "-shm"} {
		if st, statErr := os.Stat(side); statErr == nil && st.Mode().Perm()&0o077 != 0 {
			_ = os.Chmod(side, 0o600)
		}
	}
	return d, nil
}

// Close closes the database.
func (d *DB) Close() error { return d.db.Close() }

// Path returns the database file path.
func (d *DB) Path() string { return d.path }

func (d *DB) migrate(ctx context.Context) error {
	if _, err := d.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		name       TEXT NOT NULL,
		applied_ms INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	current, err := d.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	if current > len(migrations) {
		return fmt.Errorf("database schema version %d is newer than this binary supports (%d); upgrade asterism", current, len(migrations))
	}
	for _, m := range migrations[current:] {
		tx, err := d.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("migrate %d: %w", m.version, err)
		}
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migrate %d (%s): %w", m.version, m.name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, name, applied_ms) VALUES (?, ?, ?)`,
			m.version, m.name, time.Now().UnixMilli()); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migrate %d: %w", m.version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("migrate %d: %w", m.version, err)
		}
	}
	return nil
}

// SchemaVersion returns the highest applied migration.
func (d *DB) SchemaVersion(ctx context.Context) (int, error) {
	var v sql.NullInt64
	if err := d.db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&v); err != nil {
		return 0, fmt.Errorf("schema version: %w", err)
	}
	return int(v.Int64), nil
}

// JournalMode reports the active journal mode ("wal").
func (d *DB) JournalMode(ctx context.Context) (string, error) {
	var mode string
	err := d.db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode)
	return mode, err
}
