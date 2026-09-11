package engine

import (
	"context"
	"time"

	"github.com/ffaerber/global-net-bench/internal/bus"
	"github.com/ffaerber/global-net-bench/internal/model"
	"github.com/ffaerber/global-net-bench/internal/score"
	"github.com/ffaerber/global-net-bench/internal/store"
)

// Snapshot recomputes the current health picture from the latest measurements,
// the learned baselines and recent route churn.
func (e *Engine) Snapshot(ctx context.Context) score.Snapshot {
	routeChanges := map[string]int{}
	changesToday := 0

	hourly, err := e.store.Events(ctx, store.EventFilter{
		Site:  e.cfg.Site.ID,
		Type:  model.EventRouteChange,
		Since: time.Now().Add(-time.Hour),
		Limit: 500,
	})
	if err != nil {
		e.log.Warn("load route change events", "error", err)
	}
	for _, event := range hourly {
		routeChanges[event.Target]++
	}

	today, err := e.store.Events(ctx, store.EventFilter{
		Site:  e.cfg.Site.ID,
		Type:  model.EventRouteChange,
		Since: startOfDay(time.Now().UTC()),
		Limit: 2000,
	})
	if err == nil {
		changesToday = len(today)
	}

	latest := e.Latest()
	return score.Compute(score.Input{
		Site:              e.cfg.Site.ID,
		Regions:           e.regions,
		Latest:            latest,
		Baselines:         e.detector.Baselines(),
		RouteChanges:      routeChanges,
		RouteChangesToday: changesToday,
		MaxAge:            e.staleAfter(),
		IPv4Enabled:       e.cfg.Network.IPv4,
		IPv6Enabled:       e.cfg.Network.IPv6,
		GeoPositions:      e.regionPositions(latest),
		PublicOrigin:      e.publicOrigin(),
	})
}

// RouteChangeCounts exposes the process-lifetime route change counters.
func (e *Engine) RouteChangeCounts() map[RouteChangeKey]int {
	return e.detector.RouteChangeCounts()
}

// PublishSnapshot recomputes and pushes the snapshot to live dashboard clients.
func (e *Engine) PublishSnapshot(ctx context.Context) score.Snapshot {
	snapshot := e.Snapshot(ctx)
	e.bus.Publish(bus.Message{Type: bus.MessageSnapshot, Data: snapshot})
	return snapshot
}

// staleAfter allows a couple of missed cycles before results stop counting.
func (e *Engine) staleAfter() time.Duration {
	longest := e.cfg.Monitoring.HealthInterval.Duration()
	if q := e.cfg.Monitoring.QualityInterval.Duration(); q > longest {
		longest = q
	}
	stale := longest * 3
	if stale < 5*time.Minute {
		stale = 5 * time.Minute
	}
	return stale
}

func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}
