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
	AddProfile(context.Context, string, string, time.Duration) error
	UpdateProfile(context.Context, string) error
	UseProfile(context.Context, string) error
	RemoveProfile(context.Context, string) error
	CloseConnection(context.Context, string) error
	CloseAllConnections(context.Context) error
}
