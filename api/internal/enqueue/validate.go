// Package enqueue serves POST /v1/messages: authentication, validation and the hand-off to the store.
package enqueue

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/davcs86/commsgun/api/internal/config"
	"github.com/davcs86/commsgun/api/internal/constants"
	"github.com/davcs86/commsgun/api/internal/model"
	"github.com/davcs86/commsgun/api/internal/store"
)

const (
	fieldTo = "to"
	crlf    = "\r\n"
	// unknownFieldPrefix is encoding/json's DisallowUnknownFields error text before the quoted name.
	unknownFieldPrefix = "json: unknown field "
)

var e164 = regexp.MustCompile(`^\+[1-9][0-9]{1,14}$`)

// ValidationError is rendered as the 422 body {"error": Message, "field": Field}.
type ValidationError struct{ Message, Field string }

// request is also the canonical form that is hashed, so field order and whitespace never matter.
type request struct {
	Channel   string   `json:"channel"`
	To        []string `json:"to"`
	From      *string  `json:"from,omitempty"`
	Subject   string   `json:"subject,omitempty"`
	Text      string   `json:"text,omitempty"`
	HTML      string   `json:"html,omitempty"`
	ReplyTo   *string  `json:"reply_to,omitempty"`
	Body      string   `json:"body,omitempty"`
	ExpiresAt *string  `json:"expires_at,omitempty"`
}

func invalid(msg, field string) (*store.EnqueueInput, *ValidationError) {
	return nil, &ValidationError{Message: msg, Field: field}
}

// parse decodes the body and hashes its canonical form; it depends on neither time nor config.
func parse(raw []byte) (request, [32]byte, *ValidationError) {
	var req request
	var zero [32]byte
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) {
			return req, zero, &ValidationError{Message: "invalid_json", Field: te.Field}
		}
		if quoted, ok := strings.CutPrefix(err.Error(), unknownFieldPrefix); ok {
			name, _ := strconv.Unquote(quoted)
			return req, zero, &ValidationError{Message: "unknown_field", Field: name}
		}
		return req, zero, &ValidationError{Message: "invalid_json"}
	}
	if dec.More() {
		return req, zero, &ValidationError{Message: "invalid_json"}
	}
	canonical, err := json.Marshal(req)
	if err != nil {
		return req, zero, &ValidationError{Message: "invalid_json"}
	}
	return req, sha256.Sum256(canonical), nil
}

// Validate fills the request-derived fields of EnqueueInput; the caller owns key, source and limits.
func Validate(app *config.App, raw []byte, now time.Time) (*store.EnqueueInput, *ValidationError) {
	req, hash, verr := parse(raw)
	if verr != nil {
		return nil, verr
	}
	return validateRequest(app, &req, hash, now)
}

func validateRequest(app *config.App, reqp *request, hash [32]byte, now time.Time) (*store.EnqueueInput, *ValidationError) {
	req := *reqp
	ch := model.Channel(req.Channel)
	if ch != model.ChannelEmail && ch != model.ChannelSMS {
		return invalid("invalid_channel", "channel")
	}
	in := &store.EnqueueInput{App: app.ID, Channel: ch, BodyHash: hash}

	if n := len(req.To); n < 1 || n > constants.MaxRecipients {
		return invalid("invalid_recipient_count", fieldTo)
	}
	rcpts, verr := recipients(ch, req.To)
	if verr != nil {
		return nil, verr
	}
	in.Recipients = rcpts

	expires, verr := expiry(req.ExpiresAt, now)
	if verr != nil {
		return nil, verr
	}
	in.ExpiresAt = expires

	cfg, enabled := app.Channels[ch]
	if enabled && (len(rcpts) > cfg.PerMinute || len(rcpts) > cfg.PerDay) {
		return invalid("too_many_recipients_for_limit", fieldTo)
	}

	if ch == model.ChannelSMS {
		if n := utf8.RuneCountInString(req.Body); n < 1 || n > constants.SMSBodyMaxLen {
			return invalid("invalid_body", "body")
		}
		in.SMSBody = req.Body
		return in, nil
	}
	if verr := emailContent(in, &req, cfg, enabled); verr != nil {
		return nil, verr
	}
	return in, nil
}

func recipients(ch model.Channel, to []string) ([]string, *ValidationError) {
	seen := make(map[string]bool, len(to))
	out := make([]string, 0, len(to))
	for _, r := range to {
		if !validRecipient(ch, r) {
			return nil, &ValidationError{Message: "invalid_recipient", Field: fieldTo}
		}
		k := strings.ToLower(r)
		if !seen[k] {
			seen[k] = true
			out = append(out, r)
		}
	}
	return out, nil
}

func validRecipient(ch model.Channel, r string) bool {
	if ch == model.ChannelSMS {
		return e164.MatchString(r)
	}
	if strings.ContainsAny(r, crlf) {
		return false
	}
	a, err := mail.ParseAddress(r)
	return err == nil && a.Address == r
}

func expiry(raw *string, now time.Time) (time.Time, *ValidationError) {
	limit := now.Add(constants.MaxSendWindow)
	if raw == nil {
		return limit, nil
	}
	t, err := time.Parse(time.RFC3339, *raw)
	if err != nil || !t.After(now) || t.After(limit) {
		return time.Time{}, &ValidationError{Message: "invalid_expires_at", Field: "expires_at"}
	}
	return t.UTC(), nil
}

// emailContent skips sender resolution when the channel is disabled; the handler answers 403 then.
func emailContent(in *store.EnqueueInput, req *request, cfg config.ChannelCfg, enabled bool) *ValidationError {
	if req.Subject == "" || strings.ContainsAny(req.Subject, crlf) ||
		utf8.RuneCountInString(req.Subject) > constants.SubjectMaxLen {
		return &ValidationError{Message: "invalid_subject", Field: "subject"}
	}
	if req.Text == "" && req.HTML == "" {
		return &ValidationError{Message: "body_required", Field: "text"}
	}
	in.Subject, in.Text, in.HTML = req.Subject, req.Text, req.HTML

	if req.ReplyTo != nil {
		if strings.ContainsAny(*req.ReplyTo, crlf) {
			return &ValidationError{Message: "invalid_reply_to", Field: "reply_to"}
		}
		if _, err := mail.ParseAddress(*req.ReplyTo); err != nil {
			return &ValidationError{Message: "invalid_reply_to", Field: "reply_to"}
		}
		in.ReplyTo = *req.ReplyTo
	}

	if !enabled {
		return nil
	}
	if req.From == nil {
		if cfg.DefaultSender == nil {
			return &ValidationError{Message: "sender_required", Field: "from"}
		}
		in.Sender = cfg.DefaultSender.Raw
		return nil
	}
	if strings.ContainsAny(*req.From, crlf) {
		return &ValidationError{Message: "sender_not_allowed", Field: "from"}
	}
	a, err := mail.ParseAddress(*req.From)
	if err != nil {
		return &ValidationError{Message: "sender_not_allowed", Field: "from"}
	}
	addr := strings.ToLower(a.Address)
	for _, s := range cfg.Senders {
		if s.Address == addr {
			in.Sender = *req.From
			return nil
		}
	}
	return &ValidationError{Message: "sender_not_allowed", Field: "from"}
}
