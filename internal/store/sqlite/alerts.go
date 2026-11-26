package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"

	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// Rows hold protobuf-encoded messages in a data column next to the few
// columns used for lookups, so fields can be added without migrations.

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// MaxDeliveriesPerChannel caps the delivery log per channel.
const MaxDeliveriesPerChannel = 200

func marshal(m proto.Message) ([]byte, error) {
	b, err := proto.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("encode %T: %w", m, err)
	}
	return b, nil
}

// ---------------------------------------------------------------------------
// Rules
// ---------------------------------------------------------------------------

// LoadRules returns stored rule overrides keyed by rule id.
func (d *DB) LoadRules(ctx context.Context) (map[string]*asterismv1.AlertRule, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT rule_id, data FROM alert_rules`)
	if err != nil {
		return nil, fmt.Errorf("load rules: %w", err)
	}
	defer rows.Close()
	out := map[string]*asterismv1.AlertRule{}
	for rows.Next() {
		var id string
		var data []byte
		if err := rows.Scan(&id, &data); err != nil {
			return nil, fmt.Errorf("load rules: %w", err)
		}
		r := &asterismv1.AlertRule{}
		if err := proto.Unmarshal(data, r); err != nil {
			return nil, fmt.Errorf("decode rule %s: %w", id, err)
		}
		out[id] = r
	}
	return out, rows.Err()
}

// SaveRule stores a rule override.
func (d *DB) SaveRule(ctx context.Context, r *asterismv1.AlertRule) error {
	data, err := marshal(r)
	if err != nil {
		return err
	}
	_, err = d.db.ExecContext(ctx, `INSERT INTO alert_rules (rule_id, data, updated_ms) VALUES (?, ?, ?)
		ON CONFLICT(rule_id) DO UPDATE SET data = excluded.data, updated_ms = excluded.updated_ms`,
		r.Id, data, time.Now().UnixMilli())
	if err != nil {
		return fmt.Errorf("save rule %s: %w", r.Id, err)
	}
	return nil
}

// DeleteRule removes an override (back to defaults).
func (d *DB) DeleteRule(ctx context.Context, id string) error {
	if _, err := d.db.ExecContext(ctx, `DELETE FROM alert_rules WHERE rule_id = ?`, id); err != nil {
		return fmt.Errorf("delete rule %s: %w", id, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Alert state
// ---------------------------------------------------------------------------

// LoadAlerts returns every stored alert (active and recent history).
func (d *DB) LoadAlerts(ctx context.Context) ([]*asterismv1.Alert, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT fingerprint, data FROM alert_state`)
	if err != nil {
		return nil, fmt.Errorf("load alerts: %w", err)
	}
	defer rows.Close()
	var out []*asterismv1.Alert
	for rows.Next() {
		var fp string
		var data []byte
		if err := rows.Scan(&fp, &data); err != nil {
			return nil, fmt.Errorf("load alerts: %w", err)
		}
		a := &asterismv1.Alert{}
		if err := proto.Unmarshal(data, a); err != nil {
			return nil, fmt.Errorf("decode alert %s: %w", fp, err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SaveAlerts upserts and deletes alert rows in one transaction.
func (d *DB) SaveAlerts(ctx context.Context, upserts []*asterismv1.Alert, deletes []string) error {
	if len(upserts) == 0 && len(deletes) == 0 {
		return nil
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("save alerts: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UnixMilli()
	for _, a := range upserts {
		data, err := marshal(a)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO alert_state (fingerprint, rule_id, state, updated_ms, data) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(fingerprint) DO UPDATE SET rule_id = excluded.rule_id, state = excluded.state, updated_ms = excluded.updated_ms, data = excluded.data`,
			a.Id, a.RuleId, int(a.State), now, data); err != nil {
			return fmt.Errorf("save alert %s: %w", a.Id, err)
		}
	}
	for _, fp := range deletes {
		if _, err := tx.ExecContext(ctx, `DELETE FROM alert_state WHERE fingerprint = ?`, fp); err != nil {
			return fmt.Errorf("delete alert %s: %w", fp, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("save alerts: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Silences
// ---------------------------------------------------------------------------

// ListSilences returns every stored silence.
func (d *DB) ListSilences(ctx context.Context) ([]*asterismv1.Silence, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT data FROM silences ORDER BY ends_ms DESC`)
	if err != nil {
		return nil, fmt.Errorf("list silences: %w", err)
	}
	defer rows.Close()
	var out []*asterismv1.Silence
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, fmt.Errorf("list silences: %w", err)
		}
		s := &asterismv1.Silence{}
		if err := proto.Unmarshal(data, s); err != nil {
			return nil, fmt.Errorf("decode silence: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SaveSilence upserts a silence.
func (d *DB) SaveSilence(ctx context.Context, s *asterismv1.Silence) error {
	data, err := marshal(s)
	if err != nil {
		return err
	}
	if _, err := d.db.ExecContext(ctx, `INSERT INTO silences (id, ends_ms, data) VALUES (?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET ends_ms = excluded.ends_ms, data = excluded.data`, s.Id, s.EndsAtMs, data); err != nil {
		return fmt.Errorf("save silence %s: %w", s.Id, err)
	}
	return nil
}

// PruneSilences deletes silences that ended before beforeMs.
func (d *DB) PruneSilences(ctx context.Context, beforeMs int64) error {
	if _, err := d.db.ExecContext(ctx, `DELETE FROM silences WHERE ends_ms < ?`, beforeMs); err != nil {
		return fmt.Errorf("prune silences: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Channels
// ---------------------------------------------------------------------------

// ChannelRecord is a stored channel: its public settings (secret fields
// empty) and the sealed secret blob.
type ChannelRecord struct {
	Channel *asterismv1.NotificationChannel
	Secret  []byte
}

// ListChannels returns every channel, oldest first.
func (d *DB) ListChannels(ctx context.Context) ([]ChannelRecord, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT id, data, secret FROM channels ORDER BY created_ms, id`)
	if err != nil {
		return nil, fmt.Errorf("list channels: %w", err)
	}
	defer rows.Close()
	var out []ChannelRecord
	for rows.Next() {
		var id string
		var data, secret []byte
		if err := rows.Scan(&id, &data, &secret); err != nil {
			return nil, fmt.Errorf("list channels: %w", err)
		}
		c := &asterismv1.NotificationChannel{}
		if err := proto.Unmarshal(data, c); err != nil {
			return nil, fmt.Errorf("decode channel %s: %w", id, err)
		}
		out = append(out, ChannelRecord{Channel: c, Secret: secret})
	}
	return out, rows.Err()
}

// GetChannel returns one channel or ErrNotFound.
func (d *DB) GetChannel(ctx context.Context, id string) (ChannelRecord, error) {
	var data, secret []byte
	err := d.db.QueryRowContext(ctx, `SELECT data, secret FROM channels WHERE id = ?`, id).Scan(&data, &secret)
	if errors.Is(err, sql.ErrNoRows) {
		return ChannelRecord{}, ErrNotFound
	}
	if err != nil {
		return ChannelRecord{}, fmt.Errorf("get channel %s: %w", id, err)
	}
	c := &asterismv1.NotificationChannel{}
	if err := proto.Unmarshal(data, c); err != nil {
		return ChannelRecord{}, fmt.Errorf("decode channel %s: %w", id, err)
	}
	return ChannelRecord{Channel: c, Secret: secret}, nil
}

// SaveChannel upserts a channel.
func (d *DB) SaveChannel(ctx context.Context, rec ChannelRecord) error {
	data, err := marshal(rec.Channel)
	if err != nil {
		return err
	}
	if _, err := d.db.ExecContext(ctx, `INSERT INTO channels (id, type, created_ms, data, secret) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET data = excluded.data, secret = excluded.secret`,
		rec.Channel.Id, int(rec.Channel.Type), rec.Channel.CreatedAtMs, data, rec.Secret); err != nil {
		return fmt.Errorf("save channel %s: %w", rec.Channel.Id, err)
	}
	return nil
}

// DeleteChannel removes a channel and its delivery log.
func (d *DB) DeleteChannel(ctx context.Context, id string) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("delete channel: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `DELETE FROM channels WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete channel %s: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM deliveries WHERE channel_id = ?`, id); err != nil {
		return fmt.Errorf("delete deliveries %s: %w", id, err)
	}
	return tx.Commit()
}

// ---------------------------------------------------------------------------
// Delivery log
// ---------------------------------------------------------------------------

// AddDelivery appends an attempt, sets its id, and keeps only the newest
// MaxDeliveriesPerChannel rows for that channel.
func (d *DB) AddDelivery(ctx context.Context, a *asterismv1.DeliveryAttempt) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("add delivery: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `INSERT INTO deliveries (channel_id, ts_ms, data) VALUES (?, ?, x'')`, a.ChannelId, a.TsMs)
	if err != nil {
		return fmt.Errorf("add delivery: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("add delivery: %w", err)
	}
	a.Id = id
	data, err := marshal(a)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE deliveries SET data = ? WHERE id = ?`, data, id); err != nil {
		return fmt.Errorf("add delivery: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM deliveries WHERE channel_id = ? AND id NOT IN
		(SELECT id FROM deliveries WHERE channel_id = ? ORDER BY id DESC LIMIT ?)`, a.ChannelId, a.ChannelId, MaxDeliveriesPerChannel); err != nil {
		return fmt.Errorf("cap deliveries: %w", err)
	}
	return tx.Commit()
}

// ListDeliveries returns attempts newest first for one channel ("" = all),
// skipping offset rows, plus the total count.
func (d *DB) ListDeliveries(ctx context.Context, channelID string, offset, limit int) ([]*asterismv1.DeliveryAttempt, int, error) {
	where, args := "", []any{}
	if channelID != "" {
		where, args = "WHERE channel_id = ?", append(args, channelID)
	}
	var total int
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM deliveries `+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count deliveries: %w", err)
	}
	rows, err := d.db.QueryContext(ctx, `SELECT data FROM deliveries `+where+` ORDER BY id DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("list deliveries: %w", err)
	}
	defer rows.Close()
	var out []*asterismv1.DeliveryAttempt
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, 0, fmt.Errorf("list deliveries: %w", err)
		}
		a := &asterismv1.DeliveryAttempt{}
		if err := proto.Unmarshal(data, a); err != nil {
			return nil, 0, fmt.Errorf("decode delivery: %w", err)
		}
		out = append(out, a)
	}
	return out, total, rows.Err()
}

// LastDeliveries returns the newest attempt per channel.
func (d *DB) LastDeliveries(ctx context.Context) (map[string]*asterismv1.DeliveryAttempt, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT d.channel_id, d.data FROM deliveries d
		JOIN (SELECT channel_id, MAX(id) AS id FROM deliveries GROUP BY channel_id) m ON m.id = d.id`)
	if err != nil {
		return nil, fmt.Errorf("last deliveries: %w", err)
	}
	defer rows.Close()
	out := map[string]*asterismv1.DeliveryAttempt{}
	for rows.Next() {
		var ch string
		var data []byte
		if err := rows.Scan(&ch, &data); err != nil {
			return nil, fmt.Errorf("last deliveries: %w", err)
		}
		a := &asterismv1.DeliveryAttempt{}
		if err := proto.Unmarshal(data, a); err != nil {
			return nil, fmt.Errorf("decode delivery: %w", err)
		}
		out[ch] = a
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Web Push subscriptions
// ---------------------------------------------------------------------------

// PushRecord is a stored browser subscription: public metadata and the
// sealed endpoint and keys.
type PushRecord struct {
	Sub          *asterismv1.WebPushSubscription
	EndpointHash string
	Secret       []byte
}

// ListPushSubscriptions returns every subscription, oldest first.
func (d *DB) ListPushSubscriptions(ctx context.Context) ([]PushRecord, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT endpoint_hash, data, secret FROM push_subscriptions ORDER BY created_ms, id`)
	if err != nil {
		return nil, fmt.Errorf("list push subscriptions: %w", err)
	}
	defer rows.Close()
	var out []PushRecord
	for rows.Next() {
		var hash string
		var data, secret []byte
		if err := rows.Scan(&hash, &data, &secret); err != nil {
			return nil, fmt.Errorf("list push subscriptions: %w", err)
		}
		s := &asterismv1.WebPushSubscription{}
		if err := proto.Unmarshal(data, s); err != nil {
			return nil, fmt.Errorf("decode push subscription: %w", err)
		}
		out = append(out, PushRecord{Sub: s, EndpointHash: hash, Secret: secret})
	}
	return out, rows.Err()
}

// SavePushSubscription upserts a subscription by endpoint hash; an existing
// row for the same endpoint keeps its id.
func (d *DB) SavePushSubscription(ctx context.Context, rec PushRecord) (*asterismv1.WebPushSubscription, error) {
	var existing string
	err := d.db.QueryRowContext(ctx, `SELECT id FROM push_subscriptions WHERE endpoint_hash = ?`, rec.EndpointHash).Scan(&existing)
	switch {
	case err == nil:
		rec.Sub.Id = existing
	case errors.Is(err, sql.ErrNoRows):
	default:
		return nil, fmt.Errorf("save push subscription: %w", err)
	}
	data, err := marshal(rec.Sub)
	if err != nil {
		return nil, err
	}
	if _, err := d.db.ExecContext(ctx, `INSERT INTO push_subscriptions (id, endpoint_hash, created_ms, data, secret) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET data = excluded.data, secret = excluded.secret`,
		rec.Sub.Id, rec.EndpointHash, rec.Sub.CreatedAtMs, data, rec.Secret); err != nil {
		return nil, fmt.Errorf("save push subscription: %w", err)
	}
	return rec.Sub, nil
}

// DeletePushSubscription removes a subscription by id or endpoint hash.
func (d *DB) DeletePushSubscription(ctx context.Context, id, endpointHash string) (bool, error) {
	res, err := d.db.ExecContext(ctx, `DELETE FROM push_subscriptions WHERE id = ? OR endpoint_hash = ?`, id, endpointHash)
	if err != nil {
		return false, fmt.Errorf("delete push subscription: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
