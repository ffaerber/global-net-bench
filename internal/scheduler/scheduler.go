// Package scheduler drives the periodic measurement modes.
package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/ffaerber/global-net-bench/internal/config"
	"github.com/ffaerber/global-net-bench/internal/engine"
	"github.com/ffaerber/global-net-bench/internal/store"
)

const (
	publicIPInterval = 5 * time.Minute
	pruneInterval    = 6 * time.Hour
)

type Scheduler struct {
	cfg    *config.Config
	engine *engine.Engine
	store  store.Store
	log    *slog.Logger
}

func New(cfg *config.Config, e *engine.Engine, st store.Store, log *slog.Logger) *Scheduler {
	return &Scheduler{cfg: cfg, engine: e, store: st, log: log}
}

// Run blocks until the context is cancelled, driving every periodic task.
func (s *Scheduler) Run(ctx context.Context) {
	var wg sync.WaitGroup

	// An immediate health pass means the dashboard has real numbers within
	// seconds of startup rather than after a full interval.
	s.runMode(ctx, engine.ModeHealth)
	s.engine.RefreshPublicIP(ctx)
	s.engine.PublishSnapshot(ctx)

	tasks := []struct {
		name     string
		interval time.Duration
		fn       func(context.Context)
	}{
		{"health", s.cfg.Monitoring.HealthInterval.Duration(), func(c context.Context) { s.runMode(c, engine.ModeHealth) }},
		{"quality", s.cfg.Monitoring.QualityInterval.Duration(), func(c context.Context) { s.runMode(c, engine.ModeQuality) }},
		{"route", s.cfg.Monitoring.RouteInterval.Duration(), func(c context.Context) { s.runMode(c, engine.ModeRoute) }},
		{"public-ip", publicIPInterval, s.engine.RefreshPublicIP},
		{"prune", pruneInterval, s.prune},
	}

	for _, task := range tasks {
		if task.interval <= 0 {
			continue
		}
		wg.Add(1)
		go func(name string, interval time.Duration, fn func(context.Context)) {
			defer wg.Done()
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					fn(ctx)
				}
			}
		}(task.name, task.interval, task.fn)
	}

	wg.Wait()
}

func (s *Scheduler) runMode(ctx context.Context, mode engine.Mode) {
	if mode == engine.ModeRoute && !s.engine.Capabilities().Traceroute {
		return
	}
	summary, err := s.engine.Run(ctx, mode, nil)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Error("scheduled run failed", "mode", mode, "error", err)
		}
		return
	}
	s.log.Info("run complete", "mode", mode, "measurements", summary.Measurements,
		"routes", summary.Routes, "events", summary.Events, "duration_ms", int(summary.DurationMS))
	s.engine.PublishSnapshot(ctx)
}

func (s *Scheduler) prune(ctx context.Context) {
	var measurementsBefore, routesBefore time.Time
	if d := s.cfg.Retention.RawMeasurements.Duration(); d > 0 {
		measurementsBefore = time.Now().Add(-d)
	}
	if d := s.cfg.Retention.Routes.Duration(); d > 0 {
		routesBefore = time.Now().Add(-d)
	}
	// Events are kept forever: they are the record of what actually happened.
	if err := s.store.Prune(ctx, measurementsBefore, routesBefore); err != nil {
		s.log.Warn("prune", "error", err)
	}
}
