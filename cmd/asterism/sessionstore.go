package main

import (
	"context"
	"time"

	"github.com/prabhavalabs/asterism/internal/auth"
	"github.com/prabhavalabs/asterism/internal/store/sqlite"
)

// sessionStore adapts the SQLite store to auth.SessionStore.
type sessionStore struct{ db *sqlite.DB }

func (s sessionStore) LoadSessions(ctx context.Context) ([]auth.StoredSession, error) {
	rows, err := s.db.LoadSessions(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]auth.StoredSession, 0, len(rows))
	for _, r := range rows {
		out = append(out, auth.StoredSession{
			TokenSHA256: r.TokenSHA256, Email: r.Email, Credential: r.Credential,
			CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt,
		})
	}
	return out, nil
}

func (s sessionStore) SaveSession(ctx context.Context, r auth.StoredSession) error {
	return s.db.SaveSession(ctx, sqlite.SessionRow{
		TokenSHA256: r.TokenSHA256, Email: r.Email, Credential: r.Credential,
		CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt,
	})
}

func (s sessionStore) DeleteSession(ctx context.Context, tokenSHA256 string) error {
	return s.db.DeleteSession(ctx, tokenSHA256)
}

func (s sessionStore) DeleteExpiredSessions(ctx context.Context, now time.Time) error {
	return s.db.DeleteExpiredSessions(ctx, now)
}
