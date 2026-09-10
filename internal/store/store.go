// Package store persists measurements, routes and events. A single SQL
// implementation backs both SQLite and PostgreSQL.
package store

import (
	"context"
	"time"

	"github.com/ffaerber/global-net-bench/internal/model"
)

// MeasurementFilter narrows a measurement query. Zero values mean "no filter".
type MeasurementFilter struct {
	Site          string
	Region        string
	Target        string
	Protocol      model.Protocol
	AddressFamily model.AddressFamily
	Since         time.Time
	Until         time.Time
	Limit         int
}

type RouteFilter struct {
	Site          string
	Region        string
	Target        string
	AddressFamily model.AddressFamily
	Since         time.Time
	Limit         int
}

type EventFilter struct {
	Site   string
	Region string
	Target string
	Type   string
	Since  time.Time
	Limit  int
}

// BaselineKey identifies the series a rolling baseline is learned from.
type BaselineKey struct {
	Target        string
	Protocol      model.Protocol
	AddressFamily model.AddressFamily
}

type Store interface {
	Migrate(ctx context.Context) error
	InsertMeasurements(ctx context.Context, measurements []model.Measurement) error
	Measurements(ctx context.Context, f MeasurementFilter) ([]model.Measurement, error)
	InsertRoute(ctx context.Context, route *model.Route) error
	Routes(ctx context.Context, f RouteFilter) ([]model.Route, error)
	InsertEvent(ctx context.Context, event *model.Event) error
	Events(ctx context.Context, f EventFilter) ([]model.Event, error)
	// Baselines computes rolling baselines for every series with samples in the
	// window, in one pass, so scoring does not issue a query per target.
	Baselines(ctx context.Context, site string, window time.Duration, minSamples int) (map[BaselineKey]model.Baseline, error)
	Prune(ctx context.Context, measurementsOlderThan, routesOlderThan time.Time) error
	Close() error
}
