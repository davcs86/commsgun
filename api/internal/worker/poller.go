package worker

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/davcs86/commsgun/api/internal/clock"
	"github.com/davcs86/commsgun/api/internal/constants"
	"github.com/davcs86/commsgun/api/internal/model"
	"github.com/davcs86/commsgun/api/internal/provider"
	"github.com/davcs86/commsgun/api/internal/store"
)

type pollStore interface {
	DuePolls(ctx context.Context, now time.Time, limit int) ([]model.Message, error)
	RecordPoll(ctx context.Context, now time.Time, id string, s provider.DeliveryStatus) error
	RecordPollError(ctx context.Context, now time.Time, id string) error
	ClearPoll(ctx context.Context, now time.Time, id string) error
}

// CheckerKey names the provider instance that sent a message: its channel and provider name.
type CheckerKey struct {
	Channel  model.Channel
	Provider string
}

// Poller learns the final delivery state of sent emails from their provider.
type Poller struct {
	st       pollStore
	checkers map[CheckerKey]provider.StatusChecker
	clk      clock.Clock
	log      *slog.Logger
}

func NewPoller(st *store.Store, checkers map[CheckerKey]provider.StatusChecker, clk clock.Clock, log *slog.Logger) *Poller {
	return &Poller{st: st, checkers: checkers, clk: clk, log: log}
}

func (p *Poller) Run(ctx context.Context) {
	t := time.NewTicker(constants.WorkerTick)
	defer t.Stop()
	for {
		if err := p.Tick(ctx); err != nil && ctx.Err() == nil {
			p.log.Error("poller tick failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick polls at most PollBatch due messages.
func (p *Poller) Tick(ctx context.Context) error {
	due, err := p.st.DuePolls(ctx, p.clk.Now(), constants.PollBatch)
	if err != nil {
		return err
	}
	var errs []error
	for _, m := range due {
		if ctx.Err() != nil {
			return errors.Join(append(errs, ctx.Err())...)
		}
		if err := p.poll(ctx, m); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (p *Poller) poll(ctx context.Context, m model.Message) error {
	bg := context.WithoutCancel(ctx)
	var checker provider.StatusChecker
	if m.Provider != nil {
		checker = p.checkers[CheckerKey{m.Channel, *m.Provider}]
	}
	if checker == nil || m.ProviderMessageID == nil {
		p.log.Warn("message cannot be polled; polling stopped", "message_id", m.ID)
		return p.st.ClearPoll(bg, p.clk.Now(), m.ID)
	}

	sctx, cancel := context.WithTimeout(ctx, constants.SendTimeout)
	status, err := checker.Status(sctx, *m.ProviderMessageID)
	cancel()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	now := p.clk.Now()
	if err != nil {
		p.log.Warn("status poll failed", "message_id", m.ID, "error", err.Error())
		return p.st.RecordPollError(bg, now, m.ID)
	}
	if err := p.st.RecordPoll(bg, now, m.ID, status); err != nil {
		if errors.Is(err, store.ErrNotSent) {
			p.log.Warn("polled message is no longer sent", "message_id", m.ID)
			return nil
		}
		return err
	}
	p.log.Info("status polled", "message_id", m.ID, "status", statusName(status))
	return nil
}

func statusName(s provider.DeliveryStatus) string {
	switch s {
	case provider.Delivered:
		return string(model.StateDelivered)
	case provider.Bounced:
		return string(model.StateBounced)
	case provider.Complained:
		return string(model.StateComplained)
	case provider.Rejected:
		return string(model.StateFailed)
	}
	return "pending"
}
