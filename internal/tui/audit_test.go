package tui

import (
	"context"
	"errors"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"mihomoctl/internal/domain"
)

var _ tea.Model = Model{}

type observedContext struct {
	deadline    time.Time
	hasDeadline bool
	observedAt  time.Time
}

type contextAuditBackend struct {
	*fakeBackend
	contexts map[string]observedContext
}

func newContextAuditBackend() *contextAuditBackend {
	return &contextAuditBackend{
		fakeBackend: &fakeBackend{},
		contexts:    make(map[string]observedContext),
	}
}

func (b *contextAuditBackend) observe(name string, ctx context.Context) {
	deadline, ok := ctx.Deadline()
	b.contexts[name] = observedContext{deadline: deadline, hasDeadline: ok, observedAt: time.Now()}
}

func (b *contextAuditBackend) CoreStatus(ctx context.Context) (domain.RuntimeStatus, error) {
	b.observe("status", ctx)
	return b.status, nil
}

func (b *contextAuditBackend) OverviewTelemetry(ctx context.Context) (OverviewTelemetry, error) {
	b.observe("overview", ctx)
	return OverviewTelemetry{}, nil
}

func (b *contextAuditBackend) Groups(ctx context.Context) ([]domain.ProxyGroup, error) {
	b.observe("groups", ctx)
	return b.groups, nil
}

func (b *contextAuditBackend) Profiles(ctx context.Context) ([]domain.Profile, error) {
	b.observe("profiles", ctx)
	return b.profiles, nil
}

func (b *contextAuditBackend) Connections(ctx context.Context) ([]domain.Connection, error) {
	b.observe("connections", ctx)
	return b.conns, nil
}

func (b *contextAuditBackend) ScheduleStatus(ctx context.Context) (domain.ScheduleStatus, error) {
	b.observe("schedule", ctx)
	return domain.ScheduleStatus{}, nil
}

func (b *contextAuditBackend) WatchLogs(ctx context.Context, _ string) (<-chan domain.LogEntry, <-chan error) {
	b.observe("logs", ctx)
	return nil, nil
}

func (b *contextAuditBackend) WatchTraffic(ctx context.Context) (<-chan domain.Traffic, <-chan error) {
	b.observe("traffic", ctx)
	return nil, nil
}

func TestSwitchPageClearsOnlyDepartedPageError(t *testing.T) {
	tests := []struct {
		name   string
		page   page
		source errorSource
	}{
		{name: "overview", page: pageOverview, source: errorOverview},
		{name: "proxies", page: pageProxies, source: errorGroups},
		{name: "profiles", page: pageProfiles, source: errorProfiles},
		{name: "connections", page: pageConnections, source: errorConnections},
		{name: "logs", page: pageLogs, source: errorLogs},
		{name: "settings", page: pageSettings, source: errorSchedule},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := testModel()
			m.page = test.page
			m.setSourceError(test.source, errors.New("departed page"))
			m.setSourceError(errorStatus, errors.New("global status"))
			m.setSourceError(errorTraffic, errors.New("global traffic"))
			m.setSourceError(errorOperation, errors.New("foreground operation"))
			m.setSourceError(errorGroupTest, errors.New("foreground group test"))

			updated, _ := m.switchPage((test.page + 1) % page(len(pageNames)))
			m = updated.(Model)
			t.Cleanup(m.cancel)

			if _, ok := m.errors[test.source]; ok {
				t.Fatalf("departed page retained %q error: %#v", test.source, m.errors)
			}
			for _, source := range []errorSource{errorStatus, errorTraffic, errorOperation, errorGroupTest} {
				if _, ok := m.errors[source]; !ok {
					t.Errorf("switching pages cleared %q error", source)
				}
			}
			if m.err != "foreground group test" {
				t.Fatalf("visible error = %q, want newest foreground error", m.err)
			}
			m.clearForegroundErrors()
			if m.err != "global traffic" {
				t.Fatalf("visible error after clearing foreground errors = %q, want newest global error", m.err)
			}
		})
	}
}

func TestSnapshotRefreshesShareDeadline(t *testing.T) {
	tests := []struct {
		name    string
		command func(*Model) tea.Cmd
	}{
		{name: "status", command: func(m *Model) tea.Cmd { return m.beginStatusRefresh(time.Now(), true) }},
		{name: "overview", command: func(m *Model) tea.Cmd { return m.beginOverviewRefresh(time.Now(), true) }},
		{name: "groups", command: func(m *Model) tea.Cmd { return m.beginGroupsRefresh(time.Now(), true) }},
		{name: "profiles", command: func(m *Model) tea.Cmd { return m.beginProfilesRefresh(time.Now(), true) }},
		{name: "connections", command: func(m *Model) tea.Cmd { return m.beginConnectionsRefresh(time.Now(), true) }},
		{name: "schedule", command: func(m *Model) tea.Cmd { return m.beginScheduleRefresh(time.Now(), true) }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := newContextAuditBackend()
			m := New(context.Background(), backend)
			m.status.Service.Active = true
			t.Cleanup(m.cancel)

			cmd := test.command(&m)
			if cmd == nil {
				t.Fatal("refresh did not start")
			}
			_ = cmd()

			observed, ok := backend.contexts[test.name]
			if !ok {
				t.Fatal("backend did not receive refresh context")
			}
			if !observed.hasDeadline {
				t.Fatal("refresh context has no deadline")
			}
			remaining := observed.deadline.Sub(observed.observedAt)
			if remaining <= refreshRequestTimeout-time.Second || remaining > refreshRequestTimeout {
				t.Fatalf("refresh deadline = %v, want about %v", remaining, refreshRequestTimeout)
			}
		})
	}
}

func TestStreamingRequestsDoNotAddDeadline(t *testing.T) {
	backend := newContextAuditBackend()
	m := New(context.Background(), backend)
	m.status.Service.Active = true
	m.page = pageLogs
	t.Cleanup(m.cancel)

	logCmd := m.beginLogs(false)
	trafficCmd := m.beginTraffic(false)
	if logCmd == nil || trafficCmd == nil {
		t.Fatalf("stream commands did not start: logs=%v traffic=%v", logCmd != nil, trafficCmd != nil)
	}
	_ = logCmd()
	_ = trafficCmd()

	for _, name := range []string{"logs", "traffic"} {
		observed, ok := backend.contexts[name]
		if !ok {
			t.Fatalf("backend did not receive %s context", name)
		}
		if observed.hasDeadline {
			t.Errorf("%s stream received deadline %v", name, observed.deadline)
		}
	}
}

func TestApplyLogFilterPersistsCacheAcrossValueViews(t *testing.T) {
	m := testModel()
	m.page = pageLogs
	m.logs = nil
	m.appendLogBatch([]domain.LogEntry{
		{Level: "info", Message: "first match"},
		{Level: "debug", Message: "skip"},
		{Level: "error", Message: "second match"},
	})

	m.applyFilter("MATCH")
	if m.logViewVersion != m.logVersion || m.logViewFilter != "MATCH" || m.logViewLength != len(m.logs) {
		t.Fatalf("filter cache was not prepared on the model: version=%d/%d filter=%q length=%d/%d",
			m.logViewVersion, m.logVersion, m.logViewFilter, m.logViewLength, len(m.logs))
	}
	if len(m.logFilteredIndices) != 2 {
		t.Fatalf("filtered cache length = %d, want 2", len(m.logFilteredIndices))
	}
	cacheStart := &m.logFilteredIndices[0]

	_ = m.View()
	_ = m.View()
	if m.logViewVersion != m.logVersion || len(m.logFilteredIndices) != 2 {
		t.Fatalf("value View invalidated the persistent filter cache: version=%d/%d length=%d",
			m.logViewVersion, m.logVersion, len(m.logFilteredIndices))
	}
	if &m.logFilteredIndices[0] != cacheStart {
		t.Fatal("repeated value Views replaced the persistent filter cache")
	}
}
