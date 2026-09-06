package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"mihomoctl/internal/domain"
)

type uiRecordingBackend struct {
	*fakeBackend
	removedProfile   string
	closedConnection string
}

type uiLogRecordingBackend struct {
	*fakeBackend
	watchLogsCalls int
	levels         []string
}

func (b *uiLogRecordingBackend) WatchLogs(_ context.Context, level string) (<-chan domain.LogEntry, <-chan error) {
	b.watchLogsCalls++
	b.levels = append(b.levels, level)
	return make(chan domain.LogEntry), make(chan error)
}

func (b *uiRecordingBackend) RemoveProfile(_ context.Context, name string) error {
	b.removedProfile = name
	return nil
}

func (b *uiRecordingBackend) CloseConnection(_ context.Context, id string) error {
	b.closedConnection = id
	return nil
}

func updateUIModel(t *testing.T, m Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	updated, cmd := m.Update(msg)
	result, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want tui.Model", updated)
	}
	return result, cmd
}

func keyPress(code rune, text string) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code, Text: text}
}

func selectedRenderLine(view, label string) string {
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(ansi.Strip(line), label) {
			return line
		}
	}
	return ""
}

func sgrHasParameter(value, parameter string) bool {
	for {
		start := strings.Index(value, "\x1b[")
		if start < 0 {
			return false
		}
		value = value[start+2:]
		end := strings.IndexByte(value, 'm')
		if end < 0 {
			return false
		}
		for _, field := range strings.Split(value[:end], ";") {
			if field == parameter {
				return true
			}
		}
		value = value[end+1:]
	}
}

func TestUIRefactorResponsiveLayoutsWithLongCJK(t *testing.T) {
	longText := strings.Repeat("跨区域智能代理节点", 18)
	base := time.Date(2026, time.September, 6, 12, 0, 0, 0, time.Local)

	for _, size := range [][2]int{{60, 16}, {80, 24}, {120, 30}, {180, 50}} {
		for currentPage := pageOverview; currentPage <= pageSettings; currentPage++ {
			name := fmt.Sprintf("%dx%d/page-%d", size[0], size[1], currentPage)
			t.Run(name, func(t *testing.T) {
				m := testModel()
				m.width, m.height, m.page = size[0], size[1], currentPage
				m.status.ActiveProfile = longText
				m.status.CoreVersion = longText
				m.status.Traffic = domain.Traffic{Down: 8 << 20, Up: 21 << 10, DownTotal: 90 << 30, UpTotal: 7 << 30}
				m.trafficHistory = make([]trafficSample, trafficHistoryLimit)
				for i := range m.trafficHistory {
					m.trafficHistory[i] = trafficSample{at: base.Add(time.Duration(i) * time.Second), down: int64(i+1) << 10, up: int64(i+1) << 7}
				}

				proxies := make([]domain.Proxy, 48)
				profiles := make([]domain.Profile, 48)
				connections := make([]domain.Connection, 48)
				logs := make([]domain.LogEntry, 48)
				for i := range proxies {
					suffix := fmt.Sprintf("-%02d", i)
					proxies[i] = domain.Proxy{Name: longText + suffix, Type: "Hysteria2-" + longText, Alive: i%3 != 0, Delay: uint16(20 + i)}
					profiles[i] = domain.Profile{Name: "配置" + longText + suffix, Kind: domain.ProfileRemote, LastUpdated: base.Add(time.Duration(i) * time.Minute)}
					connections[i] = domain.Connection{ID: "connection" + suffix, Host: "目标" + longText + suffix, Process: "进程" + longText, Rule: "规则" + longText}
					logs[i] = domain.LogEntry{Time: "12:00:00", Level: "warning", Message: "日志" + longText + suffix}
				}
				m.groups = []domain.ProxyGroup{{Name: "策略组" + longText, Now: proxies[len(proxies)-1].Name, Proxies: proxies}}
				m.profiles, m.connections, m.logs = profiles, connections, logs
				m.proxyCursor, m.profileCursor, m.connectionCursor, m.settingCursor = len(proxies)-1, len(profiles)-1, len(connections)-1, len(settingOrder)-1
				m.clampCursors()

				view := m.render()
				lines := strings.Split(view, "\n")
				if len(lines) != size[1] {
					t.Fatalf("rendered %d lines, want %d", len(lines), size[1])
				}
				for lineNumber, line := range lines {
					if width := ansi.StringWidth(line); width > size[0] {
						t.Fatalf("line %d width = %d, terminal width = %d: %q", lineNumber+1, width, size[0], ansi.Strip(line))
					}
				}
				plain := ansi.Strip(view)
				for _, required := range []string{"MIHOMOCTL", "?帮助", "q退出"} {
					if !strings.Contains(plain, required) {
						t.Fatalf("responsive view missing %q:\n%s", required, plain)
					}
				}
			})
		}
	}
}

func TestUIRefactorSelectedNodeVisibleInColorAndNoColor(t *testing.T) {
	original, existed := os.LookupEnv("NO_COLOR")
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv("NO_COLOR", original)
			return
		}
		_ = os.Unsetenv("NO_COLOR")
	})

	for _, noColor := range []bool{false, true} {
		name := "color"
		if noColor {
			name = "no-color"
		}
		t.Run(name, func(t *testing.T) {
			if noColor {
				if err := os.Setenv("NO_COLOR", "1"); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Unsetenv("NO_COLOR"); err != nil {
				t.Fatal(err)
			}

			m := testModel()
			m.width, m.height, m.page = 80, 24, pageProxies
			m.groups[0].Proxies = []domain.Proxy{
				{Name: "ordinary-node", Type: "VLESS", Alive: true, Delay: 18},
				{Name: "selected-full-view-node", Type: "AnyTLS", Alive: true, Delay: 31},
			}
			m.proxyCursor = 1
			m.syncViewports()
			line := selectedRenderLine(m.render(), "selected-full-view-node")
			if line == "" || !strings.Contains(ansi.Strip(line), ">") {
				t.Fatalf("selected node is not visibly marked: %q", ansi.Strip(line))
			}
			if !strings.Contains(line, "\x1b[") {
				t.Fatalf("selected node has no terminal highlight: %q", line)
			}
			if reversed := sgrHasParameter(line, "7"); reversed != noColor {
				t.Fatalf("reverse highlight = %v, want %v: %q", reversed, noColor, line)
			}
		})
	}
}

func TestUIRefactorConfirmationKeepsImmutableTarget(t *testing.T) {
	t.Run("profile disappears", func(t *testing.T) {
		m := testModel()
		backend := &uiRecordingBackend{fakeBackend: m.backend.(*fakeBackend)}
		m.backend = backend
		m.page = pageProfiles
		m.profiles = []domain.Profile{
			{Name: "keep", Kind: domain.ProfileRemote},
			{Name: "remove-original", Kind: domain.ProfileRemote},
		}
		m.profileCursor = 1

		m, _ = updateUIModel(t, m, keyPress('d', "d"))
		if m.confirm != confirmRemoveProfile || m.confirmTarget.name != "remove-original" {
			t.Fatalf("confirmation did not snapshot profile: mode=%v target=%+v", m.confirm, m.confirmTarget)
		}
		m.profiles = nil
		if prompt := ansi.Strip(m.confirmText()); !strings.Contains(prompt, "remove-original") {
			t.Fatalf("confirmation prompt lost snapshotted profile: %q", prompt)
		}

		var cmd tea.Cmd
		m, cmd = updateUIModel(t, m, keyPress('y', "y"))
		if cmd == nil {
			t.Fatal("confirming removal returned no command")
		}
		_ = cmd()
		if backend.removedProfile != "remove-original" {
			t.Fatalf("removed profile = %q, want immutable target", backend.removedProfile)
		}
	})

	t.Run("connection list reorders", func(t *testing.T) {
		m := testModel()
		backend := &uiRecordingBackend{fakeBackend: m.backend.(*fakeBackend)}
		m.backend = backend
		m.page = pageConnections
		m.connections = []domain.Connection{
			{ID: "connection-a", Host: "a.example"},
			{ID: "connection-original", Host: "original.example"},
		}
		m.connectionCursor = 1

		m, _ = updateUIModel(t, m, keyPress(tea.KeyEnter, ""))
		if m.confirm != confirmCloseConnection || m.confirmTarget.id != "connection-original" {
			t.Fatalf("confirmation did not snapshot connection: mode=%v target=%+v", m.confirm, m.confirmTarget)
		}
		m.connections = []domain.Connection{{ID: "replacement", Host: "replacement.example"}}

		var cmd tea.Cmd
		m, cmd = updateUIModel(t, m, keyPress('y', "y"))
		if cmd == nil {
			t.Fatal("confirming close returned no command")
		}
		_ = cmd()
		if backend.closedConnection != "connection-original" {
			t.Fatalf("closed connection = %q, want immutable target", backend.closedConnection)
		}
	})
}

func TestUIRefactorBackgroundRefreshDoesNotOwnLoadingState(t *testing.T) {
	m := testModel()
	m.loading = true
	m.mutationGeneration = 41

	background := []tea.Msg{
		statusMsg{status: m.status},
		groupsMsg{groups: m.groups},
		profilesMsg{profiles: m.profiles},
		connectionsMsg{connections: m.connections},
		scheduleMsg{status: domain.ScheduleStatus{Enabled: true}},
	}
	for _, msg := range background {
		m, _ = updateUIModel(t, m, msg)
		if !m.loading {
			t.Fatalf("background %T cleared mutation loading state", msg)
		}
	}

	m, _ = updateUIModel(t, m, operationMsg{message: "完成", generation: 40})
	if !m.loading {
		t.Fatal("stale operation response cleared current loading state")
	}
	m, _ = updateUIModel(t, m, operationMsg{message: "完成", generation: 41})
	if m.loading {
		t.Fatal("matching operation response left loading stuck")
	}
}

func TestUIRefactorNavigationAndRefreshStayNonBlocking(t *testing.T) {
	m := testModel()
	m.page = pageOverview
	var cmd tea.Cmd
	m, cmd = updateUIModel(t, m, keyPress(tea.KeyTab, ""))
	if m.loading || m.page != pageProxies || cmd == nil {
		t.Fatalf("page navigation state: page=%v loading=%v cmd=%v", m.page, m.loading, cmd != nil)
	}
	m, cmd = updateUIModel(t, m, keyPress('r', "r"))
	if m.loading || cmd == nil {
		t.Fatalf("manual refresh became blocking: loading=%v cmd=%v", m.loading, cmd != nil)
	}
}

func TestUIRefactorErrorsAreIsolatedBySource(t *testing.T) {
	m := testModel()
	m.statusRequest = requestState{inFlight: true, generation: 1}
	m, _ = updateUIModel(t, m, statusMsg{err: errors.New("状态接口失败"), generation: 1})
	if m.err != "状态接口失败" {
		t.Fatalf("status error = %q", m.err)
	}

	m.groupRequest = requestState{inFlight: true, generation: 1}
	m, _ = updateUIModel(t, m, groupsMsg{err: errors.New("节点接口失败"), generation: 1})
	if m.err != "节点接口失败" {
		t.Fatalf("newest error = %q, want groups error", m.err)
	}

	m.groupRequest = requestState{inFlight: true, generation: 2}
	m, _ = updateUIModel(t, m, groupsMsg{groups: m.groups, generation: 2})
	if m.err != "状态接口失败" {
		t.Fatalf("groups success cleared unrelated status error: %q", m.err)
	}

	m.statusRequest = requestState{inFlight: true, generation: 2}
	m, _ = updateUIModel(t, m, statusMsg{status: m.status, generation: 2})
	if m.err != "" || len(m.errors) != 0 {
		t.Fatalf("successful sources left errors: current=%q all=%#v", m.err, m.errors)
	}
}

func TestUIRefactorPausedLogsKeepBoundedBufferAndUnread(t *testing.T) {
	m := testModel()
	m.page, m.logPaused, m.logGeneration = pageLogs, true, 7
	m.logs = nil
	m.logCh = make(chan domain.LogEntry)
	for i := 0; i < 1005; i++ {
		entry := domain.LogEntry{Time: "12:00:00", Level: "info", Message: "message-" + strconv.Itoa(i)}
		m, _ = updateUIModel(t, m, logMsg{entry: entry, ok: true, generation: 7})
	}
	if len(m.logs) != 1000 {
		t.Fatalf("paused log buffer length = %d, want 1000", len(m.logs))
	}
	if m.logs[0].Message != "message-5" || m.logs[len(m.logs)-1].Message != "message-1004" {
		t.Fatalf("paused log buffer retained wrong range: first=%q last=%q", m.logs[0].Message, m.logs[len(m.logs)-1].Message)
	}
	if m.logUnread != 1005 {
		t.Fatalf("paused unread count = %d, want 1005", m.logUnread)
	}
	if view := ansi.Strip(m.render()); !strings.Contains(view, "+1005 未读") {
		t.Fatalf("paused view does not expose unread count:\n%s", view)
	}

	m, _ = updateUIModel(t, m, keyPress(tea.KeyEnd, ""))
	if m.logOffset != 0 || m.logUnread != 0 {
		t.Fatalf("End did not follow newest logs: offset=%d unread=%d", m.logOffset, m.logUnread)
	}
	m.logUnread = 3
	m, _ = updateUIModel(t, m, keyPress(' ', " "))
	if m.logPaused || m.logOffset != 0 || m.logUnread != 0 {
		t.Fatalf("resume state: paused=%v offset=%d unread=%d", m.logPaused, m.logOffset, m.logUnread)
	}
}

func TestUIRefactorLogNavigationKeys(t *testing.T) {
	m := testModel()
	m.width, m.height, m.page = 80, 24, pageLogs
	m.logs = make([]domain.LogEntry, 80)
	for i := range m.logs {
		m.logs[i] = domain.LogEntry{Level: "info", Message: fmt.Sprintf("log-%02d", i)}
	}

	m, _ = updateUIModel(t, m, keyPress(tea.KeyHome, ""))
	if m.logOffset == 0 {
		t.Fatal("Home did not move logs to the oldest page")
	}
	homeOffset := m.logOffset
	m, _ = updateUIModel(t, m, keyPress(tea.KeyPgDown, ""))
	if m.logOffset >= homeOffset {
		t.Fatalf("PgDn did not move toward newer logs: %d -> %d", homeOffset, m.logOffset)
	}
	m, _ = updateUIModel(t, m, keyPress(tea.KeyEnd, ""))
	if m.logOffset != 0 {
		t.Fatalf("End log offset = %d, want 0", m.logOffset)
	}
	m, _ = updateUIModel(t, m, keyPress(tea.KeyPgUp, ""))
	if m.logOffset == 0 {
		t.Fatal("PgUp did not move toward older logs")
	}
}

func TestUIRefactorListNavigationKeys(t *testing.T) {
	tests := []struct {
		name    string
		page    page
		maximum int
		setup   func(*Model)
		cursor  func(Model) int
	}{
		{
			name: "proxies", page: pageProxies, maximum: 49,
			setup:  func(m *Model) { m.groups[0].Proxies = numberedProxies(50) },
			cursor: func(m Model) int { return m.proxyCursor },
		},
		{
			name: "profiles", page: pageProfiles, maximum: 49,
			setup: func(m *Model) {
				m.profiles = make([]domain.Profile, 50)
				for i := range m.profiles {
					m.profiles[i].Name = fmt.Sprintf("profile-%02d", i)
				}
			},
			cursor: func(m Model) int { return m.profileCursor },
		},
		{
			name: "connections", page: pageConnections, maximum: 49,
			setup: func(m *Model) {
				m.connections = make([]domain.Connection, 50)
				for i := range m.connections {
					m.connections[i] = domain.Connection{ID: fmt.Sprintf("connection-%02d", i), Host: fmt.Sprintf("host-%02d", i)}
				}
			},
			cursor: func(m Model) int { return m.connectionCursor },
		},
		{
			name: "settings", page: pageSettings, maximum: len(settingOrder) - 1,
			setup:  func(*Model) {},
			cursor: func(m Model) int { return m.settingCursor },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := testModel()
			m.width, m.height, m.page = 60, 16, tt.page
			tt.setup(&m)
			m.clampCursors()

			m, _ = updateUIModel(t, m, keyPress(tea.KeyEnd, ""))
			if got := tt.cursor(m); got != tt.maximum {
				t.Fatalf("End cursor = %d, want %d", got, tt.maximum)
			}
			m, _ = updateUIModel(t, m, keyPress(tea.KeyHome, ""))
			if got := tt.cursor(m); got != 0 {
				t.Fatalf("Home cursor = %d, want 0", got)
			}
			m, _ = updateUIModel(t, m, keyPress(tea.KeyPgDown, ""))
			if got := tt.cursor(m); got <= 0 || got > tt.maximum {
				t.Fatalf("PgDn cursor = %d, want within (0,%d]", got, tt.maximum)
			}
			m, _ = updateUIModel(t, m, keyPress(tea.KeyPgUp, ""))
			if got := tt.cursor(m); got != 0 {
				t.Fatalf("PgUp cursor = %d, want 0", got)
			}
		})
	}
}

func TestUIRefactorDelayTimeoutPreservesCoreAliveState(t *testing.T) {
	m := testModel()
	m.page = pageProxies
	m.groups = []domain.ProxyGroup{{
		Name: "PROXY",
		All:  []string{"success", "zero", "missing"},
		Proxies: []domain.Proxy{
			{Name: "success", Type: "VLESS", Alive: false, Delay: 71},
			{Name: "zero", Type: "AnyTLS", Alive: true, Delay: 72},
			{Name: "missing", Type: "Hysteria2", Alive: true, Delay: 73},
		},
	}}
	m.loading = true
	m.mutationGeneration = 9

	m, _ = updateUIModel(t, m, groupTestMsg{
		group:       "PROXY",
		delays:      map[string]uint16{"success": 24, "zero": 0},
		completedAt: time.Now(),
		generation:  9,
	})
	proxies := m.groups[0].Proxies
	if proxies[0].Alive || !proxies[1].Alive || !proxies[2].Alive {
		t.Fatalf("delay test overwrote core Alive state: %#v", proxies)
	}
	if proxies[0].Delay != 71 || proxies[1].Delay != 72 || proxies[2].Delay != 73 {
		t.Fatalf("delay test overwrote core delays: %#v", proxies)
	}
	states := m.proxyTestStates["PROXY"]
	if states["success"].state != proxyTestSuccess || states["success"].delay != 24 {
		t.Fatalf("success test state = %#v", states["success"])
	}
	for _, name := range []string{"zero", "missing"} {
		if states[name].state != proxyTestTimeout {
			t.Fatalf("%s test state = %#v, want timeout", name, states[name])
		}
	}
	if m.loading {
		t.Fatal("completed delay test left loading stuck")
	}
}

func TestUIRefactorRequestSingleFlightAndGeneration(t *testing.T) {
	m := testModel()
	m.status = domain.RuntimeStatus{ActiveProfile: "original"}
	now := time.Date(2026, time.September, 6, 12, 0, 0, 0, time.UTC)
	first := m.beginStatusRefresh(now, true)
	if first == nil || !m.statusRequest.inFlight {
		t.Fatal("first status request did not start")
	}
	firstGeneration := m.statusRequest.generation
	if duplicate := m.beginStatusRefresh(now, true); duplicate != nil || !m.statusRequest.queued {
		t.Fatalf("duplicate request was not coalesced: cmd=%v state=%+v", duplicate != nil, m.statusRequest)
	}

	m, _ = updateUIModel(t, m, statusMsg{
		status:     domain.RuntimeStatus{ActiveProfile: "stale"},
		generation: firstGeneration + 1,
	})
	if m.status.ActiveProfile != "original" || !m.statusRequest.inFlight {
		t.Fatalf("mismatched generation was accepted: profile=%q state=%+v", m.status.ActiveProfile, m.statusRequest)
	}

	var queued tea.Cmd
	m, queued = updateUIModel(t, m, statusMsg{
		status:      domain.RuntimeStatus{ActiveProfile: "accepted"},
		requestedAt: now,
		generation:  firstGeneration,
	})
	if m.status.ActiveProfile != "accepted" {
		t.Fatalf("matching generation was not applied: %q", m.status.ActiveProfile)
	}
	if queued == nil || !m.statusRequest.inFlight || m.statusRequest.generation <= firstGeneration {
		t.Fatalf("queued forced refresh was not started: cmd=%v state=%+v", queued != nil, m.statusRequest)
	}

	m, _ = updateUIModel(t, m, statusMsg{
		status:      domain.RuntimeStatus{ActiveProfile: "late-old"},
		requestedAt: now.Add(time.Second),
		generation:  firstGeneration,
	})
	if m.status.ActiveProfile != "accepted" {
		t.Fatalf("late old response overwrote current state: %q", m.status.ActiveProfile)
	}
}

func TestUIRefactorConnectionSelectionWithoutIDSurvivesReorder(t *testing.T) {
	m := testModel()
	m.page = pageConnections
	m.connections = []domain.Connection{
		{Host: "first.example", Process: "curl"},
		{Host: "selected.example", Process: "browser"},
	}
	m.connectionCursor = 1

	m, _ = updateUIModel(t, m, connectionsMsg{connections: []domain.Connection{
		{Host: "selected.example", Process: "browser"},
		{Host: "first.example", Process: "curl"},
	}})
	if m.connectionCursor != 0 || m.connections[m.connectionCursor].Host != "selected.example" {
		t.Fatalf("ID-less connection selection moved after reorder: cursor=%d connections=%#v", m.connectionCursor, m.connections)
	}
}

func TestUIRefactorFallbackChartPeakUsesVisibleSamples(t *testing.T) {
	history := []trafficSample{
		{down: 10_000, up: 20_000},
		{down: 10, up: 20},
		{down: 30, up: 40},
	}
	_, _, downPeak, upPeak := trafficChartSeries(history, 2)
	if downPeak != 30 || upPeak != 40 {
		t.Fatalf("fallback peaks = %d/%d, want visible-window 30/40", downPeak, upPeak)
	}
}

func TestUIRefactorHeaderOnlyTreatsStatusFailureAsUnavailable(t *testing.T) {
	m := testModel()
	m.status.Service.Active = false
	m.setSourceError(errorProfiles, errors.New("配置刷新失败"))
	if header := ansi.Strip(m.renderHeader()); !strings.Contains(header, "已停止") || strings.Contains(header, "不可用") {
		t.Fatalf("profile error changed service state: %q", header)
	}

	m.setSourceError(errorStatus, errors.New("状态刷新失败"))
	if header := ansi.Strip(m.renderHeader()); !strings.Contains(header, "不可用") {
		t.Fatalf("status error was not reflected in header: %q", header)
	}
}

func TestUIRefactorTrafficStreamAndStatusMergeRateAndTotals(t *testing.T) {
	m := testModel()
	m.status.Service.Active = true
	m.status.Traffic = domain.Traffic{Up: 1, Down: 2, UpTotal: 100, DownTotal: 200}
	m.trafficGeneration = 4
	m.trafficCh = make(chan domain.Traffic)

	m, _ = updateUIModel(t, m, trafficMsg{
		traffic:    domain.Traffic{Up: 30, Down: 40},
		at:         time.Now(),
		ok:         true,
		generation: 4,
	})
	if got := m.status.Traffic; got.Up != 30 || got.Down != 40 || got.UpTotal != 100 || got.DownTotal != 200 {
		t.Fatalf("stream sample erased cumulative totals: %#v", got)
	}

	m.statusRequest = requestState{inFlight: true, generation: 2}
	m, _ = updateUIModel(t, m, statusMsg{
		status: domain.RuntimeStatus{
			Service: domain.ServiceStatus{Active: true},
			Traffic: domain.Traffic{Up: 3, Down: 4, UpTotal: 150, DownTotal: 250},
		},
		requestedAt: time.Now(),
		generation:  2,
	})
	if got := m.status.Traffic; got.Up != 30 || got.Down != 40 || got.UpTotal != 150 || got.DownTotal != 250 {
		t.Fatalf("status snapshot did not merge with stream rates: %#v", got)
	}
}

func TestUIRefactorGroupTestRequestFailureKeepsLastResults(t *testing.T) {
	m := testModel()
	m.page = pageProxies
	m.proxyTestStates["PROXY"] = map[string]proxyTestResult{
		"香港 01": {state: proxyTestSuccess, delay: 45, testedAt: time.Now().Add(-time.Minute)},
	}
	m.loading = true
	m.mutationGeneration = 3

	m, _ = updateUIModel(t, m, groupTestMsg{
		group:      "PROXY",
		err:        errors.New("controller disconnected"),
		generation: 3,
	})
	result := m.proxyTestStates["PROXY"]["香港 01"]
	if result.state != proxyTestSuccess || result.delay != 45 {
		t.Fatalf("request failure replaced previous result: %#v", result)
	}
	if m.loading || !strings.Contains(m.err, "controller disconnected") {
		t.Fatalf("request failure state: loading=%v error=%q", m.loading, m.err)
	}
}

func TestUIRefactorTrafficClosureReconnectsAfterBothChannelsClose(t *testing.T) {
	m := testModel()
	m.status.Service.Active = true
	m.trafficGeneration = 8
	m.trafficCh = make(chan domain.Traffic)
	m.trafficErrCh = make(chan error)

	var reconnect tea.Cmd
	m, reconnect = updateUIModel(t, m, trafficMsg{generation: 8, ok: false})
	if reconnect != nil || m.trafficCh != nil || m.trafficErrCh == nil {
		t.Fatalf("first channel close restarted early: cmd=%v channels=%v/%v", reconnect != nil, m.trafficCh != nil, m.trafficErrCh != nil)
	}
	m, reconnect = updateUIModel(t, m, trafficErrMsg{generation: 8, ok: false})
	if reconnect == nil || !m.trafficReconnectPending || m.trafficGeneration != 9 {
		t.Fatalf("second channel close did not schedule one reconnect: cmd=%v pending=%v generation=%d", reconnect != nil, m.trafficReconnectPending, m.trafficGeneration)
	}

	m, duplicate := updateUIModel(t, m, trafficErrMsg{generation: 8, ok: false})
	if duplicate != nil || m.trafficGeneration != 9 {
		t.Fatalf("stale channel close scheduled duplicate reconnect: cmd=%v generation=%d", duplicate != nil, m.trafficGeneration)
	}
}

func TestUIRefactorLogsWaitForRunningService(t *testing.T) {
	m := testModel()
	backend := &uiLogRecordingBackend{fakeBackend: m.backend.(*fakeBackend)}
	m.backend = backend
	m.page = pageLogs
	m.status.Service.Active = false
	m.setSourceError(errorLogs, errors.New("日志流已断开"))

	if cmd := m.beginLogs(true); cmd != nil {
		t.Fatal("stopped service started a log stream")
	}
	if backend.watchLogsCalls != 0 || m.logStreamPresent() {
		t.Fatalf("stopped service touched log backend: calls=%d stream=%v", backend.watchLogsCalls, m.logStreamPresent())
	}
	if _, ok := m.errors[errorLogs]; ok {
		t.Fatal("stale log error was not cleared while service is stopped")
	}
	view := ansi.Strip(m.renderLogs(12))
	for _, text := range []string{"服务已停止", "Mihomo 服务未运行"} {
		if !strings.Contains(view, text) {
			t.Fatalf("stopped log view missing %q:\n%s", text, view)
		}
	}

	m.statusRequest = requestState{inFlight: true, generation: 7}
	m, cmd := updateUIModel(t, m, statusMsg{
		status:      domain.RuntimeStatus{Service: domain.ServiceStatus{Active: true}},
		requestedAt: time.Now(),
		generation:  7,
	})
	if cmd == nil || !m.logConnecting {
		t.Fatalf("running service did not start the log stream: cmd=%v connecting=%v", cmd != nil, m.logConnecting)
	}
}

func TestUIRefactorStoppingServiceClosesLogsAndClearsError(t *testing.T) {
	m := testModel()
	m.page = pageLogs
	m.logGeneration = 4
	m.logCh = make(chan domain.LogEntry)
	m.logErrCh = make(chan error)
	streamCtx, cancel := context.WithCancel(context.Background())
	m.logCancel = cancel
	m.logReconnectPending = true
	m.setSourceError(errorLogs, errors.New("controller disconnected"))
	m.statusRequest = requestState{inFlight: true, generation: 9}

	m, _ = updateUIModel(t, m, statusMsg{
		status:      domain.RuntimeStatus{Service: domain.ServiceStatus{Active: false}},
		requestedAt: time.Now(),
		generation:  9,
	})
	if m.logStreamPresent() || m.logRetry != 0 {
		t.Fatalf("stopped service retained log stream state: present=%v retry=%d", m.logStreamPresent(), m.logRetry)
	}
	if _, ok := m.errors[errorLogs]; ok {
		t.Fatal("stopped service retained its log error")
	}
	select {
	case <-streamCtx.Done():
	default:
		t.Fatal("stopped service did not cancel the log context")
	}
}
