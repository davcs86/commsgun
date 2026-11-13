package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/davcs86/commsgun/api/internal/config"
	"github.com/davcs86/commsgun/api/internal/constants"
	"github.com/davcs86/commsgun/api/internal/model"
)

// Ping checks that the read pool answers within constants.PingTimeout.
func (s *Store) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, constants.PingTimeout)
	defer cancel()
	var one int
	return translate(s.r.QueryRowContext(ctx, `SELECT 1`).Scan(&one))
}

// Backlog returns when the oldest dispatchable message became due: queued, not held, its provider
// configured, unpaused and under both window limits.
func (s *Store) Backlog(ctx context.Context, now time.Time, providers map[model.Channel]*config.ProviderCfg) (oldest time.Time, ok bool, err error) {
	now = now.UTC()
	nowMS := now.UnixMilli()
	for ch, p := range providers {
		scope := providerScope(ch, p.Name)
		var paused int
		if err = s.r.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM provider_pauses WHERE provider=? AND until>?`, scope, nowMS).Scan(&paused); err != nil {
			return time.Time{}, false, translate(err)
		}
		if paused > 0 {
			continue
		}
		full := false
		for _, w := range windowsAt(now, p.PerMinute, p.PerDay) {
			var used int
			err = s.r.QueryRowContext(ctx,
				`SELECT count FROM rate_counters WHERE scope=? AND window_kind=? AND window_start=?`,
				scope, w.kind, w.start.UnixMilli()).Scan(&used)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return time.Time{}, false, translate(err)
			}
			if used >= w.limit {
				full = true
			}
		}
		if full {
			continue
		}
		var due sql.NullInt64
		if err = s.r.QueryRowContext(ctx,
			`SELECT MIN(next_attempt_at) FROM messages
			 WHERE state=? AND hold_reason IS NULL AND next_attempt_at<=? AND channel=? AND provider=?`,
			model.StateQueued, nowMS, ch, p.Name).Scan(&due); err != nil {
			return time.Time{}, false, translate(err)
		}
		if due.Valid {
			if t := time.UnixMilli(due.Int64).UTC(); !ok || t.Before(oldest) {
				oldest, ok = t, true
			}
		}
	}
	return oldest, ok, nil
}
