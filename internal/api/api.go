// Package api exposes the REST interface, the live event stream and the
// Prometheus endpoint.
package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ffaerber/global-net-bench/internal/bus"
	"github.com/ffaerber/global-net-bench/internal/config"
	"github.com/ffaerber/global-net-bench/internal/engine"
	"github.com/ffaerber/global-net-bench/internal/metrics"
	"github.com/ffaerber/global-net-bench/internal/model"
	"github.com/ffaerber/global-net-bench/internal/score"
	"github.com/ffaerber/global-net-bench/internal/store"
	"github.com/ffaerber/global-net-bench/internal/web"
)

const defaultLimit = 500

type Server struct {
	cfg       *config.Config
	engine    *engine.Engine
	store     store.Store
	bus       *bus.Bus
	log       *slog.Logger
	version   string
	startedAt time.Time
}

func New(cfg *config.Config, e *engine.Engine, st store.Store, b *bus.Bus, log *slog.Logger, version string) *Server {
	return &Server{
		cfg:       cfg,
		engine:    e,
		store:     st,
		bus:       b,
		log:       log,
		version:   version,
		startedAt: time.Now(),
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/v1/status", s.handleStatus)
	mux.HandleFunc("GET /api/v1/regions", s.handleRegions)
	mux.HandleFunc("GET /api/v1/targets", s.handleTargets)
	mux.HandleFunc("GET /api/v1/measurements", s.handleMeasurements)
	mux.HandleFunc("GET /api/v1/routes", s.handleRoutes)
	mux.HandleFunc("GET /api/v1/events", s.handleEvents)
	mux.HandleFunc("GET /api/v1/events/stream", s.handleStream)
	mux.HandleFunc("POST /api/v1/tests/run", s.handleRun)
	mux.HandleFunc("POST /api/v1/tests/run/{region}", s.handleRun)

	if s.cfg.Prometheus.Enabled {
		mux.HandleFunc("GET /metrics", s.handleMetrics)
	}

	mux.Handle("/", web.Handler())
	return logRequests(s.log, mux)
}

type StatusResponse struct {
	Version         string                 `json:"version"`
	Site            string                 `json:"site"`
	SiteName        string                 `json:"site_name"`
	UptimeSeconds   float64                `json:"uptime_seconds"`
	Capabilities    engine.Capabilities    `json:"capabilities"`
	PublicAddresses []engine.PublicAddress `json:"public_addresses"`
	Intervals       map[string]string      `json:"intervals"`
	LastRuns        map[string]*time.Time  `json:"last_runs"`
	Snapshot        score.Snapshot         `json:"snapshot"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	lastRuns := map[string]*time.Time{}
	for _, mode := range []engine.Mode{engine.ModeHealth, engine.ModeQuality, engine.ModeRoute, engine.ModeFull} {
		if t := s.engine.LastRun(mode); !t.IsZero() {
			value := t.UTC()
			lastRuns[string(mode)] = &value
		} else {
			lastRuns[string(mode)] = nil
		}
	}

	writeJSON(w, http.StatusOK, StatusResponse{
		Version:         s.version,
		Site:            s.cfg.Site.ID,
		SiteName:        s.cfg.Site.Name,
		UptimeSeconds:   time.Since(s.startedAt).Seconds(),
		Capabilities:    s.engine.Capabilities(),
		PublicAddresses: s.engine.PublicAddresses(),
		Intervals: map[string]string{
			"health":  s.cfg.Monitoring.HealthInterval.Duration().String(),
			"quality": s.cfg.Monitoring.QualityInterval.Duration().String(),
			"route":   s.cfg.Monitoring.RouteInterval.Duration().String(),
		},
		LastRuns: lastRuns,
		Snapshot: s.engine.Snapshot(r.Context()),
	})
}

func (s *Server) handleRegions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.engine.Snapshot(r.Context()).Regions)
}

func (s *Server) handleTargets(w http.ResponseWriter, r *http.Request) {
	var targets []model.Target
	for _, region := range s.engine.Regions() {
		targets = append(targets, region.Targets...)
	}
	writeJSON(w, http.StatusOK, targets)
}

func (s *Server) handleMeasurements(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	filter := store.MeasurementFilter{
		Site:          s.cfg.Site.ID,
		Region:        query.Get("region"),
		Target:        query.Get("target"),
		Protocol:      model.Protocol(query.Get("protocol")),
		AddressFamily: model.AddressFamily(query.Get("address_family")),
		Limit:         parseLimit(query.Get("limit")),
	}

	since, err := parseSince(query.Get("since"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	filter.Since = since

	measurements, err := s.store.Measurements(r.Context(), filter)
	if err != nil {
		s.log.Error("query measurements", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to query measurements")
		return
	}
	writeJSON(w, http.StatusOK, nonNil(measurements))
}

func (s *Server) handleRoutes(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	filter := store.RouteFilter{
		Site:          s.cfg.Site.ID,
		Region:        query.Get("region"),
		Target:        query.Get("target"),
		AddressFamily: model.AddressFamily(query.Get("address_family")),
		Limit:         parseLimit(query.Get("limit")),
	}

	since, err := parseSince(query.Get("since"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	filter.Since = since

	routes, err := s.store.Routes(r.Context(), filter)
	if err != nil {
		s.log.Error("query routes", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to query routes")
		return
	}
	writeJSON(w, http.StatusOK, nonNil(routes))
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	filter := store.EventFilter{
		Site:   s.cfg.Site.ID,
		Region: query.Get("region"),
		Target: query.Get("target"),
		Type:   query.Get("type"),
		Limit:  parseLimit(query.Get("limit")),
	}

	since, err := parseSince(query.Get("since"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	filter.Since = since

	events, err := s.store.Events(r.Context(), filter)
	if err != nil {
		s.log.Error("query events", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to query events")
		return
	}
	writeJSON(w, http.StatusOK, nonNil(events))
}

type runRequest struct {
	Mode    string   `json:"mode"`
	Regions []string `json:"regions"`
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	request := runRequest{}
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
			return
		}
	}

	// A manual benchmark defaults to the full sweep, which is what the
	// dashboard's "Run global benchmark" button is for.
	mode := engine.ModeFull
	if request.Mode != "" {
		switch engine.Mode(request.Mode) {
		case engine.ModeHealth, engine.ModeQuality, engine.ModeRoute, engine.ModeFull:
			mode = engine.Mode(request.Mode)
		default:
			writeError(w, http.StatusBadRequest, "unknown mode "+request.Mode)
			return
		}
	}

	regions := request.Regions
	if region := r.PathValue("region"); region != "" {
		regions = []string{region}
	}

	summary, err := s.engine.Run(r.Context(), mode, regions)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.engine.PublishSnapshot(r.Context())
	writeJSON(w, http.StatusOK, summary)
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	err := metrics.Render(w, s.engine.Snapshot(r.Context()), s.engine.Latest(), s.engine.RouteChangeCounts())
	if err != nil {
		s.log.Error("render metrics", "error", err)
	}
}

// handleStream pushes events, run summaries and recomputed snapshots to the
// dashboard over Server-Sent Events.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	messages, unsubscribe := s.bus.Subscribe()
	defer unsubscribe()

	// Send the current picture immediately so a reconnecting client does not
	// sit blank until the next scheduled run.
	s.writeSSE(w, bus.Message{Type: bus.MessageSnapshot, Data: s.engine.Snapshot(r.Context())})
	flusher.Flush()

	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case msg, ok := <-messages:
			if !ok {
				return
			}
			if !s.writeSSE(w, msg) {
				return
			}
			flusher.Flush()
		case <-keepalive.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) writeSSE(w http.ResponseWriter, msg bus.Message) bool {
	payload, err := json.Marshal(msg.Data)
	if err != nil {
		s.log.Error("encode sse message", "error", err)
		return true
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", msg.Type, payload)
	return err == nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// parseSince accepts either a relative duration ("15m") or an RFC 3339 instant.
func parseSince(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	if d, err := time.ParseDuration(raw); err == nil {
		return time.Now().Add(-d), nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("since must be a duration like 15m or an RFC3339 timestamp")
	}
	return t, nil
}

func parseLimit(raw string) int {
	if raw == "" {
		return defaultLimit
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return defaultLimit
	}
	if n > 10000 {
		return 10000
	}
	return n
}

// nonNil keeps JSON arrays as [] rather than null, which clients find easier.
func nonNil[T any](values []T) []T {
	if values == nil {
		return []T{}
	}
	return values
}

func logRequests(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/metrics" {
			start := time.Now()
			next.ServeHTTP(w, r)
			log.Debug("request", "method", r.Method, "path", r.URL.Path, "duration_ms", time.Since(start).Milliseconds())
			return
		}
		next.ServeHTTP(w, r)
	})
}
