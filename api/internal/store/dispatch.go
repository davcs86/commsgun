package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/davcs86/commsgun/api/internal/constants"
	"github.com/davcs86/commsgun/api/internal/model"
	"github.com/davcs86/commsgun/api/internal/provider"
)

var ErrNotSending = errors.New("store: message is not in state sending")

const clearBodies = `subject=NULL, body_text=NULL, body_html=NULL, reply_to=NULL, sms_body=NULL`

const messageCols = `id, request_id, app, channel, recipient, sender, state, hold_reason, attempts, error_type,
	provider, provider_message_id, body_bytes, created_at, sent_at, final_at, expires_at, next_attempt_at,
	subject, body_text, body_html, reply_to, sms_body`

type ClaimedMessage struct {
	model.Message
	Body provider.Body
}

// ClaimDue leases up to limit due messages of one channel and provider, oldest next_attempt_at first.
func (s *Store) ClaimDue(ctx context.Context, now time.Time, limit int, channel model.Channel, provider string) (out []ClaimedMessage, err error) {
	if limit <= 0 {
		return nil, nil
	}
	nowMS := now.UTC().UnixMilli()
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return nil, translate(err)
	}
	defer func() {
		if err != nil {
			tx.Rollback()
			err = translate(err)
		}
	}()

	rows, err := tx.QueryContext(ctx,
		`SELECT id FROM messages
		 WHERE ((state=? AND hold_reason IS NULL AND next_attempt_at<=? AND (locked_until IS NULL OR locked_until<=?))
		        OR (state=? AND locked_until<=?))
		   AND channel=? AND provider=?
		 ORDER BY next_attempt_at, rowid LIMIT ?`,
		model.StateQueued, nowMS, nowMS, model.StateSending, nowMS, channel, provider, limit)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}

	lease := now.UTC().Add(constants.LeaseDuration).UnixMilli()
	for _, id := range ids {
		var m ClaimedMessage
		if m, err = scanClaimed(tx.QueryRowContext(ctx,
			`UPDATE messages SET state=?, locked_until=?, updated_at=? WHERE id=? RETURNING `+messageCols,
			model.StateSending, lease, nowMS, id)); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

// ProviderReserve takes one slot in the channel provider's minute and day windows unless paused or full.
func (s *Store) ProviderReserve(ctx context.Context, now time.Time, channel model.Channel, provider string, perMinute, perDay int) (ok bool, until time.Time, reason string, err error) {
	scope := providerScope(channel, provider)
	now = now.UTC()
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return false, until, "", translate(err)
	}
	defer func() {
		if err != nil {
			tx.Rollback()
			err = translate(err)
		}
	}()

	var pauseUntil int64
	var pauseReason string
	switch err = tx.QueryRowContext(ctx, `SELECT until, reason FROM provider_pauses WHERE provider=?`, scope).
		Scan(&pauseUntil, &pauseReason); {
	case err == nil:
		if pauseUntil > now.UnixMilli() {
			return false, time.UnixMilli(pauseUntil).UTC(), "pause:" + pauseReason, tx.Commit()
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM provider_pauses WHERE provider=?`, scope); err != nil {
			return false, until, "", err
		}
	case !errors.Is(err, sql.ErrNoRows):
		return false, until, "", err
	}

	blocked, err := reserveWindows(ctx, tx, scope, windowsAt(now, perMinute, perDay), 1)
	if err != nil {
		return false, until, "", err
	}
	if blocked != nil {
		return false, blocked.reset, blocked.kind, tx.Commit()
	}
	if err = tx.Commit(); err != nil {
		return false, until, "", err
	}
	return true, time.Time{}, "", nil
}

// ProviderRelease undoes one reservation; now must be the time passed to ProviderReserve.
func (s *Store) ProviderRelease(ctx context.Context, now time.Time, channel model.Channel, provider string) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return translate(err)
	}
	defer tx.Rollback()
	for _, w := range windowsAt(now, 0, 0) {
		if _, err := tx.ExecContext(ctx,
			`UPDATE rate_counters SET count = count - 1 WHERE scope=? AND window_kind=? AND window_start=? AND count > 0`,
			providerScope(channel, provider), w.kind, w.start.UnixMilli()); err != nil {
			return translate(err)
		}
	}
	return translate(tx.Commit())
}

func (s *Store) PauseProvider(ctx context.Context, channel model.Channel, provider string, until time.Time, reason string) error {
	_, err := s.w.ExecContext(ctx,
		`INSERT INTO provider_pauses(provider, until, reason) VALUES (?,?,?)
		 ON CONFLICT(provider) DO UPDATE SET until=excluded.until, reason=excluded.reason`,
		providerScope(channel, provider), until.UTC().UnixMilli(), reason)
	return translate(err)
}

// RecordAttempt writes the attempt row and applies its transition; the row must be in state sending.
func (s *Store) RecordAttempt(ctx context.Context, now time.Time, id string, r provider.SendResult, started time.Time, backoff time.Duration) (err error) {
	nowMS := now.UTC().UnixMilli()
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
	var attempts int
	if err = tx.QueryRowContext(ctx, `SELECT state, attempts FROM messages WHERE id=?`, id).Scan(&state, &attempts); err != nil {
		return err
	}
	if state != model.StateSending {
		return ErrNotSending
	}
	n := attempts + 1
	if r.Outcome != provider.Accepted && r.ErrorType == "" {
		r.ErrorType = model.ErrUnknown
	}

	var outcome model.Outcome
	switch r.Outcome {
	case provider.Accepted:
		outcome = model.OutcomeAccepted
		_, err = tx.ExecContext(ctx,
			`UPDATE messages SET state=?, attempts=?, sent_at=?, provider_message_id=?, locked_until=NULL, updated_at=?, `+clearBodies+` WHERE id=?`,
			model.StateSent, n, nowMS, r.ProviderID, nowMS, id)
	case provider.Retryable:
		outcome = model.OutcomeRetryable
		if n >= constants.MaxAttempts {
			err = failTx(ctx, tx, id, n, r.ErrorType, nowMS)
		} else {
			_, err = tx.ExecContext(ctx,
				`UPDATE messages SET state=?, attempts=?, next_attempt_at=?, locked_until=NULL, updated_at=? WHERE id=?`,
				model.StateQueued, n, now.UTC().Add(backoff).UnixMilli(), nowMS, id)
		}
	case provider.Permanent:
		outcome = model.OutcomePermanent
		err = failTx(ctx, tx, id, n, r.ErrorType, nowMS)
	default:
		return fmt.Errorf("store: cannot record outcome %d", r.Outcome)
	}
	if err != nil {
		return err
	}

	var status any
	if r.Status != 0 {
		status = r.Status
	}
	if _, err = tx.ExecContext(ctx,
		`INSERT INTO attempts(message_id, n, started_at, duration_ms, outcome, error_type, provider_status, provider_error, message)
		 VALUES (?,?,?,?,?,?,?,?,?)`,
		id, n, started.UTC().UnixMilli(), nowMS-started.UTC().UnixMilli(), outcome,
		nullIfEmpty(string(r.ErrorType)), status, nullIfEmpty(r.ProviderError), r.Message); err != nil {
		return err
	}
	return tx.Commit()
}

func failTx(ctx context.Context, tx *sql.Tx, id string, n int, et model.ErrorType, nowMS int64) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE messages SET state=?, attempts=?, error_type=?, final_at=?, locked_until=NULL, updated_at=?, `+clearBodies+` WHERE id=?`,
		model.StateFailed, n, nullIfEmpty(string(et)), nowMS, nowMS, id)
	return err
}

// Unclaim returns a sending message to the queue without consuming an attempt.
func (s *Store) Unclaim(ctx context.Context, now time.Time, id string) error {
	res, err := s.w.ExecContext(ctx,
		`UPDATE messages SET state=?, locked_until=NULL, updated_at=? WHERE id=? AND state=?`,
		model.StateQueued, now.UTC().UnixMilli(), id, model.StateSending)
	return affectedOne(res, err)
}

// ExpireDue expires messages past their deadline that are queued or held by a dead lease.
func (s *Store) ExpireDue(ctx context.Context, now time.Time) (int, error) {
	nowMS := now.UTC().UnixMilli()
	res, err := s.w.ExecContext(ctx,
		`UPDATE messages SET state=?, error_type=?, final_at=?, locked_until=NULL, updated_at=?, `+clearBodies+`
		 WHERE expires_at<=? AND (state=? OR (state=? AND locked_until<=?))`,
		model.StateExpired, model.ErrExpired, nowMS, nowMS, nowMS, model.StateQueued, model.StateSending, nowMS)
	if err != nil {
		return 0, translate(err)
	}
	n, err := res.RowsAffected()
	return int(n), translate(err)
}

// ExpireClaimed expires a claimed (sending) message instead of sending it.
func (s *Store) ExpireClaimed(ctx context.Context, now time.Time, id string) error {
	nowMS := now.UTC().UnixMilli()
	res, err := s.w.ExecContext(ctx,
		`UPDATE messages SET state=?, error_type=?, final_at=?, locked_until=NULL, updated_at=?, `+clearBodies+`
		 WHERE id=? AND state=?`,
		model.StateExpired, model.ErrExpired, nowMS, nowMS, id, model.StateSending)
	return affectedOne(res, err)
}

// ReleaseHeld assigns provider to queued messages of channel held for no_provider.
func (s *Store) ReleaseHeld(ctx context.Context, channel model.Channel, provider string) (int, error) {
	res, err := s.w.ExecContext(ctx,
		`UPDATE messages SET provider=?, hold_reason=NULL WHERE state=? AND channel=? AND hold_reason=?`,
		provider, model.StateQueued, channel, holdNoProv)
	if err != nil {
		return 0, translate(err)
	}
	n, err := res.RowsAffected()
	return int(n), translate(err)
}

func affectedOne(res sql.Result, err error) error {
	if err != nil {
		return translate(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return translate(err)
	}
	if n != 1 {
		return ErrNotSending
	}
	return nil
}

func providerScope(channel model.Channel, name string) string {
	return fmt.Sprintf("provider:%s:%s", channel, name)
}

type rowScanner interface{ Scan(dest ...any) error }

func scanClaimed(row rowScanner) (ClaimedMessage, error) {
	var m ClaimedMessage
	var hold, errType, prov, provID, subject, text, html, replyTo, sms sql.NullString
	var created, expires int64
	var sent, final, next sql.NullInt64
	if err := row.Scan(&m.ID, &m.RequestID, &m.App, &m.Channel, &m.Recipient, &m.Sender, &m.State, &hold,
		&m.Attempts, &errType, &prov, &provID, &m.BodyBytes, &created, &sent, &final, &expires, &next,
		&subject, &text, &html, &replyTo, &sms); err != nil {
		return m, err
	}
	m.HoldReason = strPtr(hold)
	if errType.Valid {
		et := model.ErrorType(errType.String)
		m.ErrorType = &et
	}
	m.Provider, m.ProviderMessageID = strPtr(prov), strPtr(provID)
	m.CreatedAt, m.ExpiresAt = time.UnixMilli(created).UTC(), time.UnixMilli(expires).UTC()
	m.SentAt, m.FinalAt, m.NextAttemptAt = timePtr(sent), timePtr(final), timePtr(next)
	m.Body = provider.Body{Subject: subject.String, Text: text.String, HTML: html.String, ReplyTo: replyTo.String, SMSBody: sms.String}
	return m, nil
}

func strPtr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	return &v.String
}

func timePtr(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.UnixMilli(v.Int64).UTC()
	return &t
}
