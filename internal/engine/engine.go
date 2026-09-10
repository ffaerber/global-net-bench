// Package engine executes the configured tests and turns their results into
// measurements, routes and events.
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/ffaerber/global-net-bench/internal/bus"
	"github.com/ffaerber/global-net-bench/internal/config"
	"github.com/ffaerber/global-net-bench/internal/geoip"
	"github.com/ffaerber/global-net-bench/internal/model"
	"github.com/ffaerber/global-net-bench/internal/probe"
	"github.com/ffaerber/global-net-bench/internal/store"
)

type Mode string

const (
	ModeHealth  Mode = "health"
	ModeQuality Mode = "quality"
	ModeRoute   Mode = "route"
	ModeFull    Mode = "full"
)

// DNSRegion is the synthetic region resolver measurements are filed under. It
// is deliberately not a configured region so it never distorts regional scores.
const DNSRegion = "_dns"

type Engine struct {
	cfg     *config.Config
	store   store.Store
	bus     *bus.Bus
	log     *slog.Logger
	regions []model.Region

	// runMu serialises runs so a manual benchmark cannot overlap a scheduled
	// one and double the load on every target.
	runMu sync.Mutex

	// geo is nil when no GeoIP database is configured, which is the default.
	// The nil resolver is usable, so hop enrichment needs no special case.
	geo *geoip.Resolver

	mu       sync.RWMutex
	latest   map[SeriesKey]model.Measurement
	lastRun  map[Mode]time.Time
	caps     Capabilities
	detector *detector
	publicIP *publicIPWatcher
}

// Capabilities records which raw-socket-dependent tests this process can run.
type Capabilities struct {
	ICMP           bool   `json:"icmp"`
	Traceroute     bool   `json:"traceroute"`
	ICMPPrivileged bool   `json:"icmp_privileged"`
	Note           string `json:"note,omitempty"`
}

// SeriesKey identifies one continuously measured line of results.
type SeriesKey struct {
	Target        string
	Protocol      model.Protocol
	AddressFamily model.AddressFamily
	Port          int
	Resolver      string
}

type RunSummary struct {
	Mode         Mode      `json:"mode"`
	StartedAt    time.Time `json:"started_at"`
	FinishedAt   time.Time `json:"finished_at"`
	DurationMS   float64   `json:"duration_ms"`
	Regions      []string  `json:"regions"`
	Measurements int       `json:"measurements"`
	Routes       int       `json:"routes"`
	Events       int       `json:"events"`
}

// New builds the engine. It fails if GeoIP is enabled but its databases cannot
// be opened, rather than starting up and quietly plotting nothing.
func New(cfg *config.Config, st store.Store, b *bus.Bus, log *slog.Logger) (*Engine, error) {
	e := &Engine{
		cfg:      cfg,
		store:    st,
		bus:      b,
		log:      log,
		regions:  cfg.ModelRegions(),
		latest:   make(map[SeriesKey]model.Measurement),
		lastRun:  make(map[Mode]time.Time),
		caps:     detectCapabilities(),
		publicIP: newPublicIPWatcher(),
	}
	if cfg.GeoIP.Enabled {
		resolver, err := geoip.Open(cfg.GeoIP.CityDB, cfg.GeoIP.ASNDB)
		if err != nil {
			return nil, fmt.Errorf("geoip: %w", err)
		}
		e.geo = resolver
	}
	e.detector = newDetector(cfg, st, b, log)
	return e, nil
}

// Close releases resources held by the engine.
func (e *Engine) Close() error { return e.geo.Close() }

func detectCapabilities() Capabilities {
	caps := Capabilities{}
	if raw, err := probe.ProbeICMPSupport(false); err == nil {
		caps.ICMP = true
		caps.ICMPPrivileged = raw
		caps.Traceroute = raw
		if !raw {
			caps.Note = "unprivileged ICMP sockets only; traceroute needs CAP_NET_RAW"
		}
	} else {
		caps.Note = err.Error()
	}
	return caps
}

func (e *Engine) Regions() []model.Region    { return e.regions }
func (e *Engine) Capabilities() Capabilities { return e.caps }
func (e *Engine) Config() *config.Config     { return e.cfg }

// Latest returns the most recent measurement of every series.
func (e *Engine) Latest() []model.Measurement {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]model.Measurement, 0, len(e.latest))
	for _, m := range e.latest {
		out = append(out, m)
	}
	return out
}

func (e *Engine) LastRun(mode Mode) time.Time {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.lastRun[mode]
}

// Run executes one pass of the given mode. Passing no region IDs tests
// everything that is configured.
func (e *Engine) Run(ctx context.Context, mode Mode, regionIDs []string) (*RunSummary, error) {
	e.runMu.Lock()
	defer e.runMu.Unlock()

	regions, err := e.selectRegions(regionIDs)
	if err != nil {
		return nil, err
	}

	started := time.Now()
	summary := &RunSummary{Mode: mode, StartedAt: started}
	for _, r := range regions {
		summary.Regions = append(summary.Regions, r.ID)
	}

	type job struct {
		region model.Region
		target model.Target
	}
	var jobs []job
	for _, region := range regions {
		for _, target := range region.Targets {
			jobs = append(jobs, job{region: region, target: target})
		}
	}

	concurrency := e.cfg.Monitoring.Concurrency
	if concurrency > len(jobs) {
		concurrency = len(jobs)
	}
	if concurrency < 1 {
		concurrency = 1
	}

	var (
		mu           sync.Mutex
		measurements []model.Measurement
		routes       []model.Route
	)

	jobCh := make(chan job)
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobCh {
				ms, rs := e.probeTarget(ctx, j.region, j.target, mode)
				mu.Lock()
				measurements = append(measurements, ms...)
				routes = append(routes, rs...)
				mu.Unlock()
			}
		}()
	}
	for _, j := range jobs {
		select {
		case <-ctx.Done():
		case jobCh <- j:
		}
	}
	close(jobCh)
	wg.Wait()

	if mode == ModeHealth || mode == ModeFull {
		measurements = append(measurements, e.probeResolvers(ctx)...)
	}

	if err := e.store.InsertMeasurements(ctx, measurements); err != nil {
		e.log.Error("store measurements", "error", err)
	}
	for i := range routes {
		if err := e.store.InsertRoute(ctx, &routes[i]); err != nil {
			e.log.Error("store route", "error", err)
		}
	}

	e.mu.Lock()
	for _, m := range measurements {
		e.latest[seriesKeyOf(m)] = m
	}
	e.lastRun[mode] = started
	e.mu.Unlock()

	summary.Events = e.detector.inspect(ctx, measurements, routes)
	summary.FinishedAt = time.Now()
	summary.DurationMS = float64(summary.FinishedAt.Sub(started).Nanoseconds()) / 1e6
	summary.Measurements = len(measurements)
	summary.Routes = len(routes)

	e.bus.Publish(bus.Message{Type: bus.MessageRun, Data: summary})
	return summary, nil
}

func (e *Engine) selectRegions(ids []string) ([]model.Region, error) {
	if len(ids) == 0 {
		return e.regions, nil
	}
	wanted := make(map[string]bool, len(ids))
	for _, id := range ids {
		wanted[id] = true
	}
	var out []model.Region
	for _, r := range e.regions {
		if wanted[r.ID] {
			out = append(out, r)
			delete(wanted, r.ID)
		}
	}
	for id := range wanted {
		return nil, fmt.Errorf("unknown region %q", id)
	}
	return out, nil
}

// probeTarget runs every test the mode calls for against one target, across
// every enabled address family.
func (e *Engine) probeTarget(ctx context.Context, region model.Region, target model.Target, mode Mode) ([]model.Measurement, []model.Route) {
	var measurements []model.Measurement
	var routes []model.Route
	now := func() time.Time { return time.Now().UTC() }

	for _, af := range e.cfg.Families() {
		if af == model.IPv4 && !target.IPv4 {
			continue
		}
		if af == model.IPv6 && !target.IPv6 {
			continue
		}

		base := model.Measurement{
			Timestamp:     now(),
			Site:          e.cfg.Site.ID,
			Region:        region.ID,
			Target:        target.ID,
			AddressFamily: af,
		}

		ip, _, err := probe.Resolve(ctx, target.Hostname, af)
		if err != nil {
			// Resolution failure is itself a result: it is how an IPv6 outage or
			// a broken DNS path shows up, so record it per protocol rather than
			// silently skipping the target.
			for _, proto := range e.protocolsFor(target, mode) {
				failed := base
				failed.Protocol = proto
				failed.Success = false
				failed.Error = "resolve: " + err.Error()
				measurements = append(measurements, failed)
			}
			continue
		}
		base.ResolvedIP = ip.String()

		if e.shouldRun(mode, "icmp") && e.cfg.Tests.ICMP.Enabled && target.Supports(model.CapICMP) && e.caps.ICMP {
			measurements = append(measurements, e.runICMP(ctx, base, ip, mode))
		}
		if e.shouldRun(mode, "tcp") && e.cfg.Tests.TCP.Enabled && target.Supports(model.CapTCP) {
			measurements = append(measurements, e.runTCP(ctx, base, ip, target)...)
		}
		if e.shouldRun(mode, "https") && e.cfg.Tests.HTTPS.Enabled && target.Supports(model.CapHTTPS) && target.HTTPSURL != "" {
			measurements = append(measurements, e.runHTTPS(ctx, base, af, target))
		}
		if e.shouldRun(mode, "traceroute") && e.cfg.Tests.Traceroute.Enabled && target.Supports(model.CapTraceroute) && e.caps.Traceroute {
			if route := e.runTraceroute(ctx, region, target, af, ip); route != nil {
				routes = append(routes, *route)
			}
		}
	}
	return measurements, routes
}

// protocolsFor lists the protocols a mode would have exercised on a target,
// used to attribute a resolution failure to each of them.
func (e *Engine) protocolsFor(target model.Target, mode Mode) []model.Protocol {
	var out []model.Protocol
	if e.shouldRun(mode, "icmp") && e.cfg.Tests.ICMP.Enabled && target.Supports(model.CapICMP) && e.caps.ICMP {
		out = append(out, model.ProtoICMP)
	}
	if e.shouldRun(mode, "tcp") && e.cfg.Tests.TCP.Enabled && target.Supports(model.CapTCP) {
		out = append(out, model.ProtoTCP)
	}
	if e.shouldRun(mode, "https") && e.cfg.Tests.HTTPS.Enabled && target.Supports(model.CapHTTPS) && target.HTTPSURL != "" {
		out = append(out, model.ProtoHTTPS)
	}
	return out
}

func (e *Engine) shouldRun(mode Mode, test string) bool {
	switch mode {
	case ModeFull:
		return true
	case ModeHealth:
		return test == "icmp" || test == "tcp" || test == "https"
	case ModeQuality:
		return test == "icmp"
	case ModeRoute:
		return test == "traceroute"
	}
	return false
}

func (e *Engine) runICMP(ctx context.Context, base model.Measurement, ip netip.Addr, mode Mode) model.Measurement {
	count := e.cfg.Tests.ICMP.Packets
	if mode == ModeQuality || mode == ModeFull {
		count = e.cfg.Tests.ICMP.QualityPackets
	}

	m := base
	m.Protocol = model.ProtoICMP

	result, err := probe.Ping(ctx, ip, probe.PingOptions{
		Count:   count,
		Spacing: e.cfg.Tests.ICMP.Spacing.Duration(),
		Timeout: e.cfg.Tests.ICMP.Timeout.Duration(),
	})
	if err != nil {
		m.Success = false
		m.Error = err.Error()
		return m
	}

	m.LossRatio = model.Float(result.LossRatio)
	if result.Received == 0 {
		m.Success = false
		m.Error = "no icmp replies"
		return m
	}

	s := result.Stats
	m.Success = true
	m.LatencyMS = model.Float(s.AvgMS)
	m.MinRTTMS = model.Float(s.MinMS)
	m.AvgRTTMS = model.Float(s.AvgMS)
	m.MedianRTTMS = model.Float(s.MedMS)
	m.P95RTTMS = model.Float(s.P95MS)
	m.P99RTTMS = model.Float(s.P99MS)
	m.JitterMS = model.Float(s.JitterMS)
	return m
}

func (e *Engine) runTCP(ctx context.Context, base model.Measurement, ip netip.Addr, target model.Target) []model.Measurement {
	var out []model.Measurement
	for _, port := range target.Ports {
		result := probe.TCPConnect(ctx, ip, port, e.cfg.Tests.TCP.Timeout.Duration())

		m := base
		m.Protocol = model.ProtoTCP
		m.Port = model.Int(port)
		m.Success = result.Success
		m.ConnectMS = model.Float(float64(result.Duration.Nanoseconds()) / 1e6)
		if result.Success {
			m.LatencyMS = m.ConnectMS
		} else {
			m.Error = result.Error
		}
		out = append(out, m)
	}
	return out
}

func (e *Engine) runHTTPS(ctx context.Context, base model.Measurement, af model.AddressFamily, target model.Target) model.Measurement {
	result := probe.HTTPTiming(ctx, target.HTTPSURL, af,
		e.cfg.Tests.HTTPS.Timeout.Duration(), e.cfg.Tests.HTTPS.MaxBytes)

	m := base
	m.Protocol = model.ProtoHTTPS
	m.Success = result.Success
	m.Error = result.Error

	if result.HasDNS {
		m.DNSMS = model.Float(float64(result.DNS.Nanoseconds()) / 1e6)
	}
	if result.Connect > 0 {
		m.ConnectMS = model.Float(float64(result.Connect.Nanoseconds()) / 1e6)
	}
	if result.HasTLS {
		m.TLSMS = model.Float(float64(result.TLS.Nanoseconds()) / 1e6)
	}
	if result.TTFB > 0 {
		m.TTFBMS = model.Float(float64(result.TTFB.Nanoseconds()) / 1e6)
	}
	if result.Total > 0 {
		m.LatencyMS = model.Float(float64(result.Total.Nanoseconds()) / 1e6)
	}
	if result.StatusCode > 0 {
		m.HTTPStatus = model.Int(result.StatusCode)
	}
	if result.Bytes > 0 {
		m.ResponseBytes = model.Int64(result.Bytes)
	}
	if result.RemoteAddr != "" {
		if host, _, err := net.SplitHostPort(result.RemoteAddr); err == nil {
			m.ResolvedIP = host
		}
	}
	return m
}

func (e *Engine) runTraceroute(ctx context.Context, region model.Region, target model.Target, af model.AddressFamily, ip netip.Addr) *model.Route {
	result, err := probe.Traceroute(ctx, ip, probe.TracerouteOptions{
		MaxHops:       e.cfg.Tests.Traceroute.MaxHops,
		QueriesPerHop: e.cfg.Tests.Traceroute.QueriesPerHop,
		Timeout:       e.cfg.Tests.Traceroute.Timeout.Duration(),
	})
	if err != nil {
		if !errors.Is(err, probe.ErrTracerouteUnsupported) {
			e.log.Warn("traceroute failed", "target", target.ID, "family", af, "error", err)
		}
		return nil
	}
	if len(result.Hops) == 0 {
		return nil
	}

	route := &model.Route{
		Timestamp:     time.Now().UTC(),
		Site:          e.cfg.Site.ID,
		Region:        region.ID,
		Target:        target.ID,
		AddressFamily: af,
		Fingerprint:   result.Fingerprint(),
		HopCount:      len(result.Hops),
		Complete:      result.Reached,
	}
	for _, hop := range result.Hops {
		h := model.RouteHop{Hop: hop.TTL, RTTMS: hop.AvgRTT()}
		if hop.Responded() {
			h.IP = hop.Addr.String()
			h.Geo = e.lookupHopGeo(hop.Addr)
		}
		route.Hops = append(route.Hops, h)
	}
	return route
}

// lookupHopGeo resolves a hop's approximate position. It returns nil when GeoIP
// is disabled, when the address is not publicly locatable, or when the database
// has nothing for it -- all of which are ordinary, not errors.
func (e *Engine) lookupHopGeo(addr netip.Addr) *model.HopGeo {
	loc, ok := e.geo.Lookup(addr)
	if !ok {
		return nil
	}
	return &model.HopGeo{
		Latitude:    loc.Latitude,
		Longitude:   loc.Longitude,
		City:        loc.City,
		Country:     loc.Country,
		CountryName: loc.CountryName,
		ASN:         loc.ASN,
		Org:         loc.Org,
		Confidence:  string(loc.Confidence),
	}
}

// probeResolvers benchmarks the configured DNS resolvers. Results are filed
// under the synthetic DNS region so they inform the local score without being
// mistaken for a geographic region.
func (e *Engine) probeResolvers(ctx context.Context) []model.Measurement {
	if !e.cfg.Tests.DNS.Enabled || len(e.cfg.Tests.DNS.Resolvers) == 0 {
		return nil
	}

	var out []model.Measurement
	for _, resolver := range e.cfg.Tests.DNS.Resolvers {
		for _, af := range e.cfg.Families() {
			result := probe.DNSLookup(ctx, resolver.Address, e.cfg.Tests.DNS.QueryName, af,
				e.cfg.Tests.DNS.Timeout.Duration())

			name := resolver.Name
			if name == "" {
				name = resolver.Address
			}
			m := model.Measurement{
				Timestamp:     time.Now().UTC(),
				Site:          e.cfg.Site.ID,
				Region:        DNSRegion,
				Target:        name,
				Protocol:      model.ProtoDNS,
				AddressFamily: af,
				Resolver:      resolver.Address,
				Success:       result.Success,
				LatencyMS:     model.Float(float64(result.Duration.Nanoseconds()) / 1e6),
				Error:         result.Error,
			}
			out = append(out, m)
		}
	}
	return out
}

func seriesKeyOf(m model.Measurement) SeriesKey {
	key := SeriesKey{
		Target:        m.Target,
		Protocol:      m.Protocol,
		AddressFamily: m.AddressFamily,
		Resolver:      m.Resolver,
	}
	if m.Port != nil {
		key.Port = *m.Port
	}
	return key
}
