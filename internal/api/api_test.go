package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ffaerber/global-net-bench/internal/bus"
	"github.com/ffaerber/global-net-bench/internal/config"
	"github.com/ffaerber/global-net-bench/internal/engine"
	"github.com/ffaerber/global-net-bench/internal/model"
	"github.com/ffaerber/global-net-bench/internal/store"
)

func newTestServer(t *testing.T) (http.Handler, store.Store, *config.Config) {
	t.Helper()
	ctx := context.Background()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	body := `
server:
  listen: 127.0.0.1:0
site:
  id: test-site
network:
  ipv4: true
regions:
  - id: eu-central
    display_name: Frankfurt
    targets:
      - id: fra-01
        hostname: fra.example.net
      - id: fra-02
        hostname: fra2.example.net
prometheus:
  enabled: true
`
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	st, err := store.Open(ctx, "sqlite", filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	testEngine := engine.New(cfg, st, bus.New(), log)
	return New(cfg, testEngine, st, bus.New(), log, "test").Handler(), st, cfg
}

func get(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	return recorder
}

func TestStatusEndpoint(t *testing.T) {
	handler, _, _ := newTestServer(t)
	recorder := get(t, handler, "/api/v1/status")

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}

	var response StatusResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if response.Version != "test" || response.Site != "test-site" {
		t.Errorf("response = %+v", response)
	}
	if len(response.Snapshot.Regions) != 1 {
		t.Errorf("got %d regions in the snapshot", len(response.Snapshot.Regions))
	}
	if response.Intervals["health"] == "" {
		t.Error("intervals were not reported")
	}
}

func TestTargetsEndpoint(t *testing.T) {
	handler, _, _ := newTestServer(t)
	recorder := get(t, handler, "/api/v1/targets")

	var targets []model.Target
	if err := json.Unmarshal(recorder.Body.Bytes(), &targets); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("got %d targets, want 2", len(targets))
	}
	if targets[0].Region != "eu-central" {
		t.Errorf("target region = %q", targets[0].Region)
	}
}

func TestMeasurementsEndpointReturnsStoredRows(t *testing.T) {
	handler, st, cfg := newTestServer(t)

	err := st.InsertMeasurements(context.Background(), []model.Measurement{{
		Timestamp: time.Now().UTC(), Site: cfg.Site.ID, Region: "eu-central", Target: "fra-01",
		Protocol: model.ProtoICMP, AddressFamily: model.IPv4, Success: true,
		LatencyMS: model.Float(48),
	}})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	recorder := get(t, handler, "/api/v1/measurements?target=fra-01")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}

	var measurements []model.Measurement
	if err := json.Unmarshal(recorder.Body.Bytes(), &measurements); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(measurements) != 1 || *measurements[0].LatencyMS != 48 {
		t.Errorf("measurements = %+v", measurements)
	}
}

func TestEmptyResultsAreJSONArrays(t *testing.T) {
	handler, _, _ := newTestServer(t)
	for _, path := range []string{"/api/v1/measurements", "/api/v1/routes", "/api/v1/events"} {
		body := strings.TrimSpace(get(t, handler, path).Body.String())
		if body != "[]" {
			t.Errorf("%s returned %q, want []", path, body)
		}
	}
}

func TestInvalidSinceIsRejected(t *testing.T) {
	handler, _, _ := newTestServer(t)
	recorder := get(t, handler, "/api/v1/measurements?since=yesterday")
	if recorder.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", recorder.Code)
	}
}

func TestRunRejectsUnknownMode(t *testing.T) {
	handler, _, _ := newTestServer(t)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/tests/run", strings.NewReader(`{"mode":"bandwidth"}`))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", recorder.Code)
	}
}

func TestRunRejectsUnknownRegion(t *testing.T) {
	handler, _, _ := newTestServer(t)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/tests/run/atlantis", nil))

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "atlantis") {
		t.Errorf("body = %s", recorder.Body.String())
	}
}

func TestMetricsEndpoint(t *testing.T) {
	handler, _, _ := newTestServer(t)
	recorder := get(t, handler, "/metrics")

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	body := recorder.Body.String()
	for _, want := range []string{
		"# TYPE globalnetbench_global_score gauge",
		`globalnetbench_global_score{site="test-site"}`,
		"# TYPE globalnetbench_route_changes_total counter",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics output is missing %q", want)
		}
	}
}

func TestDashboardIsServed(t *testing.T) {
	handler, _, _ := newTestServer(t)

	recorder := get(t, handler, "/")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "GlobalNetBench") {
		t.Error("the dashboard shell was not served")
	}

	for _, asset := range []string{"/app.js", "/style.css"} {
		if code := get(t, handler, asset).Code; code != http.StatusOK {
			t.Errorf("%s returned %d", asset, code)
		}
	}
}

func TestParseSince(t *testing.T) {
	if _, err := parseSince(""); err != nil {
		t.Errorf("empty since returned %v", err)
	}

	relative, err := parseSince("15m")
	if err != nil {
		t.Fatalf("duration: %v", err)
	}
	if time.Since(relative) < 14*time.Minute {
		t.Errorf("15m resolved to %v", relative)
	}

	absolute, err := parseSince("2026-01-02T03:04:05Z")
	if err != nil {
		t.Fatalf("rfc3339: %v", err)
	}
	if absolute.Year() != 2026 {
		t.Errorf("parsed year = %d", absolute.Year())
	}

	if _, err := parseSince("nope"); err == nil {
		t.Error("expected an error for an unparseable value")
	}
}

func TestParseLimit(t *testing.T) {
	cases := map[string]int{
		"":      defaultLimit,
		"abc":   defaultLimit,
		"0":     defaultLimit,
		"-5":    defaultLimit,
		"50":    50,
		"99999": 10000,
	}
	for input, want := range cases {
		if got := parseLimit(input); got != want {
			t.Errorf("parseLimit(%q) = %d, want %d", input, got, want)
		}
	}
}
