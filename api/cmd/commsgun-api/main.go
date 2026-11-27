// Command commsgun-api queues email and SMS in SQLite and sends them through a provider.
package main

import (
	"context"
	"crypto/tls"
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

	"github.com/emersion/go-smtp"

	"github.com/davcs86/commsgun/api/internal/admin"
	"github.com/davcs86/commsgun/api/internal/clock"
	"github.com/davcs86/commsgun/api/internal/config"
	"github.com/davcs86/commsgun/api/internal/constants"
	"github.com/davcs86/commsgun/api/internal/enqueue"
	"github.com/davcs86/commsgun/api/internal/health"
	"github.com/davcs86/commsgun/api/internal/model"
	"github.com/davcs86/commsgun/api/internal/password"
	"github.com/davcs86/commsgun/api/internal/provider"
	"github.com/davcs86/commsgun/api/internal/store"
	"github.com/davcs86/commsgun/api/internal/submission"
	"github.com/davcs86/commsgun/api/internal/worker"
)

const errNotFound = `{"error":"not_found"}` + "\n"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "admin" {
		h := password.NewHasher(password.Default(), constants.PasswordHashConcurrency)
		os.Exit(runAdmin(context.Background(), os.Args[2:], os.Getenv, os.Stdin, os.Stdout, os.Stderr, clock.Real{}, h))
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	// Restoring default handling after the first signal lets a second one kill the process.
	go func() { <-ctx.Done(); stop() }()
	if err := run(ctx, os.Getenv, os.Stdout); err != nil {
		slog.New(slog.NewJSONHandler(os.Stdout, nil)).Error("commsgun-api failed", "error", err.Error())
		os.Exit(1)
	}
}

func run(ctx context.Context, getenv func(string) string, stdout io.Writer) error {
	return runWith(ctx, getenv, stdout, nil)
}

// runWith lets tests wrap each provider; wrap is nil in production.
func runWith(ctx context.Context, getenv func(string) string, stdout io.Writer, wrap func(provider.Provider) provider.Provider) error {
	cfg, err := config.Load(getenv)
	if err != nil {
		return err
	}
	var smtpTLS *tls.Config
	if cfg.SMTPAddr != "" {
		cert, err := tls.LoadX509KeyPair(cfg.SMTPTLSCertFile, cfg.SMTPTLSKeyFile)
		if err != nil {
			return fmt.Errorf("SMTP_TLS_CERT_FILE, SMTP_TLS_KEY_FILE: %w", err)
		}
		smtpTLS = &tls.Config{Certificates: []tls.Certificate{cert}}
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
	if err := st.ClearWindowPauses(ctx); err != nil {
		return err
	}

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
	hasher := password.NewHasher(password.Default(), constants.PasswordHashConcurrency)
	admMux.Handle("/admin/v1/", admin.NewHandler(st, cfg, clk, log, hasher))
	admMux.Handle("/healthz", hh)
	admMux.HandleFunc("/", notFound)

	enqSrv, enqLn, err := listen(cfg.EnqueueAddr, enqMux)
	if err != nil {
		return err
	}
	admSrv, admLn, err := listen(cfg.AdminAddr, admMux)
	if err != nil {
		enqLn.Close()
		return err
	}
	var smtpSrv *smtp.Server
	var smtpLn net.Listener
	if smtpTLS != nil {
		if smtpLn, err = net.Listen("tcp", cfg.SMTPAddr); err != nil {
			enqLn.Close()
			admLn.Close()
			return fmt.Errorf("listen %s: %w", cfg.SMTPAddr, err)
		}
		smtpSrv = submission.NewServer(cfg, st, clk, log, smtpTLS)
	}
	log.Info("listening", "listener", "enqueue", "addr", enqLn.Addr().String())
	log.Info("listening", "listener", "admin", "addr", admLn.Addr().String())
	if smtpSrv != nil {
		log.Info("listening", "listener", "smtp", "addr", smtpLn.Addr().String())
	}

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

	serveErr := make(chan error, 3)
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
	if smtpSrv != nil {
		loop(func() {
			if err := smtpSrv.Serve(smtpLn); err != nil {
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
			log.Warn("http shutdown timed out; closing open connections", "timeout_ms", constants.ShutdownTimeout.Milliseconds())
			runErr = errors.Join(runErr, err, s.Close())
		}
	}
	if smtpSrv != nil {
		// go-smtp waits for idle connections too, and Close after Shutdown is a no-op.
		if err := smtpSrv.Shutdown(sctx); err != nil {
			log.Warn("smtp shutdown timed out", "timeout_ms", constants.ShutdownTimeout.Milliseconds())
			runErr = errors.Join(runErr, err)
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

// buildProviders makes one instance per configured channel and shares it between worker and poller.
func buildProviders(cfg *config.Config, wrap func(provider.Provider) provider.Provider) (map[model.Channel]provider.Provider, map[worker.CheckerKey]provider.StatusChecker, error) {
	hc := &http.Client{Timeout: constants.HTTPClientTimeout}
	providers := map[model.Channel]provider.Provider{}
	checkers := map[worker.CheckerKey]provider.StatusChecker{}
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
			checkers[worker.CheckerKey{Channel: ch, Provider: pc.Name}] = sc
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
