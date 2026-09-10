package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ffaerber/global-net-bench/internal/model"

	_ "github.com/lib/pq"
	_ "modernc.org/sqlite"
)

type dialect int

const (
	dialectSQLite dialect = iota
	dialectPostgres
)

type sqlStore struct {
	db      *sql.DB
	dialect dialect
}

// Open connects to the configured database. Timestamps are stored as Unix
// microseconds so ordering and range queries behave identically on both engines.
func Open(ctx context.Context, driver, dsn string) (Store, error) {
	switch driver {
	case "sqlite":
		db, err := sql.Open("sqlite", sqliteDSN(dsn))
		if err != nil {
			return nil, err
		}
		// modernc's driver serialises writes badly under concurrency; a single
		// connection plus WAL keeps the scheduler and API from tripping over
		// each other with "database is locked".
		db.SetMaxOpenConns(1)
		if err := db.PingContext(ctx); err != nil {
			return nil, err
		}
		return &sqlStore{db: db, dialect: dialectSQLite}, nil
	case "postgres":
		db, err := sql.Open("postgres", dsn)
		if err != nil {
			return nil, err
		}
		db.SetMaxOpenConns(10)
		if err := db.PingContext(ctx); err != nil {
			return nil, err
		}
		return &sqlStore{db: db, dialect: dialectPostgres}, nil
	default:
		return nil, fmt.Errorf("unsupported database driver %q", driver)
	}
}

func sqliteDSN(dsn string) string {
	if strings.Contains(dsn, "?") {
		return dsn
	}
	return dsn + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)"
}

// rebind turns the `?` placeholders used throughout this file into `$n` for
// PostgreSQL.
func (s *sqlStore) rebind(query string) string {
	if s.dialect != dialectPostgres {
		return query
	}
	var b strings.Builder
	n := 0
	for _, r := range query {
		if r == '?' {
			n++
			b.WriteString("$")
			b.WriteString(strconv.Itoa(n))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (s *sqlStore) Close() error { return s.db.Close() }

func (s *sqlStore) Migrate(ctx context.Context) error {
	pk := "INTEGER PRIMARY KEY AUTOINCREMENT"
	if s.dialect == dialectPostgres {
		pk = "BIGSERIAL PRIMARY KEY"
	}
	stmts := []string{
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS measurements (
			id %s,
			ts BIGINT NOT NULL,
			site TEXT NOT NULL,
			region TEXT NOT NULL,
			target TEXT NOT NULL,
			protocol TEXT NOT NULL,
			address_family TEXT NOT NULL,
			success BOOLEAN NOT NULL,
			latency_ms DOUBLE PRECISION,
			min_rtt_ms DOUBLE PRECISION,
			avg_rtt_ms DOUBLE PRECISION,
			median_rtt_ms DOUBLE PRECISION,
			p95_rtt_ms DOUBLE PRECISION,
			p99_rtt_ms DOUBLE PRECISION,
			jitter_ms DOUBLE PRECISION,
			loss_ratio DOUBLE PRECISION,
			dns_ms DOUBLE PRECISION,
			connect_ms DOUBLE PRECISION,
			tls_ms DOUBLE PRECISION,
			ttfb_ms DOUBLE PRECISION,
			http_status INTEGER,
			response_bytes BIGINT,
			resolved_ip TEXT,
			port INTEGER,
			resolver TEXT,
			error TEXT
		)`, pk),
		`CREATE INDEX IF NOT EXISTS idx_measurements_ts ON measurements (ts)`,
		`CREATE INDEX IF NOT EXISTS idx_measurements_series ON measurements (target, protocol, address_family, ts)`,
		`CREATE INDEX IF NOT EXISTS idx_measurements_region ON measurements (region, ts)`,
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS routes (
			id %s,
			ts BIGINT NOT NULL,
			site TEXT NOT NULL,
			region TEXT NOT NULL,
			target TEXT NOT NULL,
			address_family TEXT NOT NULL,
			fingerprint TEXT NOT NULL,
			hop_count INTEGER NOT NULL,
			complete BOOLEAN NOT NULL,
			hops TEXT NOT NULL
		)`, pk),
		`CREATE INDEX IF NOT EXISTS idx_routes_series ON routes (target, address_family, ts)`,
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS events (
			id %s,
			ts BIGINT NOT NULL,
			site TEXT NOT NULL,
			type TEXT NOT NULL,
			severity TEXT NOT NULL,
			region TEXT,
			target TEXT,
			message TEXT NOT NULL,
			details TEXT
		)`, pk),
		`CREATE INDEX IF NOT EXISTS idx_events_ts ON events (ts)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	return nil
}

const measurementColumns = `ts, site, region, target, protocol, address_family, success,
	latency_ms, min_rtt_ms, avg_rtt_ms, median_rtt_ms, p95_rtt_ms, p99_rtt_ms, jitter_ms, loss_ratio,
	dns_ms, connect_ms, tls_ms, ttfb_ms, http_status, response_bytes, resolved_ip, port, resolver, error`

func (s *sqlStore) InsertMeasurements(ctx context.Context, measurements []model.Measurement) error {
	if len(measurements) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	query := s.rebind(`INSERT INTO measurements (` + measurementColumns + `)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	stmt, err := tx.PrepareContext(ctx, query)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, m := range measurements {
		_, err := stmt.ExecContext(ctx,
			m.Timestamp.UTC().UnixMicro(), m.Site, m.Region, m.Target, string(m.Protocol),
			string(m.AddressFamily), m.Success,
			m.LatencyMS, m.MinRTTMS, m.AvgRTTMS, m.MedianRTTMS, m.P95RTTMS, m.P99RTTMS,
			m.JitterMS, m.LossRatio, m.DNSMS, m.ConnectMS, m.TLSMS, m.TTFBMS,
			m.HTTPStatus, m.ResponseBytes, nullString(m.ResolvedIP), m.Port,
			nullString(m.Resolver), nullString(m.Error),
		)
		if err != nil {
			return fmt.Errorf("insert measurement %s/%s: %w", m.Target, m.Protocol, err)
		}
	}
	return tx.Commit()
}

func (s *sqlStore) Measurements(ctx context.Context, f MeasurementFilter) ([]model.Measurement, error) {
	var where []string
	var args []any
	add := func(clause string, value any) {
		where = append(where, clause)
		args = append(args, value)
	}
	if f.Site != "" {
		add("site = ?", f.Site)
	}
	if f.Region != "" {
		add("region = ?", f.Region)
	}
	if f.Target != "" {
		add("target = ?", f.Target)
	}
	if f.Protocol != "" {
		add("protocol = ?", string(f.Protocol))
	}
	if f.AddressFamily != "" {
		add("address_family = ?", string(f.AddressFamily))
	}
	if !f.Since.IsZero() {
		add("ts >= ?", f.Since.UTC().UnixMicro())
	}
	if !f.Until.IsZero() {
		add("ts <= ?", f.Until.UTC().UnixMicro())
	}

	query := `SELECT id, ` + measurementColumns + ` FROM measurements`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY ts DESC"
	if f.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, f.Limit)
	}

	rows, err := s.db.QueryContext(ctx, s.rebind(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.Measurement
	for rows.Next() {
		m, err := scanMeasurement(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanMeasurement(rows scanner) (model.Measurement, error) {
	var m model.Measurement
	var ts int64
	var protocol, family string
	var resolvedIP, resolver, errText sql.NullString
	err := rows.Scan(&m.ID, &ts, &m.Site, &m.Region, &m.Target, &protocol, &family, &m.Success,
		&m.LatencyMS, &m.MinRTTMS, &m.AvgRTTMS, &m.MedianRTTMS, &m.P95RTTMS, &m.P99RTTMS,
		&m.JitterMS, &m.LossRatio, &m.DNSMS, &m.ConnectMS, &m.TLSMS, &m.TTFBMS,
		&m.HTTPStatus, &m.ResponseBytes, &resolvedIP, &m.Port, &resolver, &errText)
	if err != nil {
		return m, err
	}
	m.Timestamp = time.UnixMicro(ts).UTC()
	m.Protocol = model.Protocol(protocol)
	m.AddressFamily = model.AddressFamily(family)
	m.ResolvedIP = resolvedIP.String
	m.Resolver = resolver.String
	m.Error = errText.String
	return m, nil
}

func (s *sqlStore) InsertRoute(ctx context.Context, route *model.Route) error {
	hops, err := json.Marshal(route.Hops)
	if err != nil {
		return err
	}
	query := s.rebind(`INSERT INTO routes (ts, site, region, target, address_family, fingerprint, hop_count, complete, hops)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	_, err = s.db.ExecContext(ctx, query, route.Timestamp.UTC().UnixMicro(), route.Site,
		route.Region, route.Target, string(route.AddressFamily), route.Fingerprint,
		route.HopCount, route.Complete, string(hops))
	return err
}

func (s *sqlStore) Routes(ctx context.Context, f RouteFilter) ([]model.Route, error) {
	var where []string
	var args []any
	add := func(clause string, value any) {
		where = append(where, clause)
		args = append(args, value)
	}
	if f.Site != "" {
		add("site = ?", f.Site)
	}
	if f.Region != "" {
		add("region = ?", f.Region)
	}
	if f.Target != "" {
		add("target = ?", f.Target)
	}
	if f.AddressFamily != "" {
		add("address_family = ?", string(f.AddressFamily))
	}
	if !f.Since.IsZero() {
		add("ts >= ?", f.Since.UTC().UnixMicro())
	}

	query := `SELECT id, ts, site, region, target, address_family, fingerprint, hop_count, complete, hops FROM routes`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY ts DESC"
	if f.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, f.Limit)
	}

	rows, err := s.db.QueryContext(ctx, s.rebind(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.Route
	for rows.Next() {
		var r model.Route
		var ts int64
		var family, hops string
		if err := rows.Scan(&r.ID, &ts, &r.Site, &r.Region, &r.Target, &family,
			&r.Fingerprint, &r.HopCount, &r.Complete, &hops); err != nil {
			return nil, err
		}
		r.Timestamp = time.UnixMicro(ts).UTC()
		r.AddressFamily = model.AddressFamily(family)
		if err := json.Unmarshal([]byte(hops), &r.Hops); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *sqlStore) InsertEvent(ctx context.Context, event *model.Event) error {
	var details string
	if len(event.Details) > 0 {
		encoded, err := json.Marshal(event.Details)
		if err != nil {
			return err
		}
		details = string(encoded)
	}
	query := s.rebind(`INSERT INTO events (ts, site, type, severity, region, target, message, details)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	_, err := s.db.ExecContext(ctx, query, event.Timestamp.UTC().UnixMicro(), event.Site,
		event.Type, event.Severity, nullString(event.Region), nullString(event.Target),
		event.Message, nullString(details))
	return err
}

func (s *sqlStore) Events(ctx context.Context, f EventFilter) ([]model.Event, error) {
	var where []string
	var args []any
	add := func(clause string, value any) {
		where = append(where, clause)
		args = append(args, value)
	}
	if f.Site != "" {
		add("site = ?", f.Site)
	}
	if f.Region != "" {
		add("region = ?", f.Region)
	}
	if f.Target != "" {
		add("target = ?", f.Target)
	}
	if f.Type != "" {
		add("type = ?", f.Type)
	}
	if !f.Since.IsZero() {
		add("ts >= ?", f.Since.UTC().UnixMicro())
	}

	query := `SELECT id, ts, site, type, severity, region, target, message, details FROM events`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY ts DESC"
	if f.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, f.Limit)
	}

	rows, err := s.db.QueryContext(ctx, s.rebind(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.Event
	for rows.Next() {
		var e model.Event
		var ts int64
		var region, target, details sql.NullString
		if err := rows.Scan(&e.ID, &ts, &e.Site, &e.Type, &e.Severity, &region, &target,
			&e.Message, &details); err != nil {
			return nil, err
		}
		e.Timestamp = time.UnixMicro(ts).UTC()
		e.Region = region.String
		e.Target = target.String
		if details.Valid && details.String != "" {
			if err := json.Unmarshal([]byte(details.String), &e.Details); err != nil {
				return nil, err
			}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Baselines learns what "normal" looks like per series. Percentiles are computed
// in Go rather than SQL so SQLite and PostgreSQL agree on the result.
func (s *sqlStore) Baselines(ctx context.Context, site string, window time.Duration, minSamples int) (map[BaselineKey]model.Baseline, error) {
	query := s.rebind(`SELECT target, protocol, address_family, latency_ms, loss_ratio, jitter_ms
		FROM measurements
		WHERE site = ? AND ts >= ? AND success = ?`)
	rows, err := s.db.QueryContext(ctx, query, site, time.Now().Add(-window).UTC().UnixMicro(), true)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type accumulator struct {
		latencies []float64
		jitters   []float64
		lossSum   float64
		lossCount int
	}
	acc := map[BaselineKey]*accumulator{}
	for rows.Next() {
		var target, protocol, family string
		var latency, loss, jitter sql.NullFloat64
		if err := rows.Scan(&target, &protocol, &family, &latency, &loss, &jitter); err != nil {
			return nil, err
		}
		key := BaselineKey{Target: target, Protocol: model.Protocol(protocol), AddressFamily: model.AddressFamily(family)}
		a := acc[key]
		if a == nil {
			a = &accumulator{}
			acc[key] = a
		}
		if latency.Valid {
			a.latencies = append(a.latencies, latency.Float64)
		}
		if jitter.Valid {
			a.jitters = append(a.jitters, jitter.Float64)
		}
		if loss.Valid {
			a.lossSum += loss.Float64
			a.lossCount++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make(map[BaselineKey]model.Baseline, len(acc))
	for key, a := range acc {
		if len(a.latencies) < minSamples {
			continue
		}
		sort.Float64s(a.latencies)
		b := model.Baseline{
			Window:      window.String(),
			Samples:     len(a.latencies),
			MedianRTTMS: percentile(a.latencies, 0.50),
			P95RTTMS:    percentile(a.latencies, 0.95),
			LowRTTMS:    percentile(a.latencies, 0.10),
			HighRTTMS:   percentile(a.latencies, 0.90),
		}
		if a.lossCount > 0 {
			b.LossRatio = a.lossSum / float64(a.lossCount)
		}
		if len(a.jitters) > 0 {
			sort.Float64s(a.jitters)
			b.JitterMS = model.Float(percentile(a.jitters, 0.50))
		}
		out[key] = b
	}
	return out, nil
}

func (s *sqlStore) Prune(ctx context.Context, measurementsOlderThan, routesOlderThan time.Time) error {
	if !measurementsOlderThan.IsZero() {
		q := s.rebind(`DELETE FROM measurements WHERE ts < ?`)
		if _, err := s.db.ExecContext(ctx, q, measurementsOlderThan.UTC().UnixMicro()); err != nil {
			return err
		}
	}
	if !routesOlderThan.IsZero() {
		q := s.rebind(`DELETE FROM routes WHERE ts < ?`)
		if _, err := s.db.ExecContext(ctx, q, routesOlderThan.UTC().UnixMicro()); err != nil {
			return err
		}
	}
	return nil
}

// percentile expects sorted input and interpolates between neighbours.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	pos := p * float64(len(sorted)-1)
	lower := int(pos)
	upper := lower + 1
	if upper >= len(sorted) {
		return sorted[len(sorted)-1]
	}
	frac := pos - float64(lower)
	return sorted[lower] + frac*(sorted[upper]-sorted[lower])
}

func nullString(v string) any {
	if v == "" {
		return nil
	}
	return v
}
