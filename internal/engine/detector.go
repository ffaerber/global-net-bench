package engine

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ffaerber/global-net-bench/internal/bus"
	"github.com/ffaerber/global-net-bench/internal/config"
	"github.com/ffaerber/global-net-bench/internal/model"
	"github.com/ffaerber/global-net-bench/internal/store"
)

const baselineRefreshInterval = 5 * time.Minute

type routeKey struct {
	target string
	family model.AddressFamily
}

// detector turns raw results into events. It only reports transitions, so a
// target that stays down produces one event rather than one per cycle.
type detector struct {
	cfg   *config.Config
	store store.Store
	bus   *bus.Bus
	log   *slog.Logger

	mu           sync.Mutex
	routeSeen    map[routeKey]string
	active       map[string]bool
	baselines    map[store.BaselineKey]model.Baseline
	baselinesAt  time.Time
	routesLoaded map[routeKey]bool
	routeChanges map[RouteChangeKey]int
}

// RouteChangeKey labels the route-change counter exported to Prometheus.
type RouteChangeKey struct {
	Region string
	Target string
}

func newDetector(cfg *config.Config, st store.Store, b *bus.Bus, log *slog.Logger) *detector {
	return &detector{
		cfg:          cfg,
		store:        st,
		bus:          b,
		log:          log,
		routeSeen:    make(map[routeKey]string),
		active:       make(map[string]bool),
		routesLoaded: make(map[routeKey]bool),
		routeChanges: make(map[RouteChangeKey]int),
	}
}

// RouteChangeCounts returns the per-target route changes seen since startup.
// Prometheus counters must only ever increase, so this is process-scoped rather
// than a windowed count.
func (d *detector) RouteChangeCounts() map[RouteChangeKey]int {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(map[RouteChangeKey]int, len(d.routeChanges))
	for k, v := range d.routeChanges {
		out[k] = v
	}
	return out
}

// Baselines exposes the learned baselines for scoring.
func (d *detector) Baselines() map[store.BaselineKey]model.Baseline {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(map[store.BaselineKey]model.Baseline, len(d.baselines))
	for k, v := range d.baselines {
		out[k] = v
	}
	return out
}

func (d *detector) inspect(ctx context.Context, measurements []model.Measurement, routes []model.Route) int {
	d.refreshBaselines(ctx)

	var emitted int
	emitted += d.checkRoutes(ctx, routes)
	emitted += d.checkReachability(ctx, measurements)
	emitted += d.checkQuality(ctx, measurements)
	return emitted
}

func (d *detector) refreshBaselines(ctx context.Context) {
	d.mu.Lock()
	stale := time.Since(d.baselinesAt) > baselineRefreshInterval
	d.mu.Unlock()
	if !stale {
		return
	}

	baselines, err := d.store.Baselines(ctx, d.cfg.Site.ID,
		d.cfg.Scoring.BaselineWindow.Duration(), d.cfg.Scoring.MinBaselineSamples)
	if err != nil {
		d.log.Warn("refresh baselines", "error", err)
		return
	}

	d.mu.Lock()
	d.baselines = baselines
	d.baselinesAt = time.Now()
	d.mu.Unlock()
}

func (d *detector) checkRoutes(ctx context.Context, routes []model.Route) int {
	var emitted int
	for _, route := range routes {
		if route.Fingerprint == "" {
			continue
		}
		key := routeKey{target: route.Target, family: route.AddressFamily}

		d.mu.Lock()
		loaded := d.routesLoaded[key]
		previous, known := d.routeSeen[key]
		d.mu.Unlock()

		// On first sight this process has no history, so consult the database
		// before deciding whether the path actually changed.
		if !known && !loaded {
			stored, err := d.store.Routes(ctx, store.RouteFilter{
				Target: route.Target, AddressFamily: route.AddressFamily, Limit: 2,
			})
			if err != nil {
				d.log.Warn("load previous route", "target", route.Target, "error", err)
			}
			for _, s := range stored {
				if s.ID != route.ID && s.Fingerprint != "" {
					previous, known = s.Fingerprint, true
					break
				}
			}
			d.mu.Lock()
			d.routesLoaded[key] = true
			d.mu.Unlock()
		}

		if known && previous != route.Fingerprint {
			d.emit(ctx, model.Event{
				Type:     model.EventRouteChange,
				Severity: model.SeverityWarning,
				Region:   route.Region,
				Target:   route.Target,
				Message: fmt.Sprintf("Route to %s (%s) changed: %d hops via %s",
					route.Target, route.AddressFamily, route.HopCount, summarizeHops(route.Hops)),
				Details: map[string]string{
					"previous_fingerprint": previous,
					"fingerprint":          route.Fingerprint,
					"hop_count":            strconv.Itoa(route.HopCount),
					"address_family":       string(route.AddressFamily),
				},
			})
			emitted++
			d.mu.Lock()
			d.routeChanges[RouteChangeKey{Region: route.Region, Target: route.Target}]++
			d.mu.Unlock()
		}

		d.mu.Lock()
		d.routeSeen[key] = route.Fingerprint
		d.mu.Unlock()
	}
	return emitted
}

// checkReachability reports a target as lost only when every protocol failed:
// filtered ICMP alone must never be read as an outage.
func (d *detector) checkReachability(ctx context.Context, measurements []model.Measurement) int {
	type reach struct {
		region  string
		total   int
		success int
	}
	byTarget := map[routeKey]*reach{}
	for _, m := range measurements {
		if m.Region == DNSRegion {
			continue
		}
		key := routeKey{target: m.Target, family: m.AddressFamily}
		r := byTarget[key]
		if r == nil {
			r = &reach{region: m.Region}
			byTarget[key] = r
		}
		r.total++
		if m.Success {
			r.success++
		}
	}

	var emitted int
	for key, r := range byTarget {
		if r.total == 0 {
			continue
		}
		down := r.success == 0
		conditionID := "down:" + key.target + ":" + string(key.family)
		if d.transition(conditionID, down) {
			if down {
				d.emit(ctx, model.Event{
					Type:     model.EventConnectivityLost,
					Severity: model.SeverityCritical,
					Region:   r.region,
					Target:   key.target,
					Message:  fmt.Sprintf("%s unreachable over %s on every protocol", key.target, key.family),
					Details:  map[string]string{"address_family": string(key.family)},
				})
			} else {
				d.emit(ctx, model.Event{
					Type:     model.EventTargetRecovered,
					Severity: model.SeverityInfo,
					Region:   r.region,
					Target:   key.target,
					Message:  fmt.Sprintf("%s reachable again over %s", key.target, key.family),
					Details:  map[string]string{"address_family": string(key.family)},
				})
			}
			emitted++
		}
	}
	return emitted
}

func (d *detector) checkQuality(ctx context.Context, measurements []model.Measurement) int {
	t := d.cfg.Thresholds
	var emitted int

	for _, m := range measurements {
		if m.Protocol == model.ProtoDNS {
			if d.transition("dns:"+m.Target+":"+string(m.AddressFamily), !m.Success) && !m.Success {
				d.emit(ctx, model.Event{
					Type:     model.EventDNSFailure,
					Severity: model.SeverityWarning,
					Region:   m.Region,
					Target:   m.Target,
					Message:  fmt.Sprintf("DNS resolver %s failed over %s: %s", m.Target, m.AddressFamily, m.Error),
				})
				emitted++
			}
			continue
		}

		if m.LossRatio != nil {
			loss := *m.LossRatio
			condition := loss >= t.PacketLossWarn
			if d.transition("loss:"+m.Target+":"+string(m.AddressFamily), condition) && condition {
				severity := model.SeverityWarning
				if loss >= t.PacketLossCritical {
					severity = model.SeverityCritical
				}
				d.emit(ctx, model.Event{
					Type:     model.EventPacketLossSpike,
					Severity: severity,
					Region:   m.Region,
					Target:   m.Target,
					Message:  fmt.Sprintf("Packet loss to %s (%s) at %.1f%%", m.Target, m.AddressFamily, loss*100),
					Details:  map[string]string{"loss_ratio": formatFloat(loss)},
				})
				emitted++
			}
		}

		baseline, hasBaseline := d.baselineFor(m)

		if m.Success && m.LatencyMS != nil && hasBaseline && baseline.MedianRTTMS > 0 {
			latency := *m.LatencyMS
			limit := baseline.MedianRTTMS * t.LatencySpikeFactor
			// The absolute floor stops a 2 ms local target from alerting because
			// it briefly took 3 ms.
			condition := latency > limit && latency-baseline.MedianRTTMS >= t.MinLatencyDeltaMS
			if d.transition("latency:"+m.Target+":"+string(m.Protocol)+":"+string(m.AddressFamily), condition) && condition {
				increase := (latency/baseline.MedianRTTMS - 1) * 100
				d.emit(ctx, model.Event{
					Type:     model.EventLatencySpike,
					Severity: model.SeverityWarning,
					Region:   m.Region,
					Target:   m.Target,
					Message: fmt.Sprintf("%s %s latency increased %.0f%% (%.1f ms vs %.1f ms normal)",
						m.Target, m.Protocol, increase, latency, baseline.MedianRTTMS),
					Details: map[string]string{
						"latency_ms":  formatFloat(latency),
						"baseline_ms": formatFloat(baseline.MedianRTTMS),
						"protocol":    string(m.Protocol),
					},
				})
				emitted++
			}
		}

		if m.JitterMS != nil {
			jitter := *m.JitterMS
			condition := jitter >= t.JitterWarnMS
			if d.transition("jitter:"+m.Target+":"+string(m.AddressFamily), condition) && condition {
				d.emit(ctx, model.Event{
					Type:     model.EventJitterSpike,
					Severity: model.SeverityWarning,
					Region:   m.Region,
					Target:   m.Target,
					Message:  fmt.Sprintf("Jitter to %s (%s) at %.1f ms", m.Target, m.AddressFamily, jitter),
					Details:  map[string]string{"jitter_ms": formatFloat(jitter)},
				})
				emitted++
			}
		}
	}

	emitted += d.checkAddressFamily(ctx, measurements)
	return emitted
}

// checkAddressFamily flags the case where one family works and the other does
// not, which is the signature of a broken IPv6 deployment rather than an outage.
func (d *detector) checkAddressFamily(ctx context.Context, measurements []model.Measurement) int {
	success := map[model.AddressFamily]int{}
	total := map[model.AddressFamily]int{}
	for _, m := range measurements {
		if m.Region == DNSRegion {
			continue
		}
		total[m.AddressFamily]++
		if m.Success {
			success[m.AddressFamily]++
		}
	}

	v6Broken := total[model.IPv6] > 0 && success[model.IPv6] == 0 &&
		total[model.IPv4] > 0 && success[model.IPv4] > 0
	if d.transition("ipv6-broken", v6Broken) && v6Broken {
		d.emit(ctx, model.Event{
			Type:     model.EventIPv6Failure,
			Severity: model.SeverityWarning,
			Message:  "IPv6 failed to every target while IPv4 is working",
		})
		return 1
	}
	return 0
}

// transition reports whether a condition just changed state, and records the
// new state.
func (d *detector) transition(id string, condition bool) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	previous, seen := d.active[id]
	d.active[id] = condition
	if !seen {
		// The first observation of a healthy condition is not news.
		return condition
	}
	return previous != condition
}

func (d *detector) emit(ctx context.Context, event model.Event) {
	event.Timestamp = time.Now().UTC()
	event.Site = d.cfg.Site.ID
	if err := d.store.InsertEvent(ctx, &event); err != nil {
		d.log.Error("store event", "error", err)
	}
	d.log.Info("event", "type", event.Type, "severity", event.Severity, "message", event.Message)
	d.bus.Publish(bus.Message{Type: bus.MessageEvent, Data: event})
}

func (d *detector) baselineFor(m model.Measurement) (model.Baseline, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	b, ok := d.baselines[store.BaselineKey{
		Target:        m.Target,
		Protocol:      m.Protocol,
		AddressFamily: m.AddressFamily,
	}]
	return b, ok
}

func summarizeHops(hops []model.RouteHop) string {
	var parts []string
	for _, hop := range hops {
		if hop.IP != "" {
			parts = append(parts, hop.IP)
		}
	}
	if len(parts) > 4 {
		parts = append(parts[:2], append([]string{"…"}, parts[len(parts)-2:]...)...)
	}
	return strings.Join(parts, " → ")
}

func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', 3, 64)
}
