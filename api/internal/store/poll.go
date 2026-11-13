package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/davcs86/commsgun/api/internal/constants"
	"github.com/davcs86/commsgun/api/internal/model"
	"github.com/davcs86/commsgun/api/internal/provider"
)

var ErrNotSent = errors.New("store: message is not in state sent")

// SchedulePoll sets the first bounce poll of a sent message.
func (s *Store) SchedulePoll(ctx context.Context, id string, at time.Time) error {
	res, err := s.w.ExecContext(ctx,
		`UPDATE messages SET next_poll_at=? WHERE id=? AND state=?`, at.UTC().UnixMilli(), id, model.StateSent)
	return affectedSent(res, err)
}

// DuePolls lists sent messages whose next poll is due, oldest first.
func (s *Store) DuePolls(ctx context.Context, now time.Time, limit int) ([]model.Message, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.w.QueryContext(ctx,
		`SELECT `+messageCols+` FROM messages WHERE state=? AND next_poll_at<=? ORDER BY next_poll_at, rowid LIMIT ?`,
		model.StateSent, now.UTC().UnixMilli(), limit)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()
	var out []model.Message
	for rows.Next() {
		m, err := scanClaimed(rows)
		if err != nil {
			return nil, translate(err)
		}
		out = append(out, m.Message)
	}
	return out, translate(rows.Err())
}

// RecordPoll applies a polled status to a sent message; Pending only advances the schedule.
func (s *Store) RecordPoll(ctx context.Context, now time.Time, id string, st provider.DeliveryStatus) error {
	var state model.State
	var et model.ErrorType
	switch st {
	case provider.Pending:
		return s.advancePoll(ctx, now, id)
	case provider.Delivered:
		state = model.StateDelivered
	case provider.Bounced:
		state, et = model.StateBounced, model.ErrBounced
	case provider.Complained:
		state, et = model.StateComplained, model.ErrComplained
	case provider.Rejected:
		state, et = model.StateFailed, model.ErrProviderRejected
	default:
		return errors.New("store: unknown delivery status")
	}
	nowMS := now.UTC().UnixMilli()
	res, err := s.w.ExecContext(ctx,
		`UPDATE messages SET state=?, error_type=?, final_at=?, next_poll_at=NULL, poll_count=poll_count+1, updated_at=?
		 WHERE id=? AND state=?`,
		state, nullIfEmpty(string(et)), nowMS, nowMS, id, model.StateSent)
	return affectedSent(res, err)
}

// RecordPollError counts a failed poll and moves on to the next scheduled one.
func (s *Store) RecordPollError(ctx context.Context, now time.Time, id string) error {
	return s.advancePoll(ctx, now, id)
}

// ClearPoll stops polling a sent message that cannot be polled.
func (s *Store) ClearPoll(ctx context.Context, now time.Time, id string) error {
	res, err := s.w.ExecContext(ctx,
		`UPDATE messages SET next_poll_at=NULL, updated_at=? WHERE id=? AND state=?`,
		now.UTC().UnixMilli(), id, model.StateSent)
	return affectedSent(res, err)
}

func (s *Store) advancePoll(ctx context.Context, now time.Time, id string) (err error) {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return translate(err)
	}
	defer func() {
		if err != nil {
			tx.Rollback()
			err = translate(err)
		}
	}()
	var state model.State
	var sentMS sql.NullInt64
	var done int
	if err = tx.QueryRowContext(ctx, `SELECT state, sent_at, poll_count FROM messages WHERE id=?`, id).
		Scan(&state, &sentMS, &done); err != nil {
		return err
	}
	if state != model.StateSent {
		return ErrNotSent
	}
	done++
	var next any
	if done < len(constants.PollSchedule) && sentMS.Valid {
		next = sentMS.Int64 + constants.PollSchedule[done].Milliseconds()
	}
	if _, err = tx.ExecContext(ctx,
		`UPDATE messages SET poll_count=?, next_poll_at=?, updated_at=? WHERE id=?`,
		done, next, now.UTC().UnixMilli(), id); err != nil {
		return err
	}
	return tx.Commit()
}

func affectedSent(res sql.Result, err error) error {
	if err != nil {
		return translate(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return translate(err)
	}
	if n != 1 {
		return ErrNotSent
	}
	return nil
}
