package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"mihomoctl/internal/domain"
)

type fakeBackend struct {
	status      domain.RuntimeStatus
	groups      []domain.ProxyGroup
	profiles    []domain.Profile
	conns       []domain.Connection
	action      string
	schedule    *bool
	delays      map[string]uint16
	groupErr    error
	tested      string
	configKey   string
	configValue string
	configCalls int
	configErr   error
}

func (f *fakeBackend) Status(context.Context) (domain.RuntimeStatus, error)     { return f.status, nil }
func (f *fakeBackend) Groups(context.Context) ([]domain.ProxyGroup, error)      { return f.groups, nil }
func (f *fakeBackend) Profiles(context.Context) ([]domain.Profile, error)       { return f.profiles, nil }
func (f *fakeBackend) Connections(context.Context) ([]domain.Connection, error) { return f.conns, nil }
func (f *fakeBackend) WatchTraffic(context.Context) (<-chan domain.Traffic, <-chan error) {
	traffic := make(chan domain.Traffic)
	errs := make(chan error)
	return traffic, errs
}
func (f *fakeBackend) WatchLogs(context.Context, string) (<-chan domain.LogEntry, <-chan error) {
	entries := make(chan domain.LogEntry)
	errs := make(chan error)
	return entries, errs
}
func (f *fakeBackend) Service(_ context.Context, action string) error { f.action = action; return nil }
func (f *fakeBackend) SetMode(context.Context, domain.Mode) error     { return nil }
func (f *fakeBackend) SetTUN(context.Context, bool) error             { return nil }
func (f *fakeBackend) SetConfig(_ context.Context, key, value string) error {
	f.configKey = key
	f.configValue = value
	f.configCalls++
	return f.configErr
}
func (f *fakeBackend) SetSchedule(_ context.Context, enabled bool) error {
	f.schedule = &enabled
	return nil
}
func (f *fakeBackend) ScheduleStatus(context.Context) (domain.ScheduleStatus, error) {
	return domain.ScheduleStatus{Enabled: true, State: "running"}, nil
}
func (f *fakeBackend) SelectProxy(context.Context, string, string) error { return nil }
func (f *fakeBackend) TestGroup(_ context.Context, group string) (map[string]uint16, error) {
	f.tested = group
	return f.delays, f.groupErr
}
func (f *fakeBackend) AddProfile(context.Context, string, string, time.Duration) error { return nil }
func (f *fakeBackend) UpdateProfile(context.Context, string) error                     { return nil }
func (f *fakeBackend) UseProfile(context.Context, string) error                        { return nil }
func (f *fakeBackend) RemoveProfile(context.Context, string) error                     { return nil }
func (f *fakeBackend) CloseConnection(context.Context, string) error                   { return nil }
func (f *fakeBackend) CloseAllConnections(context.Context) error                       { return nil }

func testModel() Model {
	backend := &fakeBackend{
		status: domain.RuntimeStatus{
			Service: domain.ServiceStatus{Active: true}, ActiveProfile: "日常",
			Mode: domain.ModeRule, MixedPort: 7890, LogLevel: "info", ConfigAvailable: true,
		},
		groups:   []domain.ProxyGroup{{Name: "PROXY", Now: "香港 01", Proxies: []domain.Proxy{{Name: "香港 01", Type: "VLESS", Alive: true, Delay: 45}}}},
		profiles: []domain.Profile{{Name: "日常", Kind: domain.ProfileRemote, Active: true}},
		conns:    []domain.Connection{{ID: "1", Host: "example.com", Process: "curl", Download: 1024}},
	}
	m := New(context.Background(), backend)
	m.width, m.height = 100, 30
	m.status, m.groups, m.profiles, m.connections = backend.status, backend.groups, backend.profiles, backend.conns
	return m
}

func numberedProxies(count int) []domain.Proxy {
	proxies := make([]domain.Proxy, count)
	for i := range proxies {
		proxies[i] = domain.Proxy{Name: fmt.Sprintf("node-%02d", i), Type: "Hysteria2", Alive: true}
	}
	return proxies
}

func selectedItemVisible(view, item string) bool {
	for _, line := range strings.Split(ansi.Strip(view), "\n") {
		if strings.Contains(line, ">") && strings.Contains(line, item) {
			return true
		}
	}
	return false
}

func requireSelectedVisible(t *testing.T, m Model, item string) {
	t.Helper()
	if view := m.render(); !selectedItemVisible(view, item) {
		t.Fatalf("selected item %q is outside the viewport:\n%s", item, ansi.Strip(view))
	}
}

func TestRenderPagesFit(t *testing.T) {
	for _, size := range [][2]int{{60, 16}, {80, 24}, {90, 24}, {120, 36}} {
		for p := pageOverview; p <= pageSettings; p++ {
			m := testModel()
			m.width, m.height, m.page = size[0], size[1], p
			view := m.render()
			lines := strings.Split(view, "\n")
			if len(lines) != size[1] {
				t.Fatalf("page %d at %v rendered %d lines", p, size, len(lines))
			}
			for lineNumber, line := range lines {
				if width := ansi.StringWidth(line); width > size[0] {
					t.Fatalf("page %d at %v line %d width = %d", p, size, lineNumber+1, width)
				}
			}
		}
	}
}

func TestContextualFooterHints(t *testing.T) {
	tests := []struct {
		page  page
		hints []string
	}{
		{pageOverview, []string{"Enter启停", "r刷新", "Tab切页", "?帮助", "q退出"}},
		{pageProxies, []string{"↑↓选", "←→组", "Enter切换", "t测速", "/筛选", "?帮助", "q退出"}},
		{pageProfiles, []string{"↑↓选", "a添加", "u更新", "d删除", "Enter激活", "?帮助", "q退出"}},
		{pageConnections, []string{"↑↓选", "Enter关闭", "x全部", "/筛选", "?帮助", "q退出"}},
		{pageLogs, []string{"Space暂停", "/筛选", "r刷新", "?帮助", "q退出"}},
		{pageSettings, []string{"↑↓选", "Enter修改", "r刷新", "?帮助", "q退出"}},
	}

	for _, tt := range tests {
		m := testModel()
		m.width, m.height, m.page = 60, 16, tt.page
		view := ansi.Strip(m.render())
		for _, hint := range tt.hints {
			if !strings.Contains(view, hint) {
				t.Errorf("page %d missing footer hint %q:\n%s", tt.page, hint, view)
			}
		}
	}
}

func TestFooterFitsNarrowWidthsWithoutWrapping(t *testing.T) {
	for _, width := range []int{24, 32, 48, 60} {
		for p := pageOverview; p <= pageSettings; p++ {
			m := testModel()
			m.width, m.page = width, p
			footer := ansi.Strip(m.renderFooter())
			if strings.Contains(footer, "\n") {
				t.Fatalf("page %d footer wrapped at width %d: %q", p, width, footer)
			}
			if got := ansi.StringWidth(footer); got > width {
				t.Fatalf("page %d footer width at %d = %d: %q", p, width, got, footer)
			}
			for _, essential := range []string{"?帮助", "q退出"} {
				if !strings.Contains(footer, essential) {
					t.Fatalf("page %d footer at width %d missing %q: %q", p, width, essential, footer)
				}
			}
		}
	}
}

func TestFooterKeepsHelpAndQuitWithTransientMessages(t *testing.T) {
	tests := []struct {
		name  string
		apply func(*Model)
	}{
		{"loading", func(m *Model) { m.loading = true }},
		{"error", func(m *Model) { m.err = strings.Repeat("控制器不可用", 20) }},
		{"toast", func(m *Model) { m.toast = strings.Repeat("操作完成", 20) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := testModel()
			m.width = 60
			tt.apply(&m)
			footer := ansi.Strip(m.renderFooter())
			if strings.Contains(footer, "\n") || ansi.StringWidth(footer) > m.width {
				t.Fatalf("transient footer overflowed: %q", footer)
			}
			for _, essential := range []string{"?帮助", "q退出"} {
				if !strings.Contains(footer, essential) {
					t.Fatalf("transient footer missing %q: %q", essential, footer)
				}
			}
		})
	}
}

func TestPausedLogsFooterShowsResume(t *testing.T) {
	m := testModel()
	m.width, m.page, m.logPaused = 60, pageLogs, true
	footer := ansi.Strip(m.renderFooter())
	if !strings.Contains(footer, "Space继续") || strings.Contains(footer, "Space暂停") {
		t.Fatalf("paused log footer did not offer resume: %q", footer)
	}
}

func TestSmallTerminalMessage(t *testing.T) {
	m := testModel()
	m.width, m.height = 40, 10
	if got := m.render(); !strings.Contains(got, "终端空间不足") {
		t.Fatalf("missing resize message: %q", got)
	}
}

func TestFilterConnections(t *testing.T) {
	m := testModel()
	m.connections = append(m.connections, domain.Connection{ID: "2", Host: "openai.com", Process: "browser"})
	m.filter = "openai"
	got := m.filteredConnections()
	if len(got) != 1 || got[0].ID != "2" {
		t.Fatalf("unexpected filter result: %#v", got)
	}
}

func TestFilterLogs(t *testing.T) {
	m := testModel()
	m.logs = []domain.LogEntry{{Level: "info", Message: "connected"}, {Level: "error", Message: "timeout"}}
	m.filter = "TIME"
	got := m.filteredLogs()
	if len(got) != 1 || got[0].Message != "timeout" {
		t.Fatalf("unexpected log filter result: %#v", got)
	}
}

func TestSafeTextStripsUntrustedTerminalSequences(t *testing.T) {
	malicious := "safe\x1b]52;c;Y2xpcGJvYXJk\a\x1b[31mred\x1b[0m\nnext\x00"
	cleaned := safeText(malicious)
	if strings.ContainsAny(cleaned, "\x1b\a\n\r\t\x00") || strings.Contains(cleaned, "Y2xpcGJvYXJk") {
		t.Fatalf("safeText retained terminal controls: %q", cleaned)
	}
	for _, text := range []string{"safe", "red", "next"} {
		if !strings.Contains(cleaned, text) {
			t.Fatalf("safeText removed printable text %q: %q", text, cleaned)
		}
	}
}

func TestProfileInputMasksSource(t *testing.T) {
	m := testModel()
	m.page = pageProfiles
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	m = updated.(Model)
	if m.inMode != inputProfile || m.input.EchoMode != textinput.EchoPassword || m.input.EchoCharacter != profileInputMask {
		t.Fatal("profile input did not enable password echo mode")
	}

	source := "https://user:password@example.test/sub?token=private"
	m.input.SetValue(source)
	view := ansi.Strip(m.render())
	if strings.Contains(view, source) {
		t.Fatal("profile source is visible in the TUI")
	}
	if !strings.Contains(view, strings.Repeat(string(profileInputMask), len(source))) {
		t.Fatal("profile source mask is not visible in the TUI")
	}
}

func TestFilterInputIsVisibleAfterProfileInput(t *testing.T) {
	m := testModel()
	m.page = pageProfiles
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	m = updated.(Model)
	m.input.SetValue("private source")
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = updated.(Model)
	if m.inMode != inputNone || m.input.EchoMode != textinput.EchoNormal || m.input.Value() != "" {
		t.Fatal("canceling profile input did not reset the input model")
	}

	m.page = pageConnections
	m.filter = "example.com"
	updated, _ = m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	m = updated.(Model)
	if m.inMode != inputFilter || m.input.EchoMode != textinput.EchoNormal {
		t.Fatal("filter input did not use normal echo mode")
	}
	if view := ansi.Strip(m.render()); !strings.Contains(view, m.filter) {
		t.Fatal("filter value is not visible in the TUI")
	}
}

func TestSubmittingProfileResetsInput(t *testing.T) {
	m := testModel()
	m.page = pageProfiles
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	m = updated.(Model)
	m.input.SetValue("ss://credentials@example.test:443")
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("submitting profile input did not start the operation")
	}
	if m.inMode != inputNone || m.input.EchoMode != textinput.EchoNormal || m.input.Value() != "" || m.input.Placeholder != "" {
		t.Fatal("submitting profile input did not reset the input model")
	}
	updated, _ = m.Update(cmd())
	m = updated.(Model)
	if m.toast != "配置已添加，请选中后按 Enter 激活" {
		t.Fatalf("profile add toast = %q", m.toast)
	}
}

func TestStatusErrorIsVisible(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(statusMsg{err: errors.New("控制器不可用")})
	model := updated.(Model)
	if !strings.Contains(model.render(), "控制器不可用") {
		t.Fatal("error was not rendered")
	}
}

func TestStatusSamplesBoundedTrafficHistory(t *testing.T) {
	m := testModel()
	m.trafficHistory = nil
	started := time.Unix(100, 0)
	for index := range trafficHistoryLimit + 5 {
		updated, _ := m.Update(statusMsg{
			status: domain.RuntimeStatus{
				Service: domain.ServiceStatus{Active: true},
				Traffic: domain.Traffic{Up: int64(index), Down: int64(index * 2)},
			},
			requestedAt: started.Add(time.Duration(index) * time.Second),
		})
		m = updated.(Model)
	}
	if len(m.trafficHistory) != trafficHistoryLimit {
		t.Fatalf("traffic history length = %d, want %d", len(m.trafficHistory), trafficHistoryLimit)
	}
	if first := m.trafficHistory[0]; first.up != 5 || first.down != 10 {
		t.Fatalf("traffic history did not retain the newest samples: %#v", first)
	}

	updated, _ := m.Update(statusMsg{err: errors.New("temporary failure"), requestedAt: started.Add(3 * time.Minute)})
	m = updated.(Model)
	if len(m.trafficHistory) != trafficHistoryLimit {
		t.Fatalf("status error changed traffic history length: %d", len(m.trafficHistory))
	}

	updated, _ = m.Update(statusMsg{
		status:      domain.RuntimeStatus{Service: domain.ServiceStatus{Active: false}},
		requestedAt: started.Add(4 * time.Minute),
	})
	m = updated.(Model)
	if len(m.trafficHistory) != 0 {
		t.Fatalf("inactive service retained traffic history: %#v", m.trafficHistory)
	}
}

func TestStatusIgnoresLateTrafficSnapshot(t *testing.T) {
	m := testModel()
	m.trafficHistory = nil
	newer := time.Unix(200, 0)
	updated, _ := m.Update(statusMsg{
		status: domain.RuntimeStatus{
			Service: domain.ServiceStatus{Active: true},
			Traffic: domain.Traffic{Down: 200},
		},
		requestedAt: newer,
	})
	m = updated.(Model)
	updated, _ = m.Update(statusMsg{
		status: domain.RuntimeStatus{
			Service: domain.ServiceStatus{Active: true},
			Traffic: domain.Traffic{Down: 100},
		},
		requestedAt: newer.Add(-time.Second),
	})
	m = updated.(Model)
	if m.status.Traffic.Down != 200 || len(m.trafficHistory) != 1 || m.trafficHistory[0].down != 200 {
		t.Fatalf("late snapshot overwrote traffic state: status=%#v history=%#v", m.status.Traffic, m.trafficHistory)
	}
}

func TestInputModeKeepsBackgroundRefreshRunning(t *testing.T) {
	m := testModel()
	m.inMode = inputFilter
	m.input.SetValue("keep typing")

	updated, cmd := m.Update(tickMsg(time.Unix(300, 0)))
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("tick in input mode did not schedule the next refresh")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("tick in input mode returned %#v, want status and next-tick commands", batch)
	}
	if m.inMode != inputFilter || m.input.Value() != "keep typing" {
		t.Fatalf("background tick disturbed input state: mode=%v value=%q", m.inMode, m.input.Value())
	}

	updated, _ = m.Update(statusMsg{
		status: domain.RuntimeStatus{
			Service: domain.ServiceStatus{Active: true},
			Traffic: domain.Traffic{Down: 4096},
		},
		requestedAt: time.Unix(301, 0),
	})
	m = updated.(Model)
	if len(m.trafficHistory) != 1 || m.trafficHistory[0].down != 4096 {
		t.Fatalf("status update was lost in input mode: %#v", m.trafficHistory)
	}
	if m.inMode != inputFilter || m.input.Value() != "keep typing" {
		t.Fatalf("background status disturbed input state: mode=%v value=%q", m.inMode, m.input.Value())
	}
}

func TestTrafficSparklineScalingAndWidth(t *testing.T) {
	tests := []struct {
		name   string
		values []int64
		width  int
		peak   int64
		want   string
	}{
		{name: "empty", width: 4, want: "    "},
		{name: "zero baseline", values: []int64{0, 0}, width: 4, want: "  ▁▁"},
		{name: "shared scale", values: []int64{0, 25, 50, 75, 100}, width: 5, peak: 100, want: "▁▂▄▆█"},
		{name: "tail only", values: []int64{0, 25, 50, 75, 100}, width: 3, peak: 100, want: "▄▆█"},
		{name: "negative is zero", values: []int64{-1}, width: 1, peak: 10, want: "▁"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := trafficSparkline(tt.values, tt.width, tt.peak)
			if got != tt.want {
				t.Fatalf("trafficSparkline() = %q, want %q", got, tt.want)
			}
			if width := ansi.StringWidth(got); width != tt.width {
				t.Fatalf("sparkline width = %d, want %d", width, tt.width)
			}
		})
	}
}

func TestOverviewShowsTrafficChartsAtMinimumSize(t *testing.T) {
	m := testModel()
	m.width, m.height, m.page = 60, 16, pageOverview
	m.status.CoreVersion = "v1.19.30"
	m.status.TUN = true
	m.status.Traffic = domain.Traffic{Down: 8192, Up: 2048, DownTotal: 1 << 30, UpTotal: 1 << 28}
	m.status.ConnectionCount = 3
	m.status.Memory = 64 << 20
	m.trafficHistory = []trafficSample{{down: 1024, up: 128}, {down: 8192, up: 2048}}
	m.connections = []domain.Connection{{ID: "private-id", Host: "private.example", Process: "private-process"}}

	view := ansi.Strip(m.render())
	for _, text := range []string{
		"实时速度", "下载", "上传", "█", "累计下载", "累计上传",
		"活动连接", "内存", "运行状态", "活动配置", "核心版本", "模式", "TUN", "混合端口",
	} {
		if !strings.Contains(view, text) {
			t.Fatalf("minimum overview missing %q:\n%s", text, view)
		}
	}
	for _, secret := range []string{"private-id", "private.example", "private-process"} {
		if strings.Contains(view, secret) {
			t.Fatalf("overview exposed connection identity %q", secret)
		}
	}
	for _, label := range []string{"↓ 下载", "↑ 上传"} {
		for _, line := range strings.Split(view, "\n") {
			if strings.Contains(line, label) && strings.ContainsAny(line, trafficLevels) {
				label = ""
				break
			}
		}
		if label != "" {
			t.Fatalf("overview line %q has no traffic graph:\n%s", label, view)
		}
	}
}

func TestQuitCancelsContext(t *testing.T) {
	m := testModel()
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if cmd == nil {
		t.Fatal("expected quit command")
	}
	select {
	case <-m.ctx.Done():
	default:
		t.Fatal("context was not canceled")
	}
}

func TestCompactProxyPageShowsSelectableNodes(t *testing.T) {
	m := testModel()
	m.width, m.height, m.page = 70, 20, pageProxies
	view := m.render()
	if !strings.Contains(view, "香港 01") || !strings.Contains(view, "VLESS") {
		t.Fatalf("compact proxy page hid node details:\n%s", view)
	}
}

func TestProxyPageShowsGroupSelectorAboveNodeTable(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := testModel()
	m.width, m.height, m.page = 80, 24, pageProxies
	m.groups = []domain.ProxyGroup{
		{Name: "AI", Proxies: []domain.Proxy{{Name: "AI-node"}}},
		{Name: "Netflix", Proxies: []domain.Proxy{{Name: "Netflix-node"}}},
		{Name: "✈️Final", Proxies: []domain.Proxy{{Name: "Final-node"}}},
	}
	m.groupCursor = 1
	m.syncViewports()

	lines := strings.Split(ansi.Strip(m.render()), "\n")
	titleIndex, selectorIndex, headerIndex := -1, -1, -1
	for index, line := range lines {
		switch {
		case strings.Contains(line, "策略组 · Netflix"):
			titleIndex = index
		case strings.Contains(line, "> Netflix"):
			selectorIndex = index
		case strings.Contains(line, "在线") && strings.Contains(line, "测速") && strings.Contains(line, "节点"):
			headerIndex = index
		}
	}
	if titleIndex < 0 || selectorIndex != titleIndex+1 || headerIndex != selectorIndex+1 {
		t.Fatalf("group selector is not directly above the node table: title=%d selector=%d header=%d\n%s",
			titleIndex, selectorIndex, headerIndex, strings.Join(lines, "\n"))
	}
}

func TestProxyGroupSelectorKeepsCurrentGroupVisible(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	for _, size := range [][2]int{{60, 16}, {80, 24}, {120, 30}, {180, 40}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			m := testModel()
			m.width, m.height, m.page = size[0], size[1], pageProxies
			m.groups = make([]domain.ProxyGroup, 30)
			for index := range m.groups {
				m.groups[index] = domain.ProxyGroup{
					Name:    fmt.Sprintf("group-%02d", index),
					Proxies: []domain.Proxy{{Name: fmt.Sprintf("node-%02d", index)}},
				}
			}
			m.syncViewports()
			rowWidth := m.width - 4
			for index := range m.groups {
				selector := m.proxyGroupSelector(m.groups, m.groupCursor, rowWidth)
				if want := fmt.Sprintf("> group-%02d", index); !strings.Contains(ansi.Strip(selector), want) {
					t.Fatalf("current group %q is outside selector:\n%s", want, ansi.Strip(selector))
				}
				if width := ansi.StringWidth(selector); width != rowWidth {
					t.Fatalf("selector width = %d, want %d", width, rowWidth)
				}
				if width := ansi.StringWidthWc(selector); width > rowWidth {
					t.Fatalf("legacy selector width = %d, maximum %d", width, rowWidth)
				}
				m.moveGroup(1)
			}
			if m.groupCursor != 0 || m.groupOffset != 0 {
				t.Fatalf("group selector did not wrap to the beginning: cursor=%d offset=%d", m.groupCursor, m.groupOffset)
			}
		})
	}
}

func TestProxyGroupSelectorContainsEmojiWithoutWrapping(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := testModel()
	m.width, m.height, m.page = 60, 16, pageProxies
	m.groups = []domain.ProxyGroup{
		{Name: "✈️Final"},
		{Name: "🎯Direct"},
		{Name: "👨‍👩‍👧家庭节点"},
		{Name: strings.Repeat("👨‍👩‍👧", 8)},
		{Name: strings.Repeat("超长策略组", 8)},
	}
	for index := range m.groups {
		m.groupCursor = index
		m.syncViewports()
		selector := m.proxyGroupSelector(m.groups, index, m.width-4)
		if width := ansi.StringWidth(selector); width > m.width-4 {
			t.Fatalf("grapheme selector width = %d: %q", width, ansi.Strip(selector))
		}
		if width := ansi.StringWidthWc(selector); width > m.width-4 {
			t.Fatalf("legacy selector width = %d: %q", width, ansi.Strip(selector))
		}
		if !strings.Contains(ansi.Strip(selector), "> ") {
			t.Fatalf("selected emoji group has no visible marker: %q", ansi.Strip(selector))
		}
	}
}

func TestHelpExplainsPageScopedArrowKeys(t *testing.T) {
	for _, width := range []int{60, 120} {
		t.Run(fmt.Sprintf("width-%d", width), func(t *testing.T) {
			m := testModel()
			m.width = width
			help := m.helpText()
			normalized := strings.ReplaceAll(help, " ", "")
			for _, want := range []string{"Shift", "节点页←→/[]", "其他页←→"} {
				if !strings.Contains(normalized, want) {
					t.Fatalf("help at width %d does not contain %q:\n%s", width, want, help)
				}
			}
		})
	}
}

func TestProxyArrowKeysSwitchGroupsWithoutChangingPage(t *testing.T) {
	m := testModel()
	m.page = pageProxies
	m.groups = []domain.ProxyGroup{
		{Name: "first", Proxies: []domain.Proxy{{Name: "first-node"}}},
		{Name: "second", Proxies: []domain.Proxy{{Name: "second-node"}}},
	}

	m, _ = updateUIModel(t, m, keyPress(tea.KeyRight, ""))
	if m.page != pageProxies || m.groupCursor != 1 {
		t.Fatalf("right arrow changed page or wrong group: page=%d group=%d", m.page, m.groupCursor)
	}
	m, _ = updateUIModel(t, m, keyPress(tea.KeyLeft, ""))
	if m.page != pageProxies || m.groupCursor != 0 {
		t.Fatalf("left arrow changed page or wrong group: page=%d group=%d", m.page, m.groupCursor)
	}
	m, _ = updateUIModel(t, m, keyPress(tea.KeyTab, ""))
	if m.page != pageProfiles {
		t.Fatalf("Tab from proxy page = page %d, want profiles", m.page)
	}

	m.page = pageOverview
	m, _ = updateUIModel(t, m, keyPress(tea.KeyRight, ""))
	if m.page != pageProxies {
		t.Fatalf("right arrow outside proxy page = page %d, want proxies", m.page)
	}
}

func proxyTableSegments(line string, widths []int) []string {
	segments := make([]string, 0, len(widths))
	start := 2
	for _, width := range widths {
		segments = append(segments, ansi.Strip(ansi.Cut(line, start, start+width)))
		start += width + 1
	}
	return segments
}

func TestProxyTableColumnsStayAlignedAtResponsiveWidths(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	testCases := []struct {
		name      string
		state     proxyTestState
		delay     uint16
		coreDelay uint16
		want      string
		recorded  bool
	}{
		{name: "untested", want: "未测试"},
		{name: "core delay", coreDelay: 88, want: "核心 88ms"},
		{name: "success", state: proxyTestSuccess, delay: 45, want: "45 ms", recorded: true},
		{name: "timeout", state: proxyTestTimeout, want: "超时", recorded: true},
		{name: "failed", state: proxyTestFailed, want: "失败", recorded: true},
	}

	for _, rowWidth := range []int{56, 66, 86, 116} {
		for _, tt := range testCases {
			for _, selected := range []bool{false, true} {
				name := fmt.Sprintf("width-%d/%s/selected-%t", rowWidth, tt.name, selected)
				t.Run(name, func(t *testing.T) {
					nodeName := "香港-超长节点-" + strings.Repeat("区域", 24)
					proxy := domain.Proxy{Name: nodeName, Type: "Hysteria2", Alive: selected, Delay: tt.coreDelay}
					group := domain.ProxyGroup{Name: "PROXY", Now: nodeName, Proxies: []domain.Proxy{proxy}}
					m := testModel()
					m.groups = []domain.ProxyGroup{group}
					m.proxyTestStates = make(map[string]map[string]proxyTestResult)
					if tt.recorded {
						m.proxyTestStates[group.Name] = map[string]proxyTestResult{
							nodeName: {state: tt.state, delay: tt.delay},
						}
					}

					widths := proxyColumnWidths(rowWidth - 2)
					header := proxyTableHeader(rowWidth)
					row := m.proxyTableRow(group, proxy, selected, rowWidth)
					if got := ansi.StringWidth(header); got != rowWidth {
						t.Fatalf("header width = %d, want %d", got, rowWidth)
					}
					if got := ansi.StringWidth(row); got != rowWidth {
						t.Fatalf("row width = %d, want %d", got, rowWidth)
					}

					headerCells := proxyTableSegments(header, widths)
					rowCells := proxyTableSegments(row, widths)
					if got := strings.TrimSpace(headerCells[1]); got != "在线" {
						t.Fatalf("online header escaped its column: %q", headerCells[1])
					}
					if got := rowCells[1]; got != map[bool]string{false: " 否 ", true: " 是 "}[selected] {
						t.Fatalf("online value is not centered: %q", got)
					}
					if !strings.HasPrefix(headerCells[2], "测速") || !strings.HasPrefix(rowCells[2], tt.want) || strings.TrimSpace(rowCells[2]) != tt.want {
						t.Fatalf("test column is not left aligned: header=%q row=%q", headerCells[2], rowCells[2])
					}
					if !strings.HasPrefix(headerCells[3], "类型") || !strings.HasPrefix(rowCells[3], "Hysteria2") {
						t.Fatalf("type column is not left aligned: header=%q row=%q", headerCells[3], rowCells[3])
					}
					if !strings.HasPrefix(headerCells[4], "节点") || !strings.HasPrefix(rowCells[4], "香港") {
						t.Fatalf("node column is not left aligned: header=%q row=%q", headerCells[4], rowCells[4])
					}
					if rowCells[0] != "*" {
						t.Fatalf("active marker is not fixed-width ASCII: %q", rowCells[0])
					}
					if selected && !strings.HasPrefix(ansi.Strip(row), "> ") {
						t.Fatalf("selection marker is not fixed-width ASCII: %q", ansi.Strip(row))
					}
				})
			}
		}
	}
}

func TestWideProxyRowsAlignWithLegacyTerminalWidths(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	nodeNames := []string{
		"Proxies",
		"🎯Direct",
		"邮箱客服juzimao2333@gmail.com",
		"最新官网 ok.riolu.work",
		"支持AI:新日美台港",
		"🇭🇰 香港01",
		"🇭🇰 香港02",
		"🇭🇰 香港03",
		"🇭🇰 香港04",
		"🇯🇵 日本01",
		"🇯🇵 日本02",
		"🇯🇵 日本03",
		"🇯🇵 日本04",
		"🇸🇬 新加坡01",
		"🇸🇬 新加坡02",
	}
	proxies := make([]domain.Proxy, len(nodeNames))
	for index, name := range nodeNames {
		proxies[index] = domain.Proxy{Name: name, Type: "AnyTLS", Alive: true}
	}
	groupNames := []string{
		"AI", "Apple", "Bilibili", "Disney", "GLOBAL", "Game", "Google", "Microsoft",
		"Netflix", "Proxies", "Telegram", "TikTok", "YouTube", "✈️Final", "🎯Direct",
	}
	groups := make([]domain.ProxyGroup, len(groupNames))
	for index, name := range groupNames {
		groups[index] = domain.ProxyGroup{Name: name, Now: "Proxies"}
	}
	groups[0].Proxies = proxies

	m := testModel()
	m.width, m.height, m.page = 180, 32, pageProxies
	m.groups = groups
	m.syncViewports()
	lines := strings.Split(ansi.Strip(m.render()), "\n")
	wantGraphemeColumns := [4]int{-1, -1, -1, -1}
	wantLegacyColumns := [4]int{-1, -1, -1, -1}
	for _, nodeName := range nodeNames {
		found := false
		for _, line := range lines {
			if !strings.Contains(line, nodeName) || !strings.Contains(line, "未测试") {
				continue
			}
			found = true
			for tokenIndex, token := range []string{"是", "未测试", "AnyTLS", nodeName} {
				index := strings.Index(line, token)
				graphemeColumn := ansi.StringWidth(line[:index])
				legacyColumn := ansi.StringWidthWc(line[:index])
				if wantGraphemeColumns[tokenIndex] < 0 {
					wantGraphemeColumns[tokenIndex] = graphemeColumn
					wantLegacyColumns[tokenIndex] = legacyColumn
					continue
				}
				if graphemeColumn != wantGraphemeColumns[tokenIndex] || legacyColumn != wantLegacyColumns[tokenIndex] {
					t.Fatalf("%q field %q columns = grapheme %d / legacy %d, want %d / %d:\n%s",
						nodeName, token, graphemeColumn, legacyColumn,
						wantGraphemeColumns[tokenIndex], wantLegacyColumns[tokenIndex], line)
				}
			}
			break
		}
		if !found {
			t.Fatalf("rendered view does not contain proxy row %q", nodeName)
		}
	}
}

func TestSelectedNodeRowUsesFullWidthMonochromeHighlight(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	const width = 42
	selected := selectedNodeRow(true, "selected node", "52 ms  AnyTLS", width)
	unselected := selectedNodeRow(false, "other node", "85 ms  VMess", width)
	if got := ansi.StringWidth(selected); got != width {
		t.Fatalf("selected node row width = %d, want %d", got, width)
	}
	if got := ansi.StringWidth(unselected); got != width {
		t.Fatalf("unselected node row width = %d, want %d", got, width)
	}
	if !nodeHighlightStyle(width).GetReverse() {
		t.Fatal("selected node style has no monochrome reverse highlight")
	}
	if selected == ansi.Strip(selected) || unselected != ansi.Strip(unselected) {
		t.Fatalf("highlight ANSI mismatch: selected=%q unselected=%q", selected, unselected)
	}
	if stripped := ansi.Strip(selected); !strings.HasPrefix(stripped, "> ") || !strings.Contains(stripped, "selected node") {
		t.Fatalf("selected node marker or label missing: %q", stripped)
	}
}

func TestSelectedNodeStyleUsesHighContrastColorBackground(t *testing.T) {
	value, existed := os.LookupEnv("NO_COLOR")
	if err := os.Unsetenv("NO_COLOR"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv("NO_COLOR", value)
		} else {
			_ = os.Unsetenv("NO_COLOR")
		}
	})

	style := nodeHighlightStyle(42)
	if style.GetReverse() {
		t.Fatal("colored selected node style unexpectedly uses reverse video")
	}
	if style.GetBackground() == nil {
		t.Fatal("colored selected node style has no background highlight")
	}
	if got, want := style.GetBackground(), colors().accent; got != want {
		t.Fatalf("selected node background = %v, want accent %v", got, want)
	}
	if got, want := style.GetForeground(), colors().onAccent; got != want {
		t.Fatalf("selected node foreground = %v, want high-contrast text %v", got, want)
	}
	row := selectedWideNodeRow(true, " selected node        52 ms  AnyTLS", 42)
	if got := ansi.StringWidth(row); got != 42 {
		t.Fatalf("colored wide selected node row width = %d, want 42", got)
	}
	if stripped := ansi.Strip(row); !strings.HasPrefix(stripped, "> + selected node") {
		t.Fatalf("colored wide selected node markers missing: %q", stripped)
	}
	if stripped := ansi.Strip(selectedWideNodeRow(false, " offline node", 42)); !strings.HasPrefix(stripped, "> - offline node") {
		t.Fatalf("offline wide selected node marker missing: %q", stripped)
	}
}

func TestProxyViewportKeepsSelectionVisible(t *testing.T) {
	for _, size := range [][2]int{{60, 16}, {100, 30}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			m := testModel()
			m.width, m.height, m.page = size[0], size[1], pageProxies
			m.groups[0].Name = strings.Repeat("long-group-", 10)
			m.groups[0].Now = strings.Repeat("selected-node-", 8)
			m.groups[0].Proxies = numberedProxies(40)
			m.syncViewports()

			for i := 0; i < 40; i++ {
				requireSelectedVisible(t, m, fmt.Sprintf("node-%02d", i))
				m.moveCursor(1)
			}
			if m.proxyCursor != 39 || m.proxyOffset == 0 {
				t.Fatalf("bottom boundary did not advance viewport: cursor=%d offset=%d", m.proxyCursor, m.proxyOffset)
			}

			for i := 39; i >= 0; i-- {
				requireSelectedVisible(t, m, fmt.Sprintf("node-%02d", i))
				m.moveCursor(-1)
			}
			if m.proxyCursor != 0 || m.proxyOffset != 0 {
				t.Fatalf("top boundary did not reset viewport: cursor=%d offset=%d", m.proxyCursor, m.proxyOffset)
			}
		})
	}
}

func TestProfileAndConnectionViewportsStayStable(t *testing.T) {
	for _, size := range [][2]int{{60, 16}, {100, 30}} {
		t.Run(fmt.Sprintf("profiles_%dx%d", size[0], size[1]), func(t *testing.T) {
			m := testModel()
			m.width, m.height, m.page = size[0], size[1], pageProfiles
			m.profiles = make([]domain.Profile, 40)
			for i := range m.profiles {
				m.profiles[i] = domain.Profile{Name: fmt.Sprintf("profile-%02d", i), Kind: domain.ProfileRemote}
			}
			m.moveCursor(1000)
			requireSelectedVisible(t, m, "profile-39")
			if m.profileOffset == 0 {
				t.Fatal("profile viewport did not advance")
			}
			offset := m.profileOffset
			m.moveCursor(-1)
			requireSelectedVisible(t, m, "profile-38")
			if m.profileOffset != offset {
				t.Fatalf("profile viewport moved while selection remained visible: %d -> %d", offset, m.profileOffset)
			}
			m.moveCursor(-1000)
			requireSelectedVisible(t, m, "profile-00")
			if m.profileOffset != 0 {
				t.Fatalf("profile viewport did not return to top: %d", m.profileOffset)
			}
		})

		t.Run(fmt.Sprintf("connections_%dx%d", size[0], size[1]), func(t *testing.T) {
			m := testModel()
			m.width, m.height, m.page = size[0], size[1], pageConnections
			m.connections = make([]domain.Connection, 40)
			for i := range m.connections {
				m.connections[i] = domain.Connection{ID: fmt.Sprintf("id-%02d", i), Host: fmt.Sprintf("host-%02d.example", i)}
			}
			m.moveCursor(1000)
			requireSelectedVisible(t, m, "host-39.example")
			if m.connectionOffset == 0 {
				t.Fatal("connection viewport did not advance")
			}
			offset := m.connectionOffset
			m.moveCursor(-1)
			requireSelectedVisible(t, m, "host-38.example")
			if m.connectionOffset != offset {
				t.Fatalf("connection viewport moved while selection remained visible: %d -> %d", offset, m.connectionOffset)
			}
			m.moveCursor(-1000)
			requireSelectedVisible(t, m, "host-00.example")
			if m.connectionOffset != 0 {
				t.Fatalf("connection viewport did not return to top: %d", m.connectionOffset)
			}
		})
	}
}

func TestGroupViewportWrapsAndKeepsSelectionVisible(t *testing.T) {
	for _, size := range [][2]int{{60, 16}, {100, 30}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			m := testModel()
			m.width, m.height, m.page = size[0], size[1], pageProxies
			m.groups = make([]domain.ProxyGroup, 30)
			for i := range m.groups {
				m.groups[i] = domain.ProxyGroup{
					Name:    fmt.Sprintf("group-%02d", i),
					Now:     fmt.Sprintf("group-node-%02d", i),
					Proxies: []domain.Proxy{{Name: fmt.Sprintf("group-node-%02d", i), Type: "Direct", Alive: true}},
				}
			}
			for i := 0; i < 29; i++ {
				m.moveGroup(1)
			}
			requireSelectedVisible(t, m, "group-node-29")
			if m.groupCursor != 29 || m.groupOffset == 0 {
				t.Fatalf("group viewport did not reach the last group: cursor=%d offset=%d", m.groupCursor, m.groupOffset)
			}
			m.moveGroup(1)
			requireSelectedVisible(t, m, "group-node-00")
			if m.groupCursor != 0 || m.groupOffset != 0 || m.proxyCursor != 0 || m.proxyOffset != 0 {
				t.Fatalf("group wrap did not reset cursors and viewports: group=%d/%d proxy=%d/%d", m.groupCursor, m.groupOffset, m.proxyCursor, m.proxyOffset)
			}
		})
	}
}

func TestFilteringClampsProxyAndConnectionViewports(t *testing.T) {
	for _, size := range [][2]int{{60, 16}, {100, 30}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			m := testModel()
			m.width, m.height, m.page = size[0], size[1], pageProxies
			m.groups[0].Proxies = numberedProxies(40)
			for _, index := range []int{10, 20, 30} {
				m.groups[0].Proxies[index].Name = fmt.Sprintf("keep-%02d", index)
			}
			m.proxyCursor = 20
			m.syncViewports()
			m.applyFilter("keep")
			requireSelectedVisible(t, m, "keep-20")
			if m.proxyCursor != 1 || m.proxyOffset != 0 {
				t.Fatalf("proxy filter did not clamp viewport: cursor=%d offset=%d", m.proxyCursor, m.proxyOffset)
			}

			m.page = pageConnections
			m.filter = ""
			m.connections = make([]domain.Connection, 40)
			for i := range m.connections {
				host := fmt.Sprintf("host-%02d.example", i)
				if i == 10 || i == 20 || i == 30 {
					host = fmt.Sprintf("keep-%02d.example", i)
				}
				m.connections[i] = domain.Connection{ID: fmt.Sprintf("id-%02d", i), Host: host}
			}
			m.connectionCursor = 20
			m.syncViewports()
			m.applyFilter("keep")
			requireSelectedVisible(t, m, "keep-20.example")
			if m.connectionCursor != 1 || m.connectionOffset != 0 {
				t.Fatalf("connection filter did not clamp viewport: cursor=%d offset=%d", m.connectionCursor, m.connectionOffset)
			}
		})
	}
}

func TestGroupNameFilterShowsMembersAndPreservesSelection(t *testing.T) {
	for _, size := range [][2]int{{60, 16}, {100, 30}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			m := testModel()
			m.width, m.height, m.page = size[0], size[1], pageProxies
			m.groups = append(m.groups, domain.ProxyGroup{Name: "special-group", Proxies: numberedProxies(30)})
			m.groupCursor, m.proxyCursor = 1, 20
			m.syncViewports()

			m.applyFilter("special-group")
			if m.groupCursor != 0 || m.proxyCursor != 20 || len(filteredProxies(m.filteredGroups()[0], m.filter)) != 30 {
				t.Fatalf("group-name filter lost selection: group=%d proxy=%d", m.groupCursor, m.proxyCursor)
			}
			requireSelectedVisible(t, m, "node-20")

			m.applyFilter("")
			if m.groupCursor != 1 || m.proxyCursor != 20 {
				t.Fatalf("clearing group filter lost selection: group=%d proxy=%d", m.groupCursor, m.proxyCursor)
			}
			requireSelectedVisible(t, m, "node-20")
		})
	}
}

func TestSettingsSelectionVisibleAtBoundaries(t *testing.T) {
	m := testModel()
	m.width, m.height, m.page = 60, 16, pageSettings
	m.moveCursor(1000)
	requireSelectedVisible(t, m, "日志级别")
	m.moveCursor(-1000)
	requireSelectedVisible(t, m, "Mihomo 服务")
}

func TestGroupTestAppliesReturnedDelays(t *testing.T) {
	m := testModel()
	m.width, m.height, m.page = 60, 16, pageProxies
	testedGroup := domain.ProxyGroup{
		Name: "SECOND",
		All:  []string{"target-key", "zero-key", "omitted-key"},
		Proxies: []domain.Proxy{
			{Name: "target-node", Type: "VLESS", Alive: false},
			{Name: "zero-node", Type: "VLESS", Alive: false, Delay: 44},
			{Name: "omitted-node", Type: "VLESS", Alive: true, Delay: 88},
		},
	}
	m.groups = append(m.groups, testedGroup)
	m.filter = "target-node"
	m.clampCursors()
	backend := m.backend.(*fakeBackend)
	backend.delays = map[string]uint16{"target-key": 123, "zero-key": 0}

	updated, cmd := m.Update(tea.KeyPressMsg{Code: 't', Text: "t"})
	if cmd == nil {
		t.Fatal("group test did not start")
	}
	m = updated.(Model)
	updated, _ = m.Update(cmd())
	m = updated.(Model)
	if backend.tested != "SECOND" {
		t.Fatalf("tested wrong filtered group: %q", backend.tested)
	}
	proxies := m.groups[1].Proxies
	if proxies[0].Delay != 0 || proxies[0].Alive || proxies[1].Delay != 44 || proxies[1].Alive || proxies[2].Delay != 88 || !proxies[2].Alive {
		t.Fatalf("delay test overwrote core health: %#v", proxies)
	}
	state, delay := m.proxyDisplayState("SECOND", m.groups[1], 0)
	if state != proxyTestSuccess || delay != 123 {
		t.Fatalf("positive delay state = %v/%d, want success/123", state, delay)
	}
	state, delay = m.proxyDisplayState("SECOND", m.groups[1], 1)
	if state != proxyTestTimeout || delay != 0 {
		t.Fatalf("zero delay state = %v/%d, want timeout/0", state, delay)
	}
	state, delay = m.proxyDisplayState("SECOND", m.groups[1], 2)
	if state != proxyTestTimeout || delay != 0 {
		t.Fatalf("missing delay state = %v/%d, want timeout/0", state, delay)
	}
	if m.toast != "测速：成功1（叶1/组0/内置0）超时2" || !m.toastWarning {
		t.Fatalf("partial group test notice = %q, warning=%v", m.toast, m.toastWarning)
	}
	if view := ansi.Strip(m.render()); !strings.Contains(view, "123 ms") {
		t.Fatalf("returned delay is not visible:\n%s", view)
	}

	refreshed := testedGroup
	refreshed.Proxies = []domain.Proxy{
		{Name: "target-node", Type: "VLESS", Alive: false},
		{Name: "zero-node", Type: "VLESS", Alive: false, Delay: 55},
		{Name: "omitted-node", Type: "VLESS", Alive: true, Delay: 99},
	}
	updated, _ = m.Update(groupsMsg{groups: []domain.ProxyGroup{refreshed}})
	m = updated.(Model)
	proxies = m.groups[0].Proxies
	if proxies[0].Delay != 0 || proxies[0].Alive || proxies[1].Delay != 55 || proxies[1].Alive || proxies[2].Delay != 99 || !proxies[2].Alive {
		t.Fatalf("group refresh did not preserve core health: %#v", proxies)
	}
	if state, delay := m.proxyDisplayState("SECOND", m.groups[0], 0); state != proxyTestSuccess || delay != 123 {
		t.Fatalf("group refresh lost test result: %v/%d", state, delay)
	}

	coreState := refreshed
	coreState.Proxies = []domain.Proxy{
		{Name: "target-node", Type: "VLESS", Alive: false, Delay: 7},
		{Name: "zero-node", Type: "VLESS", Alive: false, Delay: 8},
		{Name: "omitted-node", Type: "VLESS", Alive: true, Delay: 99},
	}
	updated, _ = m.Update(groupsMsg{groups: []domain.ProxyGroup{coreState}})
	m = updated.(Model)
	proxies = m.groups[0].Proxies
	if proxies[0].Delay != 7 || proxies[0].Alive || proxies[1].Delay != 8 || proxies[1].Alive || proxies[2].Delay != 99 || !proxies[2].Alive {
		t.Fatalf("group refresh did not trust core state: %#v", proxies)
	}
	if state, delay := m.proxyDisplayState("SECOND", m.groups[0], 1); state != proxyTestTimeout || delay != 0 {
		t.Fatalf("group refresh lost timeout result: %v/%d", state, delay)
	}
}

func TestGroupTestErrorPreservesExistingDelayState(t *testing.T) {
	m := testModel()
	m.page = pageProxies
	m.groups[0].Proxies[0].Delay = 77
	m.groups[0].Proxies[0].Alive = false
	backend := m.backend.(*fakeBackend)
	backend.delays = map[string]uint16{"香港 01": 123}
	backend.groupErr = errors.New("测速失败")

	updated, cmd := m.Update(tea.KeyPressMsg{Code: 't', Text: "t"})
	m = updated.(Model)
	updated, _ = m.Update(cmd())
	m = updated.(Model)
	proxy := m.groups[0].Proxies[0]
	if proxy.Delay != 77 || proxy.Alive {
		t.Fatalf("failed group test mutated the node: %#v", proxy)
	}
	if m.loading || m.err != "测速失败" {
		t.Fatalf("failed group test state = loading %v, error %q", m.loading, m.err)
	}
}

func TestGroupTestWithoutUsableDelayIsExplicit(t *testing.T) {
	m := testModel()
	m.page = pageProxies
	original := m.groups[0].Proxies[0]
	backend := m.backend.(*fakeBackend)
	backend.delays = map[string]uint16{}

	updated, cmd := m.Update(tea.KeyPressMsg{Code: 't', Text: "t"})
	m = updated.(Model)
	updated, refresh := m.Update(cmd())
	m = updated.(Model)
	if m.toast != "测速：成功0（叶0/组0/内置0）超时1" || !m.toastWarning {
		t.Fatalf("empty group test result toast = %q", m.toast)
	}
	if got := m.groups[0].Proxies[0]; got.Name != original.Name || got.Alive != original.Alive || got.Delay != original.Delay {
		t.Fatalf("empty success map overwrote existing health: %#v", got)
	}
	if refresh == nil {
		t.Fatal("empty success map did not request an authoritative core refresh")
	}
}

func TestNestedGroupDelayUsesDirectMemberKeys(t *testing.T) {
	m := testModel()
	m.groups = []domain.ProxyGroup{{
		Name: "OUTER",
		All:  []string{"INNER", "DIRECT"},
		Proxies: []domain.Proxy{
			{Name: "INNER", Type: "Selector", Alive: false},
			{Name: "DIRECT", Type: "Direct", Alive: false},
		},
	}}

	updated, _ := m.Update(groupTestMsg{
		group:       "OUTER",
		delays:      map[string]uint16{"INNER": 31, "DIRECT": 0},
		completedAt: time.Unix(100, 0),
	})
	m = updated.(Model)
	if got := m.groups[0].Proxies; got[0].Alive || got[0].Delay != 0 || got[1].Alive || got[1].Delay != 0 {
		t.Fatalf("delay test overwrote nested member health: %#v", got)
	}
	if state, delay := m.proxyDisplayState("OUTER", m.groups[0], 0); state != proxyTestSuccess || delay != 31 {
		t.Fatalf("nested group result = %v/%d, want success/31", state, delay)
	}
	if state, delay := m.proxyDisplayState("OUTER", m.groups[0], 1); state != proxyTestTimeout || delay != 0 {
		t.Fatalf("built-in zero result = %v/%d, want timeout/0", state, delay)
	}
	if m.toast != "测速：成功1（叶0/组1/内置0）超时1" || !m.toastWarning {
		t.Fatalf("nested group test notice = %q, warning=%v", m.toast, m.toastWarning)
	}
}

func TestUnmatchedLeafDelayDoesNotOverwriteDirectMember(t *testing.T) {
	m := testModel()
	m.groups = []domain.ProxyGroup{{
		Name:    "OUTER",
		All:     []string{"INNER"},
		Proxies: []domain.Proxy{{Name: "INNER", Type: "Selector", Alive: true, Delay: 52}},
	}}

	updated, _ := m.Update(groupTestMsg{
		group:       "OUTER",
		delays:      map[string]uint16{"LEAF": 18},
		completedAt: time.Unix(100, 0),
	})
	m = updated.(Model)
	if got := m.groups[0].Proxies[0]; !got.Alive || got.Delay != 52 {
		t.Fatalf("unmatched leaf key overwrote direct member health: %#v", got)
	}
	if state, delay := m.proxyDisplayState("OUTER", m.groups[0], 0); state != proxyTestTimeout || delay != 0 {
		t.Fatalf("unmatched result did not become an explicit timeout: %v/%d", state, delay)
	}
	if m.toast != "测速：成功0（叶0/组0/内置0）超时1" || !m.toastWarning {
		t.Fatalf("unmatched group test notice = %q, warning=%v", m.toast, m.toastWarning)
	}
}

func TestGroupTestNoticeAnonymouslySeparatesMemberKinds(t *testing.T) {
	m := testModel()
	m.width = 60
	m.groups = []domain.ProxyGroup{{
		Name: "OUTER",
		All:  []string{"LEAF", "SELECTOR", "AUTO", "BUILTIN"},
		Proxies: []domain.Proxy{
			{Name: "LEAF", Type: "AnyTLS"},
			{Name: "SELECTOR", Type: "Selector"},
			{Name: "AUTO", Type: "URLTest"},
			{Name: "BUILTIN", Type: "Direct"},
		},
	}}
	delays := map[string]uint16{"LEAF": 11, "SELECTOR": 12, "AUTO": 13, "BUILTIN": 0}

	updated, _ := m.Update(groupTestMsg{
		group:       "OUTER",
		delays:      delays,
		completedAt: time.Unix(100, 0),
	})
	m = updated.(Model)
	if m.toast != "测速：成功3（叶1/组2/内置0）超时1" || !m.toastWarning {
		t.Fatalf("anonymous group test notice = %q, warning=%v", m.toast, m.toastWarning)
	}
	if footer := ansi.Strip(m.renderFooter()); !strings.Contains(footer, m.toast) {
		t.Fatalf("60-column footer hid anonymous group test notice: %q", footer)
	}
	for member := range delays {
		if strings.Contains(m.toast, member) {
			t.Fatalf("group test notice exposed member identity %q", member)
		}
	}
}

func TestGroupTestStateSurvivesCoreSnapshotsWithoutOverwritingHealth(t *testing.T) {
	testedAt := time.Unix(200, 0)
	m := testModel()
	m.groups = []domain.ProxyGroup{{
		Name:    "OUTER",
		All:     []string{"NODE"},
		Proxies: []domain.Proxy{{Name: "NODE", Alive: false}},
	}}

	updated, _ := m.Update(groupTestMsg{
		group:       "OUTER",
		delays:      map[string]uint16{"NODE": 24},
		completedAt: testedAt,
	})
	m = updated.(Model)
	coreSnapshot := func() []domain.ProxyGroup {
		return []domain.ProxyGroup{{
			Name:    "OUTER",
			All:     []string{"NODE"},
			Proxies: []domain.Proxy{{Name: "NODE", Alive: false, Delay: 0}},
		}}
	}
	for _, requestedAt := range []time.Time{testedAt.Add(-2 * time.Second), testedAt.Add(-time.Second)} {
		updated, _ = m.Update(groupsMsg{groups: coreSnapshot(), requestedAt: requestedAt})
		m = updated.(Model)
		if got := m.groups[0].Proxies[0]; got.Alive || got.Delay != 0 {
			t.Fatalf("test result overwrote core snapshot: %#v", got)
		}
		if state, delay := m.proxyDisplayState("OUTER", m.groups[0], 0); state != proxyTestSuccess || delay != 24 {
			t.Fatalf("snapshot lost test result: %v/%d", state, delay)
		}
	}

	updated, _ = m.Update(groupsMsg{groups: coreSnapshot(), requestedAt: testedAt.Add(time.Second)})
	m = updated.(Model)
	if got := m.groups[0].Proxies[0]; got.Alive || got.Delay != 0 {
		t.Fatalf("fresh snapshot was overwritten by test state: %#v", got)
	}

	updated, _ = m.Update(groupsMsg{groups: coreSnapshot(), requestedAt: testedAt.Add(2 * time.Second)})
	m = updated.(Model)
	if got := m.groups[0].Proxies[0]; got.Alive || got.Delay != 0 {
		t.Fatalf("second fresh snapshot was overwritten by test state: %#v", got)
	}
	if state, delay := m.proxyDisplayState("OUTER", m.groups[0], 0); state != proxyTestSuccess || delay != 24 {
		t.Fatalf("fresh snapshots lost test result: %v/%d", state, delay)
	}
}

func TestGroupNavigationWraps(t *testing.T) {
	m := testModel()
	m.groups = append(m.groups, domain.ProxyGroup{Name: "备用", Proxies: []domain.Proxy{{Name: "东京 01"}}})
	m.moveGroup(-1)
	if m.groupCursor != 1 || m.proxyCursor != 0 {
		t.Fatalf("unexpected wrapped cursors: group=%d proxy=%d", m.groupCursor, m.proxyCursor)
	}
}

func TestScheduleSettingTogglesKnownState(t *testing.T) {
	m := testModel()
	m.page, m.settingCursor = pageSettings, settingCursorFor(t, settingSchedule)
	m.scheduleOK = true
	m.schedule.Enabled = true
	backend := m.backend.(*fakeBackend)
	backend.action = ""
	_, cmd := m.activateSetting()
	if cmd == nil {
		t.Fatal("expected schedule operation")
	}
	_ = cmd()
	if backend.schedule == nil || *backend.schedule {
		t.Fatalf("enabled schedule was not toggled off: %#v", backend.schedule)
	}
}
