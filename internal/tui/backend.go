package tui

import (
	"context"
	"time"

	"mihomoctl/internal/domain"
)

type Backend interface {
	Status(context.Context) (domain.RuntimeStatus, error)
	Groups(context.Context) ([]domain.ProxyGroup, error)
	Profiles(context.Context) ([]domain.Profile, error)
	Connections(context.Context) ([]domain.Connection, error)
	WatchTraffic(context.Context) (<-chan domain.Traffic, <-chan error)
	WatchLogs(context.Context, string) (<-chan domain.LogEntry, <-chan error)

	Service(context.Context, string) error
	SetMode(context.Context, domain.Mode) error
	SetTUN(context.Context, bool) error
	SetConfig(context.Context, string, string) error
	SetSchedule(context.Context, bool) error
	SelectProxy(context.Context, string, string) error
	TestGroup(context.Context, string) (map[string]uint16, error)
	AddProfile(context.Context, string, string, time.Duration) (domain.Profile, error)
	UpdateProfile(context.Context, string) error
	UseProfile(context.Context, string) error
	RemoveProfile(context.Context, string) error
	CloseConnection(context.Context, string) error
	CloseAllConnections(context.Context) error
}

// OverviewTelemetry contains the volatile values shown on the overview page.
// Keeping it separate from RuntimeStatus lets backends avoid fetching large
// connection snapshots during every service-status refresh.
type OverviewTelemetry struct {
	Memory               int64
	ConnectionCount      int
	Traffic              domain.Traffic
	MemoryValid          bool
	ConnectionCountValid bool
	TrafficRatesValid    bool
	TrafficTotalsValid   bool
}

// CoreStatusBackend is an optional, lighter status capability. Backends that
// do not implement it continue to use Backend.Status.
type CoreStatusBackend interface {
	CoreStatus(context.Context) (domain.RuntimeStatus, error)
}

// OverviewTelemetryBackend is optional. The TUI requests it only while the
// overview page is visible.
type OverviewTelemetryBackend interface {
	OverviewTelemetry(context.Context) (OverviewTelemetry, error)
}

// GroupURLTestBackend lets the TUI pass the TestURL already present in its
// group snapshot, avoiding another full proxy-list request before a delay test.
type GroupURLTestBackend interface {
	TestGroupAtURL(context.Context, string, string) (map[string]uint16, error)
}
