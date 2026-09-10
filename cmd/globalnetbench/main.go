// Command globalnetbench measures Internet connection quality across world
// regions and serves the dashboard, REST API and Prometheus metrics.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ffaerber/global-net-bench/internal/api"
	"github.com/ffaerber/global-net-bench/internal/bus"
	"github.com/ffaerber/global-net-bench/internal/config"
	"github.com/ffaerber/global-net-bench/internal/engine"
	"github.com/ffaerber/global-net-bench/internal/scheduler"
	"github.com/ffaerber/global-net-bench/internal/store"
)

// version is the short commit SHA of the build, set at build time with
// -ldflags "-X main.version=...". Local builds report "dev".
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "globalnetbench:", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", envOr("GNB_CONFIG", "config.yaml"), "path to the configuration file")
	logLevel := flag.String("log-level", envOr("GNB_LOG_LEVEL", "info"), "log level: debug, info, warn or error")
	checkOnly := flag.Bool("check", false, "validate the configuration and exit")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("globalnetbench", version)
		return nil
	}

	log := newLogger(*logLevel)

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if *checkOnly {
		fmt.Printf("configuration OK: %d regions, %d targets\n", len(cfg.Regions), countTargets(cfg))
		return nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.Database.Type, cfg.Database.DSN)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer st.Close()

	if err := st.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}

	events := bus.New()
	testEngine := engine.New(cfg, st, events, log)

	caps := testEngine.Capabilities()
	if !caps.ICMP {
		log.Warn("ICMP tests disabled", "reason", caps.Note)
	} else if !caps.Traceroute {
		log.Warn("route tests disabled", "reason", caps.Note)
	}

	server := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           api.New(cfg, testEngine, st, events, log, version).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// The SSE stream is deliberately long-lived, so no write timeout.
		IdleTimeout: 120 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Server.Listen, "dashboard", "http://"+cfg.Server.Listen+"/")
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	schedulerDone := make(chan struct{})
	go func() {
		defer close(schedulerDone)
		scheduler.New(cfg, testEngine, st, log).Run(ctx)
	}()

	select {
	case err := <-serverErr:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
		log.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Warn("http shutdown", "error", err)
	}
	<-schedulerDone
	return nil
}

func newLogger(level string) *slog.Logger {
	var parsed slog.Level
	if err := parsed.UnmarshalText([]byte(level)); err != nil {
		parsed = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: parsed}))
}

func countTargets(cfg *config.Config) int {
	var n int
	for _, region := range cfg.Regions {
		n += len(region.Targets)
	}
	return n
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
