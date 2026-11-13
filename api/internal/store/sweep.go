package store

import (
	"context"
	"time"

	"github.com/davcs86/commsgun/api/internal/constants"
)

type SweepResult struct{ Expired, Messages, Requests, Counters int }

// Sweep expires overdue unfinished messages, then deletes data past retention and returns freed pages.
func (s *Store) Sweep(ctx context.Context, now time.Time) (res SweepResult, err error) {
	now = now.UTC()
	if res.Expired, err = s.ExpireDue(ctx, now); err != nil {
		return res, err
	}
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return res, translate(err)
	}
	defer tx.Rollback()

	cutoff := now.Add(-constants.Retention).UnixMilli()
	if _, err = tx.ExecContext(ctx,
		`DELETE FROM attempts WHERE message_id IN (SELECT id FROM messages WHERE created_at<?)`, cutoff); err != nil {
		return res, translate(err)
	}
	r, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE created_at<?`, cutoff)
	if err != nil {
		return res, translate(err)
	}
	res.Messages = affected(r)
	r, err = tx.ExecContext(ctx,
		`DELETE FROM requests WHERE created_at<? AND NOT EXISTS (SELECT 1 FROM messages WHERE request_id=requests.id)`, cutoff)
	if err != nil {
		return res, translate(err)
	}
	res.Requests = affected(r)
	r, err = tx.ExecContext(ctx,
		`DELETE FROM rate_counters WHERE (window_kind=? AND window_start<?) OR (window_kind=? AND window_start<?)`,
		windowMinute, now.Add(-constants.MinuteCounterMaxAge).UnixMilli(),
		windowDay, now.Add(-constants.DayCounterMaxAge).UnixMilli())
	if err != nil {
		return res, translate(err)
	}
	res.Counters = affected(r)
	if _, err = tx.ExecContext(ctx, `DELETE FROM provider_pauses WHERE until<=?`, now.UnixMilli()); err != nil {
		return res, translate(err)
	}
	if err = tx.Commit(); err != nil {
		return res, translate(err)
	}
	_, err = s.w.ExecContext(ctx, `PRAGMA incremental_vacuum`)
	return res, translate(err)
}

func affected(r interface{ RowsAffected() (int64, error) }) int {
	n, _ := r.RowsAffected()
	return int(n)
}
