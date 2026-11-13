package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/davcs86/commsgun/api/internal/constants"
	"github.com/davcs86/commsgun/api/internal/model"
	"github.com/google/uuid"
)

const (
	windowMinute = "minute"
	windowDay    = "day"
	holdNoProv   = "no_provider"
)

var (
	ErrIdempotencyConflict = errors.New("store: idempotency key reused with a different body")
	ErrQueueFull           = errors.New("store: queue full")
)

type RateLimitedError struct {
	Window     string
	RetryAfter time.Duration
}

func (e *RateLimitedError) Error() string { return "store: rate limited (" + e.Window + ")" }

type EnqueueInput struct {
	App            string
	Channel        model.Channel
	IdempotencyKey string
	BodyHash       [32]byte
	Recipients     []string
	Sender         string
	Subject        string
	Text           string
	HTML           string
	ReplyTo        string
	SMSBody        string
	ExpiresAt      time.Time
	SourceIP       string
	PerMinute      int
	PerDay         int
	Provider       string
	Held           bool
}

type EnqueueResult struct {
	RequestID  string
	MessageIDs []string
	Duplicate  bool
}

// Enqueue records the request and one queued message per recipient, or nothing.
func (s *Store) Enqueue(ctx context.Context, now time.Time, in EnqueueInput) (res EnqueueResult, err error) {
	now = now.UTC()
	n := len(in.Recipients)
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return res, translate(err)
	}
	defer func() {
		if err != nil {
			tx.Rollback()
			err = translate(err)
		}
	}()

	var reqID string
	var hash []byte
	switch err = tx.QueryRowContext(ctx,
		`SELECT id, body_hash FROM requests WHERE app=? AND idempotency_key=?`,
		in.App, in.IdempotencyKey).Scan(&reqID, &hash); {
	case err == nil:
		if string(hash) != string(in.BodyHash[:]) {
			return res, ErrIdempotencyConflict
		}
		ids, qerr := messageIDs(ctx, tx, reqID)
		if qerr != nil {
			return res, qerr
		}
		if err = tx.Commit(); err != nil {
			return res, err
		}
		return EnqueueResult{RequestID: reqID, MessageIDs: ids, Duplicate: true}, nil
	case !errors.Is(err, sql.ErrNoRows):
		return res, err
	}

	var depth int
	if err = tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM messages WHERE state IN (?, ?)`,
		model.StateQueued, model.StateSending).Scan(&depth); err != nil {
		return res, err
	}
	if depth+n > constants.MaxQueueDepth {
		return res, ErrQueueFull
	}

	scope := fmt.Sprintf("app:%s:%s", in.App, in.Channel)
	minuteStart := now.Truncate(time.Minute)
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	windows := []struct {
		kind  string
		start time.Time
		limit int
		reset time.Time
	}{
		{windowMinute, minuteStart, in.PerMinute, minuteStart.Add(time.Minute)},
		{windowDay, dayStart, in.PerDay, dayStart.AddDate(0, 0, 1)},
	}
	for _, w := range windows {
		var used int
		qerr := tx.QueryRowContext(ctx,
			`SELECT count FROM rate_counters WHERE scope=? AND window_kind=? AND window_start=?`,
			scope, w.kind, w.start.UnixMilli()).Scan(&used)
		if qerr != nil && !errors.Is(qerr, sql.ErrNoRows) {
			return res, qerr
		}
		if used+n > w.limit {
			return res, &RateLimitedError{Window: w.kind, RetryAfter: w.reset.Sub(now)}
		}
	}
	for _, w := range windows {
		if _, err = tx.ExecContext(ctx,
			`INSERT INTO rate_counters(scope, window_kind, window_start, count) VALUES (?,?,?,?)
			 ON CONFLICT(scope, window_kind, window_start) DO UPDATE SET count = count + excluded.count`,
			scope, w.kind, w.start.UnixMilli(), n); err != nil {
			return res, err
		}
	}

	reqID = uuid.NewString()
	if _, err = tx.ExecContext(ctx,
		`INSERT INTO requests(id, app, idempotency_key, body_hash, created_at) VALUES (?,?,?,?,?)`,
		reqID, in.App, in.IdempotencyKey, in.BodyHash[:], now.UnixMilli()); err != nil {
		return res, err
	}

	var provider, hold any
	if in.Held {
		hold = holdNoProv
	} else {
		provider = in.Provider
	}
	bodyBytes := len(in.Subject) + len(in.Text) + len(in.HTML) + len(in.SMSBody)
	ids := make([]string, 0, n)
	for _, rcpt := range in.Recipients {
		id := uuid.NewString()
		if _, err = tx.ExecContext(ctx,
			`INSERT INTO messages(id, request_id, app, channel, recipient, sender, state, hold_reason, attempts,
			   provider, subject, body_text, body_html, reply_to, sms_body, body_bytes,
			   created_at, updated_at, expires_at, next_attempt_at, source_ip)
			 VALUES (?,?,?,?,?,?,?,?,0,?,?,?,?,?,?,?,?,?,?,?,?)`,
			id, reqID, in.App, in.Channel, rcpt, in.Sender, model.StateQueued, hold,
			provider, nullIfEmpty(in.Subject), nullIfEmpty(in.Text), nullIfEmpty(in.HTML),
			nullIfEmpty(in.ReplyTo), nullIfEmpty(in.SMSBody), bodyBytes,
			now.UnixMilli(), now.UnixMilli(), in.ExpiresAt.UnixMilli(), now.UnixMilli(), in.SourceIP); err != nil {
			return res, err
		}
		ids = append(ids, id)
	}
	if err = tx.Commit(); err != nil {
		return res, err
	}
	return EnqueueResult{RequestID: reqID, MessageIDs: ids}, nil
}

func messageIDs(ctx context.Context, tx *sql.Tx, reqID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM messages WHERE request_id=? ORDER BY rowid`, reqID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
