package submission

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log"
	"log/slog"
	"net"
	"sync"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"

	"github.com/davcs86/commsgun/api/internal/clock"
	"github.com/davcs86/commsgun/api/internal/config"
	"github.com/davcs86/commsgun/api/internal/constants"
	"github.com/davcs86/commsgun/api/internal/enqueue"
	"github.com/davcs86/commsgun/api/internal/model"
)

var (
	replyNeedTLS     = Reply{530, [3]int{5, 7, 0}, "Must issue a STARTTLS command first"}
	replyNeedAuth    = Reply{530, [3]int{5, 7, 0}, "Authentication required"}
	replyBadRcpt     = Reply{553, [3]int{5, 1, 3}, "Bad recipient address"}
	replyTooManyRcpt = Reply{452, [3]int{4, 5, 3}, "Too many recipients"}
)

func (r Reply) err() *smtp.SMTPError {
	return &smtp.SMTPError{Code: r.Code, EnhancedCode: smtp.EnhancedCode(r.Enhanced), Message: r.Text}
}

type backend struct {
	cfg   *config.Config
	st    enqueue.Enqueuer
	clk   clock.Clock
	log   *slog.Logger
	slots chan struct{}
}

// NewServer is the STARTTLS submission listener of spec 12.8; AUTH is offered only after STARTTLS.
func NewServer(cfg *config.Config, st enqueue.Enqueuer, clk clock.Clock, lg *slog.Logger, tlsCfg *tls.Config) *smtp.Server {
	s := smtp.NewServer(&backend{cfg: cfg, st: st, clk: clk, log: lg, slots: make(chan struct{}, constants.SMTPMaxSessions)})
	s.TLSConfig = tlsCfg.Clone()
	s.TLSConfig.MinVersion = tls.VersionTLS12
	s.MaxMessageBytes = constants.MaxBodyBytes
	s.MaxRecipients = constants.MaxRecipients
	s.ReadTimeout = constants.SMTPReadTimeout
	s.WriteTimeout = constants.WriteTimeout
	// Library text carries peer addresses and raw errors (Rule 4).
	s.ErrorLog = log.New(io.Discard, "", 0)
	return s
}

func (b *backend) NewSession(c *smtp.Conn) (smtp.Session, error) {
	select {
	case b.slots <- struct{}{}:
	default:
		b.log.Warn("smtp session", "outcome", "busy")
		return nil, replyBusy.err()
	}
	_, isTLS := c.TLSConnectionState()
	peer, _, _ := net.SplitHostPort(c.Conn().RemoteAddr().String())
	return &session{b: b, conn: c, tls: isTLS, peer: peer}, nil
}

// session is one go-smtp Session: STARTTLS ends the plaintext one and the next EHLO starts another.
type session struct {
	b    *backend
	conn *smtp.Conn
	tls  bool
	peer string
	once sync.Once

	mu                                 sync.Mutex
	app                                *config.App
	authFailed                         bool
	from                               string
	rcpts                              []string
	mails, recipients, queued, refused int
	defects, lastReply                 int
}

// reply records r for the session log line and returns it as the command's SMTP reply.
func (s *session) reply(r Reply) error {
	s.lastReply = r.Code
	if r.Code >= 300 {
		s.refused++
	}
	if r == replyDefect {
		s.defects++
	}
	return r.err()
}

func (s *session) AuthMechanisms() []string { return []string{sasl.Plain} }

func (s *session) Auth(mech string) (sasl.Server, error) {
	if mech != sasl.Plain {
		return nil, smtp.ErrAuthUnknownMechanism
	}
	return sasl.NewPlainServer(func(identity, username, password string) error {
		app, ok := s.b.cfg.AuthenticateSMTP(username, password)
		s.mu.Lock()
		defer s.mu.Unlock()
		if !ok || (identity != "" && identity != username) {
			s.authFailed, s.lastReply = true, smtp.ErrAuthFailed.Code
			s.refused++
			return smtp.ErrAuthFailed
		}
		s.app, s.lastReply = app, 235
		return nil
	}), nil
}

func (s *session) Mail(from string, _ *smtp.MailOptions) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case !s.tls:
		return s.reply(replyNeedTLS)
	case s.app == nil:
		return s.reply(replyNeedAuth)
	case s.mails >= constants.SMTPMaxMessagesPerConn:
		return s.reply(replyBusy)
	case !allowedSender(s.app, from):
		return s.reply(replyNotAllowed)
	}
	s.mails++
	s.from, s.lastReply = from, 250
	return nil
}

func (s *session) Rcpt(to string, _ *smtp.RcptOptions) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !enqueue.ValidRecipient(model.ChannelEmail, to) {
		return s.reply(replyBadRcpt)
	}
	cc := s.app.Channels[model.ChannelEmail]
	if len(s.rcpts) >= min(constants.MaxRecipients, cc.PerMinute, cc.PerDay) {
		return s.reply(replyTooManyRcpt)
	}
	s.rcpts = append(s.rcpts, to)
	s.recipients++
	s.lastReply = 250
	return nil
}

func (s *session) Data(r io.Reader) error {
	raw, err := io.ReadAll(r)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		var se *smtp.SMTPError
		switch {
		case errors.As(err, &se):
			s.lastReply = se.Code
			s.refused++
			return se
		case errors.Is(err, smtp.ErrTooLongLine):
			return s.reply(replyContent)
		}
		return s.reply(replyBusy)
	}
	body, key, rej := BuildRequest(raw, s.from, s.rcpts, s.app)
	if rej != nil {
		return s.reply(*rej)
	}
	_, f := enqueue.SubmitWindowed(context.Background(), s.b.cfg, s.b.st, s.b.clk.Now(), s.app, Namespace, key, body,
		s.peer, s.app.SendWindow)
	rep := ReplyFor(f)
	if f == nil {
		s.queued++
	}
	return s.reply(rep)
}

func (s *session) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.from, s.rcpts = "", nil
}

// Logout writes the session's only log line: enumerated facts and counts, never an address or a username.
func (s *session) Logout() error {
	s.once.Do(func() {
		<-s.b.slots
		s.mu.Lock()
		defer s.mu.Unlock()
		_, nowTLS := s.conn.TLSConnectionState()
		outcome := "no_auth"
		switch {
		case !s.tls && nowTLS:
			outcome = "starttls"
		case s.app != nil:
			outcome = "authenticated"
		case s.authFailed:
			outcome = "auth_failed"
		}
		attrs := []any{"outcome", outcome, "transactions", s.mails, "recipients", s.recipients,
			"queued", s.queued, "refused", s.refused, "defects", s.defects, "last_reply", s.lastReply}
		if s.app != nil {
			attrs = append(attrs, "app", s.app.ID)
		}
		level := slog.LevelInfo
		if s.defects > 0 {
			level = slog.LevelError
		}
		s.b.log.Log(context.Background(), level, "smtp session", attrs...)
	})
	return nil
}
