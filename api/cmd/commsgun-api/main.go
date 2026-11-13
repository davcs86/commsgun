// Command commsgun-api queues email and SMS in SQLite and sends them through a provider.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/davcs86/commsgun/api/internal/admin"
	"github.com/davcs86/commsgun/api/internal/clock"
	"github.com/davcs86/commsgun/api/internal/config"
	"github.com/davcs86/commsgun/api/internal/constants"
	"github.com/davcs86/commsgun/api/internal/enqueue"
	"github.com/davcs86/commsgun/api/internal/health"
	"github.com/davcs86/commsgun/api/internal/model"
	"github.com/davcs86/commsgun/api/internal/provider"
	"github.com/davcs86/commsgun/api/internal/store"
	"github.com/davcs86/commsgun/api/internal/worker"
)

const (
	listenEnqueue = ":9200"
	listenAdmin   = ":9201"
	errNotFound   = `{"error":"not_found"}` + "\n"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if err := run(ctx, os.Getenv, os.Stdout, listenEnqueue, listenAdmin); err != nil {
		slog.New(slog.NewJSONHandler(os.Stdout, nil)).Error("commsgun-api failed", "error", err.Error())
		os.Exit(1)
	}
}

func run(ctx context.Context, getenv func(string) string, stdout io.Writer, enqueueAddr, adminAddr string) error {
	return runWith(ctx, getenv, stdout, enqueueAddr, adminAddr, nil)
}

// runWith lets tests wrap each provider; wrap is nil in production.
func runWith(ctx context.Context, getenv func(string) string, stdout io.Writer, enqAddr, admAddr string, wrap func(provider.Provider) provider.Provider) error {
	cfg, err := config.Load(getenv)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(stdout, nil))
	clk := clock.Real{}

	providers, checkers, err := buildProviders(cfg, wrap)
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	defer st.Close()

	w, err := worker.New(st, providers, cfg, clk, log, rand.Float64)
	if err != nil {
		return err
	}
	poller := worker.NewPoller(st, checkers, clk, log)

	hh := health.Handler(st, w.LastTick, clk, cfg.Providers)
	enqMux := http.NewServeMux()
	enqMux.Handle("/v1/messages", enqueue.NewHandler(cfg, st, clk, log))
	enqMux.Handle("/healthz", hh)
	enqMux.Handle("/healthz/backlog", hh)
	enqMux.HandleFunc("/", notFound)
	admMux := http.NewServeMux()
	admMux.Handle("/admin/v1/", admin.NewHandler(st, cfg, clk, log))
	admMux.Handle("/healthz", hh)
	admMux.HandleFunc("/", notFound)

	enqSrv, enqLn, err := listen(enqAddr, enqMux)
	if err != nil {
		return err
	}
	admSrv, admLn, err := listen(admAddr, admMux)
	if err != nil {
		enqLn.Close()
		return err
	}
	log.Info("listening", "listener", "enqueue", "addr", enqLn.Addr().String())
	log.Info("listening", "listener", "admin", "addr", admLn.Addr().String())

	lctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	loop := func(f func()) {
		wg.Add(1)
		go func() { defer wg.Done(); f() }()
	}
	loop(func() { w.Run(lctx) })
	loop(func() { poller.Run(lctx) })
	loop(func() { sweepLoop(lctx, st, clk, log) })

	serveErr := make(chan error, 2)
	for _, s := range []struct {
		srv *http.Server
		ln  net.Listener
	}{{enqSrv, enqLn}, {admSrv, admLn}} {
		loop(func() {
			if err := s.srv.Serve(s.ln); !errors.Is(err, http.ErrServerClosed) {
				serveErr <- err
			}
		})
	}

	var runErr error
	select {
	case <-lctx.Done():
	case runErr = <-serveErr:
		cancel()
	}

	sctx, scancel := context.WithTimeout(context.WithoutCancel(ctx), constants.ShutdownTimeout)
	defer scancel()
	for _, s := range []*http.Server{enqSrv, admSrv} {
		if err := s.Shutdown(sctx); err != nil {
			runErr = errors.Join(runErr, s.Close())
		}
	}
	wg.Wait()
	return runErr
}

func notFound(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_, _ = io.WriteString(w, errNotFound)
}

func listen(addr string, h http.Handler) (*http.Server, net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("listen %s: %w", addr, err)
	}
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: constants.ReadHeaderTimeout,
		ReadTimeout:       constants.ReadTimeout,
		WriteTimeout:      constants.WriteTimeout,
	}, ln, nil
}

// buildProviders makes one instance per configured provider and shares it between worker and poller.
func buildProviders(cfg *config.Config, wrap func(provider.Provider) provider.Provider) (map[model.Channel]provider.Provider, map[string]provider.StatusChecker, error) {
	hc := &http.Client{Timeout: constants.HTTPClientTimeout}
	providers := map[model.Channel]provider.Provider{}
	checkers := map[string]provider.StatusChecker{}
	for ch, pc := range cfg.Providers {
		var p provider.Provider
		switch pc.Name {
		case "resend":
			p = provider.NewResend(cfg.ResendAPIKey, hc, "")
		case "fake":
			p = provider.NewFake()
		default:
			return nil, nil, fmt.Errorf("providers.%s: unknown provider %q", ch, pc.Name)
		}
		if wrap != nil {
			p = wrap(p)
		}
		providers[ch] = p
		if sc, ok := p.(provider.StatusChecker); ok {
			checkers[pc.Name] = sc
		}
	}
	return providers, checkers, nil
}

func sweepLoop(ctx context.Context, st *store.Store, clk clock.Clock, log *slog.Logger) {
	sweep := func() {
		res, err := st.Sweep(ctx, clk.Now())
		switch {
		case ctx.Err() != nil:
		case err != nil:
			log.Error("sweep failed", "busy", errors.Is(err, store.ErrBusy))
		case res.Expired+res.Messages+res.Requests+res.Counters > 0:
			log.Info("sweep", "expired", res.Expired, "messages", res.Messages, "requests", res.Requests, "counters", res.Counters)
		}
	}
	sweep()
	t := time.NewTicker(constants.SweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sweep()
		}
	}
}
