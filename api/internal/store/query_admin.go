package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/davcs86/commsgun/api/internal/config"
	"github.com/davcs86/commsgun/api/internal/constants"
	"github.com/davcs86/commsgun/api/internal/model"
)

var (
	ErrNotFound  = errors.New("store: not found")
	ErrBadCursor = errors.New("store: invalid cursor")
)

// Filter narrows admin queries; empty strings mean "any". Recipient and Sender are substring matches.
type Filter struct {
	From, To                                          time.Time
	Channel, App, State, ErrorType, Recipient, Sender string
}

type Totals struct {
	Enqueued   int `json:"enqueued"`
	Sent       int `json:"sent"`
	Delivered  int `json:"delivered"`
	Bounced    int `json:"bounced"`
	Complained int `json:"complained"`
	Failed     int `json:"failed"`
	Expired    int `json:"expired"`
	Retries    int `json:"retries"`
	QueuedNow  int `json:"queued_now"`
}

type SeriesPoint struct {
	T          time.Time `json:"t"`
	Enqueued   int       `json:"enqueued"`
	Sent       int       `json:"sent"`
	Delivered  int       `json:"delivered"`
	Bounced    int       `json:"bounced"`
	Complained int       `json:"complained"`
	Failed     int       `json:"failed"`
	Expired    int       `json:"expired"`
	Retries    int       `json:"retries"`
}

type ErrorTypeCount struct {
	ErrorType string `json:"error_type"`
	Count     int    `json:"count"`
}

type AppCount struct {
	App   string `json:"app"`
	Count int    `json:"count"`
}

type ChannelCount struct {
	Channel string `json:"channel"`
	Count   int    `json:"count"`
}

type Stats struct {
	From          time.Time        `json:"from"`
	To            time.Time        `json:"to"`
	BucketSeconds int64            `json:"bucket_seconds"`
	Totals        Totals           `json:"totals"`
	Series        []SeriesPoint    `json:"series"`
	ByErrorType   []ErrorTypeCount `json:"by_error_type"`
	ByApp         []AppCount       `json:"by_app"`
	ByChannel     []ChannelCount   `json:"by_channel"`
}

// BucketFor returns the series bucket size for a range of length d.
func BucketFor(d time.Duration) time.Duration {
	for _, st := range constants.BucketSteps {
		if d <= st.MaxRange {
			return st.Bucket
		}
	}
	return constants.BucketLast
}

// escapeLike folds ASCII letters only, matching SQLite's lower(), and escapes LIKE wildcards.
func escapeLike(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(string(b))
}

// where builds the message-level conditions on alias m; error_type is handled by each caller.
func (f Filter) where() (conds []string, args []any) {
	if f.Channel != "" {
		conds, args = append(conds, `m.channel=?`), append(args, f.Channel)
	}
	if f.App != "" {
		conds, args = append(conds, `m.app=?`), append(args, f.App)
	}
	if f.State != "" {
		conds, args = append(conds, `m.state=?`), append(args, f.State)
	}
	if f.Recipient != "" {
		conds, args = append(conds, `lower(m.recipient) LIKE ? ESCAPE '\'`), append(args, "%"+escapeLike(f.Recipient)+"%")
	}
	if f.Sender != "" {
		conds, args = append(conds, `lower(m.sender) LIKE ? ESCAPE '\'`), append(args, "%"+escapeLike(f.Sender)+"%")
	}
	return conds, args
}

func joinAnd(conds []string) string {
	if len(conds) == 0 {
		return "1=1"
	}
	return strings.Join(conds, " AND ")
}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// metric is one counted event kind: which table and timestamp column place it in time.
type metric struct {
	attempts bool
	col      string
	extra    string
	extraArg any
}

func bucketCounts(ctx context.Context, qr querier, f Filter, m metric, bucketMS int64) (map[int64]int, error) {
	conds, args := f.where()
	from := `messages m`
	if m.attempts {
		from = `attempts a JOIN messages m ON m.id=a.message_id`
	}
	if m.extra != "" {
		conds, args = append(conds, m.extra), append(args, m.extraArg)
	}
	if f.ErrorType != "" {
		if m.attempts {
			conds, args = append(conds, `(m.error_type=? OR a.error_type=?)`), append(args, f.ErrorType, f.ErrorType)
		} else {
			conds, args = append(conds, `m.error_type=?`), append(args, f.ErrorType)
		}
	}
	conds = append(conds, m.col+`>=?`, m.col+`<?`)
	args = append(args, f.From.UnixMilli(), f.To.UnixMilli())
	q := fmt.Sprintf(`SELECT (%s/?)*? AS b, COUNT(*) FROM %s WHERE %s GROUP BY b`, m.col, from, joinAnd(conds))
	rows, err := qr.QueryContext(ctx, q, append([]any{bucketMS, bucketMS}, args...)...)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var b int64
		var n int
		if err := rows.Scan(&b, &n); err != nil {
			return nil, translate(err)
		}
		out[b] = n
	}
	return out, translate(rows.Err())
}

// Stats counts events by when they happened inside whole buckets covering the request;
// queued_now ignores the range. Reads share one snapshot.
func (s *Store) Stats(ctx context.Context, f Filter, now time.Time) (Stats, error) {
	bucket := BucketFor(f.To.Sub(f.From))
	bms := bucket.Milliseconds()
	ceil := func(v int64) int64 { return (v + bms - 1) / bms * bms }
	to := ceil(f.To.UTC().UnixMilli())
	f.From, f.To = time.UnixMilli(to-ceil(f.To.Sub(f.From).Milliseconds())).UTC(), time.UnixMilli(to).UTC()
	st := Stats{From: f.From, To: f.To, BucketSeconds: int64(bucket / time.Second),
		Series: []SeriesPoint{}, ByErrorType: []ErrorTypeCount{}, ByApp: []AppCount{}, ByChannel: []ChannelCount{}}

	tx, err := s.r.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Stats{}, translate(err)
	}
	defer tx.Rollback()
	var qr querier = tx

	index := map[int64]int{}
	for b := f.From.UnixMilli(); b < to; b += bms {
		index[b] = len(st.Series)
		st.Series = append(st.Series, SeriesPoint{T: time.UnixMilli(b).UTC()})
	}

	final := func(state model.State) metric {
		return metric{col: `m.final_at`, extra: `m.state=?`, extraArg: string(state)}
	}
	type target struct {
		m   metric
		dst func(*SeriesPoint) *int
		tot *int
	}
	targets := []target{
		{metric{col: `m.created_at`}, func(p *SeriesPoint) *int { return &p.Enqueued }, &st.Totals.Enqueued},
		{metric{col: `m.sent_at`}, func(p *SeriesPoint) *int { return &p.Sent }, &st.Totals.Sent},
		{final(model.StateDelivered), func(p *SeriesPoint) *int { return &p.Delivered }, &st.Totals.Delivered},
		{final(model.StateBounced), func(p *SeriesPoint) *int { return &p.Bounced }, &st.Totals.Bounced},
		{final(model.StateComplained), func(p *SeriesPoint) *int { return &p.Complained }, &st.Totals.Complained},
		{final(model.StateFailed), func(p *SeriesPoint) *int { return &p.Failed }, &st.Totals.Failed},
		{final(model.StateExpired), func(p *SeriesPoint) *int { return &p.Expired }, &st.Totals.Expired},
		{metric{attempts: true, col: `a.started_at`, extra: `a.n>?`, extraArg: 1}, func(p *SeriesPoint) *int { return &p.Retries }, &st.Totals.Retries},
	}
	for _, t := range targets {
		counts, err := bucketCounts(ctx, qr, f, t.m, bms)
		if err != nil {
			return Stats{}, err
		}
		for b, n := range counts {
			i, ok := index[b]
			if !ok {
				continue
			}
			*t.dst(&st.Series[i]) += n
			*t.tot += n
		}
	}

	if st.Totals.QueuedNow, err = queuedNow(ctx, qr, f); err != nil {
		return Stats{}, err
	}
	if st.ByErrorType, err = errorBreakdown(ctx, qr, f); err != nil {
		return Stats{}, err
	}
	if st.ByApp, st.ByChannel, err = enqueuedBreakdown(ctx, qr, f); err != nil {
		return Stats{}, err
	}
	return st, nil
}

func queuedNow(ctx context.Context, qr querier, f Filter) (int, error) {
	conds, args := f.where()
	conds = append(conds, `m.state IN (?,?)`)
	args = append(args, string(model.StateQueued), string(model.StateSending))
	if f.ErrorType != "" {
		conds, args = append(conds, `m.error_type=?`), append(args, f.ErrorType)
	}
	var n int
	err := qr.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages m WHERE `+joinAnd(conds), args...).Scan(&n)
	return n, translate(err)
}

func errorBreakdown(ctx context.Context, qr querier, f Filter) ([]ErrorTypeCount, error) {
	counts := map[string]int{}
	add := func(q string, args ...any) error {
		rows, err := qr.QueryContext(ctx, q, args...)
		if err != nil {
			return translate(err)
		}
		defer rows.Close()
		for rows.Next() {
			var et string
			var n int
			if err := rows.Scan(&et, &n); err != nil {
				return translate(err)
			}
			counts[et] += n
		}
		return translate(rows.Err())
	}

	conds, args := f.where()
	conds = append(conds, `a.outcome IN (?,?)`, `a.error_type IS NOT NULL`, `a.started_at>=?`, `a.started_at<?`)
	args = append(args, string(model.OutcomeRetryable), string(model.OutcomePermanent), f.From.UnixMilli(), f.To.UnixMilli())
	if f.ErrorType != "" {
		conds, args = append(conds, `(m.error_type=? OR a.error_type=?)`), append(args, f.ErrorType, f.ErrorType)
	}
	if err := add(`SELECT a.error_type, COUNT(*) FROM attempts a JOIN messages m ON m.id=a.message_id WHERE `+joinAnd(conds)+` GROUP BY a.error_type`, args...); err != nil {
		return nil, err
	}

	// A poll-rejected message has no attempt carrying its error type; a send-rejected one was counted above.
	conds, args = f.where()
	conds = append(conds, `(m.error_type IN (?,?,?) OR (m.state=? AND m.error_type=?
		AND NOT EXISTS (SELECT 1 FROM attempts a WHERE a.message_id=m.id AND a.error_type=m.error_type)))`,
		`m.final_at>=?`, `m.final_at<?`)
	args = append(args, string(model.ErrBounced), string(model.ErrComplained), string(model.ErrExpired),
		string(model.StateFailed), string(model.ErrProviderRejected), f.From.UnixMilli(), f.To.UnixMilli())
	if f.ErrorType != "" {
		conds, args = append(conds, `m.error_type=?`), append(args, f.ErrorType)
	}
	if err := add(`SELECT m.error_type, COUNT(*) FROM messages m WHERE `+joinAnd(conds)+` GROUP BY m.error_type`, args...); err != nil {
		return nil, err
	}

	out := make([]ErrorTypeCount, 0, len(counts))
	for et, n := range counts {
		out = append(out, ErrorTypeCount{et, n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].ErrorType < out[j].ErrorType
	})
	return out, nil
}

func enqueuedBreakdown(ctx context.Context, qr querier, f Filter) ([]AppCount, []ChannelCount, error) {
	conds, args := f.where()
	conds = append(conds, `m.created_at>=?`, `m.created_at<?`)
	args = append(args, f.From.UnixMilli(), f.To.UnixMilli())
	if f.ErrorType != "" {
		conds, args = append(conds, `m.error_type=?`), append(args, f.ErrorType)
	}
	w := joinAnd(conds)
	apps, chans := []AppCount{}, []ChannelCount{}

	rows, err := qr.QueryContext(ctx, `SELECT m.app, COUNT(*) AS n FROM messages m WHERE `+w+` GROUP BY m.app ORDER BY n DESC, m.app`, args...)
	if err != nil {
		return nil, nil, translate(err)
	}
	for rows.Next() {
		var a AppCount
		if err := rows.Scan(&a.App, &a.Count); err != nil {
			rows.Close()
			return nil, nil, translate(err)
		}
		apps = append(apps, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, translate(err)
	}

	rows, err = qr.QueryContext(ctx, `SELECT m.channel, COUNT(*) AS n FROM messages m WHERE `+w+` GROUP BY m.channel ORDER BY n DESC, m.channel`, args...)
	if err != nil {
		return nil, nil, translate(err)
	}
	defer rows.Close()
	for rows.Next() {
		var c ChannelCount
		if err := rows.Scan(&c.Channel, &c.Count); err != nil {
			return nil, nil, translate(err)
		}
		chans = append(chans, c)
	}
	return apps, chans, translate(rows.Err())
}

const adminMessageCols = `m.id, m.request_id, m.app, m.channel, m.recipient, m.sender, m.state, m.hold_reason, m.attempts,
	m.error_type, m.provider, m.provider_message_id, m.body_bytes, m.created_at, m.sent_at, m.final_at, m.expires_at, m.next_attempt_at`

func scanMessage(row rowScanner) (model.Message, error) {
	var m model.Message
	var hold, errType, prov, provID sql.NullString
	var created, expires int64
	var sent, final, next sql.NullInt64
	if err := row.Scan(&m.ID, &m.RequestID, &m.App, &m.Channel, &m.Recipient, &m.Sender, &m.State, &hold, &m.Attempts,
		&errType, &prov, &provID, &m.BodyBytes, &created, &sent, &final, &expires, &next); err != nil {
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
	return m, nil
}

func encodeCursor(created int64, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(created, 10) + "," + id))
}

func decodeCursor(c string) (int64, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return 0, "", ErrBadCursor
	}
	ts, id, ok := strings.Cut(string(raw), ",")
	n, perr := strconv.ParseInt(ts, 10, 64)
	if !ok || perr != nil || id == "" {
		return 0, "", ErrBadCursor
	}
	return n, id, nil
}

// ListMessages pages newest-first by creation time; the returned cursor is "" on the last page.
func (s *Store) ListMessages(ctx context.Context, f Filter, cursor string, limit int) ([]model.Message, string, error) {
	if limit <= 0 {
		limit = constants.AdminPageSize
	}
	conds, args := f.where()
	conds = append(conds, `m.created_at>=?`, `m.created_at<?`)
	args = append(args, f.From.UTC().UnixMilli(), f.To.UTC().UnixMilli())
	if f.ErrorType != "" {
		conds = append(conds, `(m.error_type=? OR EXISTS (SELECT 1 FROM attempts a WHERE a.message_id=m.id AND a.error_type=?))`)
		args = append(args, f.ErrorType, f.ErrorType)
	}
	if cursor != "" {
		ts, id, err := decodeCursor(cursor)
		if err != nil {
			return nil, "", err
		}
		conds = append(conds, `(m.created_at<? OR (m.created_at=? AND m.id<?))`)
		args = append(args, ts, ts, id)
	}
	args = append(args, limit+1)
	rows, err := s.r.QueryContext(ctx, `SELECT `+adminMessageCols+` FROM messages m WHERE `+joinAnd(conds)+
		` ORDER BY m.created_at DESC, m.id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, "", translate(err)
	}
	defer rows.Close()
	items := []model.Message{}
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, "", translate(err)
		}
		items = append(items, m)
	}
	if err := rows.Err(); err != nil {
		return nil, "", translate(err)
	}
	next := ""
	if len(items) > limit {
		items = items[:limit]
		last := items[limit-1]
		next = encodeCursor(last.CreatedAt.UnixMilli(), last.ID)
	}
	return items, next, nil
}

// GetMessage returns one message and its attempts in order, or ErrNotFound.
func (s *Store) GetMessage(ctx context.Context, id string) (model.Message, []model.Attempt, error) {
	m, err := scanMessage(s.r.QueryRowContext(ctx, `SELECT `+adminMessageCols+` FROM messages m WHERE m.id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return m, nil, ErrNotFound
	}
	if err != nil {
		return m, nil, translate(err)
	}
	rows, err := s.r.QueryContext(ctx, `SELECT n, started_at, duration_ms, outcome, error_type, provider_status, provider_error, message
		FROM attempts WHERE message_id=? ORDER BY n`, id)
	if err != nil {
		return m, nil, translate(err)
	}
	defer rows.Close()
	attempts := []model.Attempt{}
	for rows.Next() {
		var a model.Attempt
		var started int64
		var et, perr sql.NullString
		var pstatus sql.NullInt64
		if err := rows.Scan(&a.N, &started, &a.DurationMS, &a.Outcome, &et, &pstatus, &perr, &a.Message); err != nil {
			return m, nil, translate(err)
		}
		a.StartedAt = time.UnixMilli(started).UTC()
		if et.Valid {
			e := model.ErrorType(et.String)
			a.ErrorType = &e
		}
		if pstatus.Valid {
			v := int(pstatus.Int64)
			a.ProviderStatus = &v
		}
		a.ProviderError = strPtr(perr)
		attempts = append(attempts, a)
	}
	return m, attempts, translate(rows.Err())
}

type Facets struct {
	Apps       []string `json:"apps"`
	Senders    []string `json:"senders"`
	ErrorTypes []string `json:"error_types"`
	Channels   []string `json:"channels"`
	States     []string `json:"states"`
}

func (s *Store) distinct(ctx context.Context, q string) ([]string, error) {
	rows, err := s.r.QueryContext(ctx, q, constants.AdminFacetCap)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, translate(err)
		}
		out = append(out, v)
	}
	return out, translate(rows.Err())
}

// Facets lists the distinct apps, senders and error types present, each sorted and capped.
func (s *Store) Facets(ctx context.Context) (Facets, error) {
	f := Facets{
		Channels: []string{string(model.ChannelEmail), string(model.ChannelSMS)},
		States: []string{string(model.StateQueued), string(model.StateSending), string(model.StateSent), string(model.StateDelivered),
			string(model.StateBounced), string(model.StateComplained), string(model.StateFailed), string(model.StateExpired)},
	}
	var err error
	if f.Apps, err = s.distinct(ctx, `SELECT DISTINCT app FROM messages ORDER BY app LIMIT ?`); err != nil {
		return f, err
	}
	if f.Senders, err = s.distinct(ctx, `SELECT DISTINCT sender FROM messages WHERE sender<>'' ORDER BY sender LIMIT ?`); err != nil {
		return f, err
	}
	f.ErrorTypes, err = s.distinct(ctx, `SELECT error_type FROM (SELECT error_type FROM messages UNION SELECT error_type FROM attempts)
		WHERE error_type IS NOT NULL ORDER BY error_type LIMIT ?`)
	return f, err
}

type Usage struct {
	Used  int `json:"used"`
	Limit int `json:"limit"`
}

type DayUsage struct {
	Used     int       `json:"used"`
	Limit    int       `json:"limit"`
	ResetsAt time.Time `json:"resets_at"`
}

type ProviderStatus struct {
	Channel     string     `json:"channel"`
	Name        *string    `json:"name"`
	Minute      Usage      `json:"minute"`
	Day         DayUsage   `json:"day"`
	PausedUntil *time.Time `json:"paused_until"`
	PauseReason *string    `json:"pause_reason"`
	Held        int        `json:"held"`
}

type Overview struct {
	QueueDepth      int              `json:"queue_depth"`
	OldestWaitingAt *time.Time       `json:"oldest_waiting_at"`
	Providers       []ProviderStatus `json:"providers"`
}

// Overview reports queue depth, the oldest waiting message that is not held and, per channel,
// provider usage against its limits, any pause and held messages.
func (s *Store) Overview(ctx context.Context, now time.Time, cfg *config.Config) (Overview, error) {
	now = now.UTC()
	ov := Overview{Providers: []ProviderStatus{}}
	var oldest sql.NullInt64
	if err := s.r.QueryRowContext(ctx, `SELECT COUNT(*), MIN(CASE WHEN hold_reason IS NULL THEN created_at END) FROM messages WHERE state IN (?,?)`,
		string(model.StateQueued), string(model.StateSending)).Scan(&ov.QueueDepth, &oldest); err != nil {
		return ov, translate(err)
	}
	ov.OldestWaitingAt = timePtr(oldest)

	for _, ch := range []model.Channel{model.ChannelEmail, model.ChannelSMS} {
		ps := ProviderStatus{Channel: string(ch)}
		var perMinute, perDay int
		var name string
		if p := cfg.Providers[ch]; p != nil {
			name, perMinute, perDay = p.Name, p.PerMinute, p.PerDay
			ps.Name = &name
		}
		for _, w := range windowsAt(now, perMinute, perDay) {
			used := 0
			if ps.Name != nil {
				err := s.r.QueryRowContext(ctx, `SELECT count FROM rate_counters WHERE scope=? AND window_kind=? AND window_start=?`,
					providerScope(ch, name), w.kind, w.start.UnixMilli()).Scan(&used)
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return ov, translate(err)
				}
			}
			if w.kind == windowMinute {
				ps.Minute = Usage{used, w.limit}
			} else {
				ps.Day = DayUsage{used, w.limit, w.reset}
			}
		}
		if ps.Name != nil {
			var until int64
			var reason string
			err := s.r.QueryRowContext(ctx, `SELECT until, reason FROM provider_pauses WHERE provider=? AND until>?`,
				providerScope(ch, name), now.UnixMilli()).Scan(&until, &reason)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return ov, translate(err)
			}
			if err == nil {
				t := time.UnixMilli(until).UTC()
				ps.PausedUntil, ps.PauseReason = &t, &reason
			}
		}
		if err := s.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE state=? AND channel=? AND hold_reason IS NOT NULL`,
			string(model.StateQueued), string(ch)).Scan(&ps.Held); err != nil {
			return ov, translate(err)
		}
		ov.Providers = append(ov.Providers, ps)
	}
	return ov, nil
}
