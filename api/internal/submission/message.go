// Package submission maps one SMTP DATA payload onto one enqueue request, and enqueue outcomes onto SMTP replies.
package submission

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/http"
	"net/mail"
	"strings"
	"unicode/utf8"

	"github.com/davcs86/commsgun/api/internal/config"
	"github.com/davcs86/commsgun/api/internal/enqueue"
	"github.com/davcs86/commsgun/api/internal/model"
)

// Namespace prefixes every SMTP idempotency key (API-09).
const Namespace = "smtp"

// Reply is one SMTP reply; Text is fixed per reply and never carries message data.
type Reply struct {
	Code     int
	Enhanced [3]int
	Text     string
}

var (
	replyOK          = Reply{250, [3]int{2, 0, 0}, "Message queued"}
	replyNotAllowed  = Reply{550, [3]int{5, 7, 1}, "Not allowed for this app"}
	replyContent     = Reply{554, [3]int{5, 6, 0}, "Message content not supported"}
	replyRateLimited = Reply{451, [3]int{4, 7, 1}, "Rate limited, try again later"}
	replyBusy        = Reply{451, [3]int{4, 3, 2}, "Busy, try again later"}
	replyDefect      = Reply{451, [3]int{4, 3, 0}, "Temporary failure"}
)

// ReplyFor maps an enqueue outcome to the DATA reply of spec 12.8; nil is queued or a duplicate.
func ReplyFor(f *enqueue.Failure) Reply {
	if f == nil {
		return replyOK
	}
	switch {
	case f.Code == "channel_not_allowed", f.Code == "sender_not_allowed":
		return replyNotAllowed
	case f.Code == "invalid_subject", f.Code == "body_required", f.Code == "invalid_reply_to",
		f.Code == "idempotency_conflict":
		return replyContent
	case f.Status == http.StatusTooManyRequests:
		return replyRateLimited
	case f.Status == http.StatusServiceUnavailable:
		return replyBusy
	}
	return replyDefect
}

// request is the spec 6.2 email request; it never has expires_at, so a resend hashes the same.
type request struct {
	Channel string   `json:"channel"`
	To      []string `json:"to"`
	From    string   `json:"from"`
	Subject string   `json:"subject"`
	Text    string   `json:"text,omitempty"`
	HTML    string   `json:"html,omitempty"`
	ReplyTo *string  `json:"reply_to,omitempty"`
}

// singleHeaders may appear at most once.
var singleHeaders = []string{"From", "Subject", "Reply-To", "Message-Id"}

// BuildRequest turns DATA into the enqueue body and the hex SHA-256 of the trimmed Message-ID.
// Recipients come from the envelope only; rej is set instead when the message is refused.
func BuildRequest(raw []byte, mailFrom string, rcpts []string, app *config.App) (body []byte, key string, rej *Reply) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return refuse(replyContent)
	}
	h := msg.Header
	for _, name := range singleHeaders {
		if len(h[name]) > 1 {
			return refuse(replyContent)
		}
	}
	id := strings.TrimSpace(h.Get("Message-Id"))
	if id == "" {
		return refuse(replyContent)
	}
	from, err := mail.ParseAddress(h.Get("From"))
	if err != nil || !strings.EqualFold(from.Address, mailFrom) || !allowedSender(app, mailFrom) {
		return refuse(replyNotAllowed)
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(h.Get("Subject"))
	if err != nil || strings.ContainsAny(subject, "\r\n") || !utf8.ValidString(subject) {
		return refuse(replyContent)
	}
	req := request{Channel: string(model.ChannelEmail), To: rcpts, From: from.String(), Subject: subject}
	if v := h["Reply-To"]; len(v) == 1 {
		a, err := mail.ParseAddress(v[0])
		if err != nil {
			return refuse(replyContent)
		}
		s := a.String()
		req.ReplyTo = &s
	}
	if !readBody(h.Get("Content-Type"), h.Get("Content-Transfer-Encoding"), h.Get("Content-Disposition"), msg.Body, &req) {
		return refuse(replyContent)
	}
	if body, err = json.Marshal(req); err != nil {
		return refuse(replyDefect)
	}
	sum := sha256.Sum256([]byte(id))
	return body, hex.EncodeToString(sum[:]), nil
}

func refuse(r Reply) ([]byte, string, *Reply) { return nil, "", &r }

func allowedSender(app *config.App, addr string) bool {
	for _, s := range app.Channels[model.ChannelEmail].Senders {
		if strings.EqualFold(s.Address, addr) {
			return true
		}
	}
	return false
}

// mediaType defaults an absent Content-Type to text/plain; us-ascii (RFC 2045).
func mediaType(v string) (string, map[string]string, error) {
	if strings.TrimSpace(v) == "" {
		return "text/plain", map[string]string{}, nil
	}
	return mime.ParseMediaType(v)
}

func identityEncoding(cte string) bool {
	switch strings.ToLower(strings.TrimSpace(cte)) {
	case "", "7bit", "8bit":
		return true
	}
	return false
}

// readBody accepts one text/plain and/or one text/html, single or in one multipart/alternative.
func readBody(ctype, cte, disp string, r io.Reader, req *request) bool {
	mt, params, err := mediaType(ctype)
	if err != nil {
		return false
	}
	if mt != "multipart/alternative" {
		return textPart(mt, params, cte, disp, r, req, map[string]bool{})
	}
	if params["boundary"] == "" || !identityEncoding(cte) {
		return false
	}
	seen := map[string]bool{}
	mr := multipart.NewReader(r, params["boundary"])
	for {
		p, err := mr.NextRawPart()
		if err == io.EOF {
			return true
		}
		if err != nil {
			return false
		}
		pmt, pparams, err := mediaType(p.Header.Get("Content-Type"))
		if err != nil || !textPart(pmt, pparams, p.Header.Get("Content-Transfer-Encoding"),
			p.Header.Get("Content-Disposition"), p, req, seen) {
			return false
		}
	}
}

func textPart(mt string, params map[string]string, cte, disp string, r io.Reader, req *request, seen map[string]bool) bool {
	if disp != "" {
		if d, _, err := mime.ParseMediaType(disp); err != nil || d != "inline" {
			return false
		}
	}
	var dst *string
	switch mt {
	case "text/plain":
		dst = &req.Text
	case "text/html":
		dst = &req.HTML
	default:
		return false
	}
	if seen[mt] {
		return false
	}
	seen[mt] = true
	switch strings.ToLower(strings.TrimSpace(cte)) {
	case "", "7bit", "8bit":
	case "quoted-printable":
		r = quotedprintable.NewReader(r)
	case "base64":
		r = base64.NewDecoder(base64.StdEncoding, r)
	default:
		return false
	}
	b, err := io.ReadAll(r)
	if err != nil || !validCharset(strings.ToLower(params["charset"]), b) {
		return false
	}
	*dst = string(b)
	return true
}

func validCharset(charset string, b []byte) bool {
	switch charset {
	case "", "us-ascii":
		for _, c := range b {
			if c >= utf8.RuneSelf {
				return false
			}
		}
		return true
	case "utf-8":
		return utf8.Valid(b)
	}
	return false
}
