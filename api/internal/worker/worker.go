// Package worker dispatches queued messages to providers within their limits.
package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/davcs86/commsgun/api/internal/clock"
	"github.com/davcs86/commsgun/api/internal/config"
	"github.com/davcs86/commsgun/api/internal/constants"
	"github.com/davcs86/commsgun/api/internal/model"
	"github.com/davcs86/commsgun/api/internal/provider"
	"github.com/davcs86/commsgun/api/internal/store"
)

type dispatchStore interface {
	HasDue(ctx context.Context, now time.Time, channel model.Channel, provider string) (bool, error)
	ClaimDue(ctx context.Context, now time.Time, limit int, channel model.Channel, provider string) ([]store.ClaimedMessage, error)
	ProviderReserve(ctx context.Context, now time.Time, channel model.Channel, provider string, perMinute, perDay int) (bool, time.Time, string, error)
	ProviderRelease(ctx context.Context, now time.Time, channel model.Channel, provider string) error
	PauseProvider(ctx context.Context, channel model.Channel, provider string, until time.Time, reason string) error
	RecordAttempt(ctx context.Context, now time.Time, id string, r provider.SendResult, started time.Time, backoff time.Duration, firstPoll time.Time) error
	Unclaim(ctx context.Context, now time.Time, id string) error
	ExpireDue(ctx context.Context, now time.Time) (int, error)
	ExpireClaimed(ctx context.Context, now time.Time, id string) error
	ReleaseHeld(ctx context.Context, channel model.Channel, provider string) (int, error)
}

type Worker struct {
	st        dispatchStore
	providers map[model.Channel]provider.Provider
	cfg       *config.Config
	clk       clock.Clock
	log       *slog.Logger
	rnd       func() float64

	mu       sync.Mutex
	lastTick time.Time
}

// New fails if a provider has no config entry for its channel or its name differs from that entry.
func New(st *store.Store, providers map[model.Channel]provider.Provider, cfg *config.Config, clk clock.Clock, log *slog.Logger, rnd func() float64) (*Worker, error) {
	return newWorker(st, providers, cfg, clk, log, rnd)
}

func newWorker(st dispatchStore, providers map[model.Channel]provider.Provider, cfg *config.Config, clk clock.Clock, log *slog.Logger, rnd func() float64) (*Worker, error) {
	for ch, p := range providers {
		pc := cfg.Providers[ch]
		switch {
		case p == nil:
			return nil, fmt.Errorf("worker: nil provider for channel %s", ch)
		case pc == nil:
			return nil, fmt.Errorf("worker: provider for channel %s has no config entry", ch)
		case pc.Name != p.Name():
			return nil, fmt.Errorf("worker: channel %s provider %q does not match configured %q", ch, p.Name(), pc.Name)
		}
	}
	return &Worker{st: st, providers: providers, cfg: cfg, clk: clk, log: log, rnd: rnd}, nil
}

// Backoff is the delay before attempt+1: min(base·2^(attempt-1), max) with ±BackoffJitter.
func Backoff(attempt int, rnd func() float64) time.Duration {
	d := constants.BackoffBase
	for i := 1; i < attempt && d < constants.BackoffMax; i++ {
		d *= 2
	}
	d = min(d, constants.BackoffMax)
	mult := 1 - constants.BackoffJitter + 2*constants.BackoffJitter*rnd()
	return time.Duration(float64(d) * mult)
}

func (w *Worker) LastTick() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastTick
}

func (w *Worker) Run(ctx context.Context) {
	t := time.NewTicker(constants.WorkerTick)
	defer t.Stop()
	for {
		if err := w.Tick(ctx); err != nil && ctx.Err() == nil {
			w.log.Error("worker tick failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// beat marks the worker live; it runs before each claim and when a pass completes.
func (w *Worker) beat() {
	now := w.clk.Now()
	w.mu.Lock()
	w.lastTick = now
	w.mu.Unlock()
}

// Tick runs one dispatch pass.
func (w *Worker) Tick(ctx context.Context) error {
	start := w.clk.Now()
	if n, err := w.st.ExpireDue(ctx, start); err != nil {
		return err
	} else if n > 0 {
		w.log.Info("messages expired", "count", n)
	}
	for _, ch := range w.channels() {
		if _, err := w.st.ReleaseHeld(ctx, ch, w.providers[ch].Name()); err != nil {
			return err
		}
	}
	for _, ch := range w.channels() {
		if err := w.dispatch(ctx, ch); err != nil {
			return err
		}
	}
	w.beat()
	return nil
}

// channels lists the configured channels in a stable order.
func (w *Worker) channels() []model.Channel {
	var out []model.Channel
	for ch := range w.providers {
		out = append(out, ch)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (w *Worker) dispatch(ctx context.Context, ch model.Channel) error {
	p, limits := w.providers[ch], w.cfg.Providers[ch]
	name := p.Name()
	for range constants.DispatchBatch {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		w.beat()
		now := w.clk.Now()
		if due, err := w.st.HasDue(ctx, now, ch, name); err != nil || !due {
			return err
		}
		ok, _, _, err := w.st.ProviderReserve(ctx, now, ch, name, limits.PerMinute, limits.PerDay)
		if err != nil || !ok {
			return err
		}
		release := func() error { return w.st.ProviderRelease(context.WithoutCancel(ctx), now, ch, name) }
		claimed, err := w.st.ClaimDue(ctx, now, 1, ch, name)
		if err != nil || len(claimed) == 0 {
			return errors.Join(err, release())
		}
		m := claimed[0]
		if !now.Before(store.SendDeadline(m.CreatedAt, m.ExpiresAt)) {
			if err := w.st.ExpireClaimed(context.WithoutCancel(ctx), now, m.ID); err != nil {
				return errors.Join(err, release())
			}
			w.log.Info("message expired before send", "message_id", m.ID)
			if err := release(); err != nil {
				return err
			}
			continue
		}
		stop, err := w.send(ctx, now, ch, p, m)
		if err != nil || stop {
			return err
		}
	}
	return nil
}

// send delivers one claimed message; stop reports that this provider is done for the tick.
func (w *Worker) send(ctx context.Context, reservedAt time.Time, ch model.Channel, p provider.Provider, m store.ClaimedMessage) (stop bool, err error) {
	sctx, cancel := context.WithTimeout(ctx, constants.SendTimeout)
	started := w.clk.Now()
	res := p.Send(sctx, m.Message, m.Body)
	cancel()

	// Cleanup and an accepted send's record must survive shutdown, hence the uncancellable context.
	bg := context.WithoutCancel(ctx)
	now := w.clk.Now()
	if ctx.Err() != nil && res.Outcome != provider.Accepted {
		w.log.Info("send interrupted by shutdown", "message_id", m.ID)
		return true, errors.Join(ctx.Err(), w.st.ProviderRelease(bg, reservedAt, ch, p.Name()), w.st.Unclaim(bg, now, m.ID))
	}

	if res.Outcome == provider.Throttled {
		until := now.Add(res.PauseFor)
		if res.PauseUntilUTCMidnight {
			until = time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
		}
		err := errors.Join(
			w.st.ProviderRelease(bg, reservedAt, ch, p.Name()),
			w.st.PauseProvider(bg, ch, p.Name(), until, res.PauseReason),
			w.st.Unclaim(bg, now, m.ID),
		)
		w.log.Warn("provider throttled", "message_id", m.ID, "provider", p.Name(), "reason", res.PauseReason, "until", until)
		return true, err
	}

	var firstPoll time.Time
	if _, ok := p.(provider.StatusChecker); ok && res.Outcome == provider.Accepted {
		firstPoll = now.Add(constants.PollSchedule[0])
	}
	if err := w.st.RecordAttempt(bg, now, m.ID, res, started, Backoff(m.Attempts+1, w.rnd), firstPoll); err != nil {
		w.log.Error("recording attempt failed; lease will expire and the message be resent idempotently",
			"message_id", m.ID, "error", err)
		return true, err
	}
	w.log.Info("send attempt", "message_id", m.ID, "outcome", outcomeName(res.Outcome), "error_type", string(res.ErrorType))
	return ctx.Err() != nil, ctx.Err()
}

func outcomeName(o provider.Outcome) model.Outcome {
	switch o {
	case provider.Accepted:
		return model.OutcomeAccepted
	case provider.Retryable:
		return model.OutcomeRetryable
	}
	return model.OutcomePermanent
}
