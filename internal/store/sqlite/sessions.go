package sqlite

import (
	"context"
	"fmt"
	"time"
)

// SessionRow is a persisted admin browser session. TokenSHA256 is the hex
// SHA-256 digest of the cookie value; the raw token is never stored.
type SessionRow struct {
	TokenSHA256 string
	Email       string
	Credential  string
	CreatedAt   time.Time
	ExpiresAt   time.Time
}

// LoadSessions returns every stored session.
func (d *DB) LoadSessions(ctx context.Context) ([]SessionRow, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT token_sha256, email, credential, created_ms, expires_ms FROM sessions`)
	if err != nil {
		return nil, fmt.Errorf("load sessions: %w", err)
	}
	defer rows.Close()
	var out []SessionRow
	for rows.Next() {
		var r SessionRow
		var created, expires int64
		if err := rows.Scan(&r.TokenSHA256, &r.Email, &r.Credential, &created, &expires); err != nil {
			return nil, fmt.Errorf("load sessions: %w", err)
		}
		r.CreatedAt, r.ExpiresAt = time.UnixMilli(created).UTC(), time.UnixMilli(expires).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// SaveSession inserts or updates a session.
func (d *DB) SaveSession(ctx context.Context, r SessionRow) error {
	_, err := d.db.ExecContext(ctx, `INSERT INTO sessions (token_sha256, email, credential, created_ms, expires_ms)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (token_sha256) DO UPDATE SET email = excluded.email, credential = excluded.credential, expires_ms = excluded.expires_ms`,
		r.TokenSHA256, r.Email, r.Credential, r.CreatedAt.UnixMilli(), r.ExpiresAt.UnixMilli())
	if err != nil {
		return fmt.Errorf("save session: %w", err)
	}
	return nil
}

// DeleteSession removes one session.
func (d *DB) DeleteSession(ctx context.Context, tokenSHA256 string) error {
	if _, err := d.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_sha256 = ?`, tokenSHA256); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// DeleteExpiredSessions removes sessions that expired at or before now.
func (d *DB) DeleteExpiredSessions(ctx context.Context, now time.Time) error {
	if _, err := d.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_ms <= ?`, now.UnixMilli()); err != nil {
		return fmt.Errorf("delete expired sessions: %w", err)
	}
	return nil
}
