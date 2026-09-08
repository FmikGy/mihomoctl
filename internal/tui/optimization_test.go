package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"mihomoctl/internal/domain"
)

type optimizedBackend struct {
	*fakeBackend
	coreStatusCalls int
	statusCalls     int
	telemetryCalls  int
	testURL         string
	testedGroup     string
	telemetry       OverviewTelemetry
	telemetryErr    error
}

type deadlineBackend struct {
	*fakeBackend
	deadline    time.Time
	hasDeadline bool
}

func (b *deadlineBackend) CoreStatus(ctx context.Context) (domain.RuntimeStatus, error) {
	b.deadline, b.hasDeadline = ctx.Deadline()
	return b.status, nil
}

func (b *optimizedBackend) Status(context.Context) (domain.RuntimeStatus, error) {
	b.statusCalls++
	return b.status, nil
}

func (b *optimizedBackend) CoreStatus(context.Context) (domain.RuntimeStatus, error) {
	b.coreStatusCalls++
	return b.status, nil
}

func (b *optimizedBackend) OverviewTelemetry(context.Context) (OverviewTelemetry, error) {
	b.telemetryCalls++
	return b.telemetry, b.telemetryErr
}

func (b *optimizedBackend) TestGroupAtURL(_ context.Context, group, testURL string) (map[string]uint16, error) {
	b.testedGroup, b.testURL = group, testURL
	return b.delays, b.groupErr
}

func TestCtrlCQuitsAndCancelsEveryUIState(t *testing.T) {
	tests := []struct {
		name  string
		apply func(*Model)
	}{
		{"base", func(*Model) {}},
		{"input", func(m *Model) { m.inMode = inputFilter }},
		{"picker", func(m *Model) { m.picker = pickerLogLevel }},
		{"confirm", func(m *Model) { m.confirm = confirmStopService }},
		{"help", func(m *Model) { m.help = true }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := testModel()
			test.apply(&m)
			updated, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
			m = updated.(Model)
			if cmd == nil {
				t.Fatal("Ctrl+C did not request exit")
			}
			if _, ok := cmd().(tea.QuitMsg); !ok {
				t.Fatalf("Ctrl+C command returned %T, want tea.QuitMsg", cmd())
			}
			select {
			case <-m.ctx.Done():
			default:
				t.Fatal("Ctrl+C did not cancel model context")
			}
		})
	}
}

func TestCtrlCWaitsForMutationCompletionBeforeQuitting(t *testing.T) {
	for _, key := range []tea.KeyPressMsg{{Code: 'c', Mod: tea.ModCtrl}, {Code: 'q', Text: "q"}} {
		m := testModel()
		m.loading = true
		m.privilegedOperation = true
		m.mutationGeneration = 4
		updated, cmd := m.Update(key)
		m = updated.(Model)
		if cmd != nil || !m.quitPending || !m.loading {
			t.Fatalf("quit during mutation exited early: cmd=%v pending=%v loading=%v", cmd != nil, m.quitPending, m.loading)
		}
		select {
		case <-m.ctx.Done():
		default:
			t.Fatal("quit during mutation did not cancel the operation context")
		}

		m, cmd = updateUIModel(t, m, operationMsg{err: context.Canceled, generation: 4, privileged: true})
		if cmd == nil || m.loading || !m.quitPending {
			t.Fatalf("completed rollback did not request exit: cmd=%v loading=%v pending=%v", cmd != nil, m.loading, m.quitPending)
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Fatalf("completion command returned %T, want tea.QuitMsg", cmd())
		}
	}
}

func TestRunCancelsChildAndSilencesExpectedInterrupts(t *testing.T) {
	original := runTeaProgram
	t.Cleanup(func() { runTeaProgram = original })

	for _, interrupted := range []error{tea.ErrInterrupted, fmt.Errorf("wrapped: %w", context.Canceled)} {
		var child context.Context
		runTeaProgram = func(_ context.Context, model Model) error {
			child = model.ctx
			return interrupted
		}
		if err := Run(context.Background(), &fakeBackend{}); err != nil {
			t.Fatalf("Run(%v) = %v, want nil", interrupted, err)
		}
		select {
		case <-child.Done():
		default:
			t.Fatal("Run did not cancel its child context")
		}
	}

	want := errors.New("terminal failed")
	runTeaProgram = func(context.Context, Model) error { return want }
	if err := Run(context.Background(), &fakeBackend{}); !errors.Is(err, want) {
		t.Fatalf("Run error = %v, want %v", err, want)
	}
}

func TestRunWaitsForStartedOperationAfterExternalCancellation(t *testing.T) {
	original := runTeaProgram
	t.Cleanup(func() { runTeaProgram = original })

	started := make(chan struct{})
	recovering := make(chan struct{})
	finishRecovery := make(chan struct{})
	runTeaProgram = func(ctx context.Context, model Model) error {
		cmd := model.beginOperation("完成", func(operationCtx context.Context) error {
			close(started)
			<-operationCtx.Done()
			close(recovering)
			<-finishRecovery
			return operationCtx.Err()
		})
		go func() { _ = cmd() }()
		<-ctx.Done()
		return ctx.Err()
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, &fakeBackend{}) }()
	<-started
	cancel()
	<-recovering
	select {
	case err := <-done:
		t.Fatalf("Run returned before operation recovery completed: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(finishRecovery)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return after operation recovery completed")
	}
}

func TestInputViewUsesWidgetViewportWithoutLeakingProfile(t *testing.T) {
	m := testModel()
	m.width = 60
	m.focusInput(inputFilter, "筛选", "")
	m.input.SetValue("BEGIN-HIDDEN-" + strings.Repeat("prefix", 20) + "-visible-tail")
	m.input.CursorEnd()
	view := ansi.Strip(m.renderInput())
	if !strings.Contains(view, "visible-tail") || strings.Contains(view, "BEGIN-HIDDEN") {
		t.Fatalf("input did not horizontally follow the cursor: %q", view)
	}

	m.focusInput(inputProfile, "订阅", "")
	secret := strings.Repeat("https://secret.example/", 8) + "?token=private"
	m.input.SetValue(secret)
	m.input.CursorEnd()
	view = ansi.Strip(m.renderInput())
	if strings.Contains(view, "secret") || strings.Contains(view, "private") || !strings.Contains(view, string(profileInputMask)) {
		t.Fatalf("profile input leaked or omitted its mask: %q", view)
	}
}

func TestForegroundErrorsStayVisibleUntilEscape(t *testing.T) {
	m := testModel()
	m.setSourceError(errorOperation, errors.New("前台操作失败"))
	m.setSourceError(errorStatus, errors.New("后台状态失败"))
	m.setSourceError(errorConnections, errors.New("后台连接失败"))
	if m.err != "前台操作失败" {
		t.Fatalf("background error replaced foreground error: %q", m.err)
	}

	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = updated.(Model)
	if m.err != "后台连接失败" {
		t.Fatalf("Esc did not reveal newest background error: %q", m.err)
	}
	if _, exists := m.errors[errorOperation]; exists {
		t.Fatal("Esc retained operation error")
	}
}

func TestTrafficWaitsForConfirmedActiveService(t *testing.T) {
	m := testModel()
	m.status = domain.RuntimeStatus{}
	updated, _ := m.Update(bootstrapMsg{})
	m = updated.(Model)
	if m.trafficStreamPresent() {
		t.Fatal("bootstrap started traffic before service status was known")
	}
	if cmd := m.beginTraffic(false); cmd != nil {
		t.Fatal("inactive service started traffic")
	}

	m.status.Service = domain.ServiceStatus{Active: true, State: "active"}
	if cmd := m.beginTraffic(false); cmd == nil || !m.trafficConnecting {
		t.Fatal("active service did not start traffic")
	}
	m.stopTraffic()
	m.status.Service.Active = false
	m.trafficGeneration = 11
	m.trafficReconnectPending = true
	updated, cmd := m.Update(trafficReconnectMsg{generation: 11})
	m = updated.(Model)
	if cmd != nil || m.trafficReconnectPending {
		t.Fatalf("inactive reconnect state: cmd=%v pending=%v", cmd != nil, m.trafficReconnectPending)
	}
}

func TestStatusErrorMergesValidPartialRuntime(t *testing.T) {
	m := testModel()
	m.status.Service = domain.ServiceStatus{State: "inactive"}
	m.status.ActiveProfile = "旧配置"
	m.statusRequest = requestState{inFlight: true, generation: 4}
	reported := domain.RuntimeStatus{
		Service:         domain.ServiceStatus{Active: true, Enabled: true, State: "active", PID: 42},
		ActiveProfile:   "新配置",
		CoreVersion:     "控制器不可用",
		ConfigAvailable: true,
		Mode:            domain.ModeGlobal,
		MixedPort:       8899,
		LogLevel:        "error",
	}
	updated, cmd := m.Update(statusMsg{status: reported, err: errors.New("API 失败"), generation: 4, requestedAt: time.Now()})
	m = updated.(Model)
	if !m.status.Service.Active || m.status.Service.PID != 42 || m.status.ActiveProfile != "新配置" ||
		m.status.CoreVersion != "控制器不可用" || m.status.Mode != domain.ModeGlobal || m.status.MixedPort != 8899 {
		t.Fatalf("partial runtime was not merged: %#v", m.status)
	}
	if cmd == nil || !m.trafficConnecting {
		t.Fatalf("partial active status did not start runtime streams: cmd=%v connecting=%v", cmd != nil, m.trafficConnecting)
	}
	if header := ansi.Strip(m.renderHeader()); strings.Contains(header, "不可用") || !strings.Contains(header, "运行中") {
		t.Fatalf("partial active status rendered the wrong service state: %q", header)
	}
}

func TestStatusRefreshHasDeadline(t *testing.T) {
	backend := &deadlineBackend{fakeBackend: &fakeBackend{}}
	m := New(context.Background(), backend)
	started := time.Now()
	message := m.beginStatusRefresh(started, true)()
	if _, ok := message.(statusMsg); !ok {
		t.Fatalf("status command returned %T", message)
	}
	if !backend.hasDeadline {
		t.Fatal("status refresh context has no deadline")
	}
	remaining := backend.deadline.Sub(started)
	if remaining > refreshRequestTimeout+100*time.Millisecond || remaining < refreshRequestTimeout-time.Second {
		t.Fatalf("status deadline = %v, want about %v", remaining, refreshRequestTimeout)
	}
}

func TestInactiveServiceStopsRuntimePollingAndClearsSnapshots(t *testing.T) {
	m := testModel()
	m.groupsVersion = 1
	m.connectionsVersion = 1
	m.groupRequest = requestState{inFlight: true, generation: 2, cancel: func() {}}
	m.connectionRequest = requestState{inFlight: true, generation: 3, cancel: func() {}}
	m.statusRequest = requestState{inFlight: true, generation: 4}
	updated, _ := m.Update(statusMsg{
		status:     domain.RuntimeStatus{Service: domain.ServiceStatus{State: "inactive"}},
		generation: 4,
	})
	m = updated.(Model)
	if len(m.groups) != 0 || len(m.connections) != 0 {
		t.Fatalf("inactive service retained runtime snapshots: groups=%d connections=%d", len(m.groups), len(m.connections))
	}
	if m.groupRequest.inFlight || m.connectionRequest.inFlight {
		t.Fatalf("inactive service retained requests: groups=%#v connections=%#v", m.groupRequest, m.connectionRequest)
	}
	if cmd := m.beginGroupsRefresh(time.Now(), true); cmd != nil {
		t.Fatal("inactive service started a group refresh")
	}
	if cmd := m.beginConnectionsRefresh(time.Now(), true); cmd != nil {
		t.Fatal("inactive service started a connection refresh")
	}
}

func TestLogStreamBatchesAndRingKeepsLogicalOrder(t *testing.T) {
	entries := make(chan domain.LogEntry, 200)
	for index := range 200 {
		entries <- domain.LogEntry{Message: fmt.Sprintf("batch-%03d", index)}
	}
	close(entries)
	first := waitLogBatch(entries, 7)().(logBatchMsg)
	second := waitLogBatch(entries, 7)().(logBatchMsg)
	if len(first.entries) != logBatchMaximum || first.closed || len(second.entries) != 200-logBatchMaximum || !second.closed {
		t.Fatalf("unexpected batches: first=%d/%v second=%d/%v", len(first.entries), first.closed, len(second.entries), second.closed)
	}

	m := testModel()
	m.logs = nil
	batch := make([]domain.LogEntry, logBufferMaximum+5)
	for index := range batch {
		batch[index].Message = fmt.Sprintf("ring-%04d", index)
	}
	m.appendLogBatch(batch)
	if m.logViewLen() != logBufferMaximum {
		t.Fatalf("ring length = %d", m.logViewLen())
	}
	if first, last := m.logViewEntry(0), m.logViewEntry(logBufferMaximum-1); first.Message != "ring-0005" || last.Message != "ring-1004" {
		t.Fatalf("ring order: first=%q last=%q", first.Message, last.Message)
	}
}

func TestFilteredProxyViewRetainsOriginalMemberIndex(t *testing.T) {
	m := testModel()
	m.page = pageProxies
	m.groups = []domain.ProxyGroup{{
		Name: "PROXY",
		All:  []string{"member-a", "member-b", "member-target"},
		Proxies: []domain.Proxy{
			{Name: "alpha", Type: "VLESS", Alive: true},
			{Name: "beta", Type: "VLESS", Alive: true},
			{Name: "target", Type: "VLESS", Alive: true},
		},
	}}
	m.groupsVersion++
	m.invalidateGroupViews()
	m.proxyTestStates["PROXY"] = map[string]proxyTestResult{
		"member-a":      {state: proxyTestSuccess, delay: 11},
		"member-target": {state: proxyTestSuccess, delay: 33},
	}
	m.applyFilter("target")
	views := m.currentGroupViews()
	if len(views) != 1 || len(views[0].proxies) != 1 || views[0].proxies[0].proxyIndex != 2 {
		t.Fatalf("filtered refs = %#v", views)
	}
	view := ansi.Strip(m.renderProxies(16))
	if !strings.Contains(view, "33 ms") || strings.Contains(view, "11 ms") {
		t.Fatalf("filtered proxy used wrong source index:\n%s", view)
	}
}

func TestOptionalBackendCapabilitiesAndFallback(t *testing.T) {
	base := &fakeBackend{
		status: domain.RuntimeStatus{Service: domain.ServiceStatus{Active: true, State: "active"}},
		groups: []domain.ProxyGroup{{Name: "AUTO", TestURL: "https://probe.example/204"}},
		delays: map[string]uint16{"node": 21},
	}
	backend := &optimizedBackend{
		fakeBackend: base,
		telemetry: OverviewTelemetry{
			Memory: 1234, ConnectionCount: 9,
			Traffic:     domain.Traffic{UpTotal: 10, DownTotal: 20},
			MemoryValid: true, ConnectionCountValid: true, TrafficTotalsValid: true,
		},
	}
	m := New(context.Background(), backend)
	m.status = base.status
	m.groups = base.groups

	statusCommand := m.beginStatusRefresh(time.Now(), true)
	status := statusCommand().(statusMsg)
	if !status.coreOnly || backend.coreStatusCalls != 1 || backend.statusCalls != 0 {
		t.Fatalf("status capability calls: core=%d full=%d coreOnly=%v", backend.coreStatusCalls, backend.statusCalls, status.coreOnly)
	}

	telemetryCommand := m.beginOverviewRefresh(time.Now(), true)
	telemetry := telemetryCommand().(overviewTelemetryMsg)
	if backend.telemetryCalls != 1 {
		t.Fatalf("telemetry calls = %d", backend.telemetryCalls)
	}
	m.overviewRequest = requestState{inFlight: true, generation: telemetry.generation}
	updated, _ := m.Update(telemetry)
	m = updated.(Model)
	if m.status.Memory != 1234 || m.status.ConnectionCount != 9 || m.status.Traffic.DownTotal != 20 {
		t.Fatalf("telemetry was not applied: %#v", m.status)
	}

	command := m.beginGroupTest("AUTO")
	result := command().(groupTestMsg)
	if result.err != nil || backend.testedGroup != "AUTO" || backend.testURL != "https://probe.example/204" {
		t.Fatalf("URL test call: group=%q URL=%q err=%v", backend.testedGroup, backend.testURL, result.err)
	}

	fallback := New(context.Background(), base)
	message := fallback.beginStatusRefresh(time.Now(), true)().(statusMsg)
	if message.coreOnly {
		t.Fatal("legacy backend did not fall back to Status")
	}
}

func TestConnectionsRefreshUpdatesOverviewCount(t *testing.T) {
	m := testModel()
	m.status.ConnectionCount = 99
	m.connectionRequest = requestState{inFlight: true, generation: 2}
	updated, _ := m.Update(connectionsMsg{
		connections: []domain.Connection{{ID: "a"}, {ID: "b"}},
		generation:  2,
	})
	m = updated.(Model)
	if m.status.ConnectionCount != 2 {
		t.Fatalf("connection count = %d, want 2", m.status.ConnectionCount)
	}
}

func TestOverviewTelemetryMergesPartialValuesOnError(t *testing.T) {
	m := testModel()
	m.status.Service = domain.ServiceStatus{Active: true, State: "active"}
	m.overviewRequest = requestState{inFlight: true, generation: 3}
	updated, _ := m.Update(overviewTelemetryMsg{
		telemetry: OverviewTelemetry{
			Memory: 4096, ConnectionCount: 7,
			Traffic:     domain.Traffic{UpTotal: 11, DownTotal: 22},
			MemoryValid: true, ConnectionCountValid: true, TrafficTotalsValid: true,
		},
		err:        errors.New("部分遥测失败"),
		generation: 3,
	})
	m = updated.(Model)
	if m.status.Memory != 4096 || m.status.ConnectionCount != 7 || m.status.Traffic.DownTotal != 22 {
		t.Fatalf("partial telemetry was discarded: %#v", m.status)
	}
	if m.err != "部分遥测失败" {
		t.Fatalf("partial telemetry error = %q", m.err)
	}
}

func TestOverviewTelemetryPreservesFieldsThatFailed(t *testing.T) {
	m := testModel()
	m.status.Service = domain.ServiceStatus{Active: true, State: "active"}
	m.status.Memory = 1024
	m.status.ConnectionCount = 8
	m.status.Traffic = domain.Traffic{UpTotal: 30, DownTotal: 40}
	m.overviewRequest = requestState{inFlight: true, generation: 4}
	updated, _ := m.Update(overviewTelemetryMsg{
		telemetry: OverviewTelemetry{
			Memory: 0, ConnectionCount: 2,
			Traffic:              domain.Traffic{UpTotal: 0, DownTotal: 0},
			ConnectionCountValid: true,
		},
		err:        errors.New("部分遥测失败"),
		generation: 4,
	})
	m = updated.(Model)
	if m.status.Memory != 1024 || m.status.ConnectionCount != 2 || m.status.Traffic.UpTotal != 30 || m.status.Traffic.DownTotal != 40 {
		t.Fatalf("invalid telemetry fields overwrote valid state: %#v", m.status)
	}
}

func TestFilterCachesResizeWithoutRetainingLargeSnapshots(t *testing.T) {
	m := testModel()
	m.filter = "node"
	m.groups = []domain.ProxyGroup{
		{Name: "A", Proxies: numberedProxies(3)},
		{Name: "B", Proxies: numberedProxies(3)},
		{Name: "C", Proxies: numberedProxies(3)},
	}
	m.groupsVersion++
	m.ensureGroupViews()

	m.groups = []domain.ProxyGroup{{Name: "A", Proxies: numberedProxies(1)}}
	m.groupsVersion++
	m.invalidateGroupViews()
	m.ensureGroupViews()
	m.groups = []domain.ProxyGroup{
		{Name: "A", Proxies: numberedProxies(2)},
		{Name: "B", Proxies: numberedProxies(2)},
	}
	m.groupsVersion++
	m.invalidateGroupViews()
	m.ensureGroupViews()
	if len(m.currentGroupViews()) != 2 {
		t.Fatalf("group cache did not survive shrink/grow: %#v", m.currentGroupViews())
	}

	m.connections = make([]domain.Connection, 3)
	for index := range m.connections {
		m.connections[index].Host = fmt.Sprintf("node-%d.example", index)
	}
	m.connectionsVersion++
	m.invalidateConnectionViews()
	m.ensureConnectionViews()
	m.connections = m.connections[:1]
	m.connectionsVersion++
	m.invalidateConnectionViews()
	m.ensureConnectionViews()
	m.connections = append(m.connections, domain.Connection{Host: "node-new.example"})
	m.connectionsVersion++
	m.invalidateConnectionViews()
	m.ensureConnectionViews()
	if len(m.currentConnectionViews()) != 2 {
		t.Fatalf("connection cache did not survive shrink/grow: %#v", m.currentConnectionViews())
	}

	m.groups = []domain.ProxyGroup{{Name: "large", Proxies: numberedProxies(2_000)}}
	m.groupsVersion++
	m.invalidateGroupViews()
	m.ensureGroupViews()
	m.filter = ""
	m.groups = []domain.ProxyGroup{{Name: "small", Proxies: numberedProxies(1)}}
	m.groupsVersion++
	m.invalidateGroupViews()
	m.ensureGroupViews()
	if len(m.groupSearch) != 0 {
		t.Fatalf("group search remained populated without an active filter: len=%d", len(m.groupSearch))
	}
	if cap(m.groupSearch) > 0 {
		cached := m.groupSearch[:1][0]
		if cached.name != "" || cached.proxies != nil {
			t.Fatalf("group search retained stale strings: %#v", cached)
		}
	}

	m.filter = "node"
	m.connections = make([]domain.Connection, 2_000)
	m.connectionsVersion++
	m.invalidateConnectionViews()
	m.ensureConnectionViews()
	m.filter = ""
	m.connections = []domain.Connection{{Host: "node-small.example"}}
	m.connectionsVersion++
	m.invalidateConnectionViews()
	m.ensureConnectionViews()
	if len(m.connectionSearch) != 0 || cap(m.connectionSearch) != 0 {
		t.Fatalf("connection search retained stale strings without an active filter: len=%d cap=%d", len(m.connectionSearch), cap(m.connectionSearch))
	}
}

func BenchmarkAppendLogRing(b *testing.B) {
	m := testModel()
	m.logs = make([]domain.LogEntry, logBufferMaximum)
	entry := domain.LogEntry{Time: "12:00:00", Level: "info", Message: "connection accepted"}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		m.appendLog(entry)
	}
}

func BenchmarkFilterLargeProxyList(b *testing.B) {
	m := testModel()
	m.groups = []domain.ProxyGroup{{Name: "PROXY", Proxies: numberedProxies(10_000)}}
	m.groupsVersion++
	b.ReportAllocs()
	b.ResetTimer()
	filters := []string{"node-01", "node-21", "node-41", "node-61", "node-81"}
	for index := range b.N {
		m.filter = filters[index%len(filters)]
		m.invalidateGroupViews()
		m.ensureGroupViews()
	}
}

func BenchmarkCachedProxyView(b *testing.B) {
	m := testModel()
	m.groups = []domain.ProxyGroup{{Name: "PROXY", Proxies: numberedProxies(10_000)}}
	m.groupsVersion++
	m.filter = "node-42"
	m.ensureGroupViews()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if len(m.currentGroupViews()) == 0 {
			b.Fatal("cached view disappeared")
		}
	}
}
