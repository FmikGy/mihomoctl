package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"mihomoctl/internal/domain"
)

type page int

const (
	pageOverview page = iota
	pageProxies
	pageProfiles
	pageConnections
	pageLogs
	pageSettings
)

var pageNames = []string{"总览", "节点", "配置", "连接", "日志", "设置"}

type inputMode int

const (
	inputNone inputMode = iota
	inputProfile
	inputFilter
)

const profileInputMask = '*'

type confirmMode int

const (
	confirmNone confirmMode = iota
	confirmRemoveProfile
	confirmCloseConnection
	confirmCloseAll
	confirmStopService
)

type Model struct {
	ctx     context.Context
	cancel  context.CancelFunc
	backend Backend

	width  int
	height int
	page   page

	status             domain.RuntimeStatus
	schedule           domain.ScheduleStatus
	scheduleOK         bool
	groups             []domain.ProxyGroup
	profiles           []domain.Profile
	connections        []domain.Connection
	logs               []domain.LogEntry
	pendingGroupDelays map[string]map[string]uint16
	groupSnapshotFloor time.Time
	logLevel           string
	logPaused          bool
	logCh              <-chan domain.LogEntry
	logErrCh           <-chan error

	groupCursor      int
	proxyCursor      int
	profileCursor    int
	connectionCursor int
	settingCursor    int
	groupOffset      int
	proxyOffset      int
	profileOffset    int
	connectionOffset int
	settingOffset    int

	loading      bool
	err          string
	toast        string
	toastWarning bool
	help         bool
	filter       string
	input        textinput.Model
	inMode       inputMode
	confirm      confirmMode
}

type tickMsg time.Time
type statusMsg struct {
	status domain.RuntimeStatus
	err    error
}
type scheduleMsg struct {
	status domain.ScheduleStatus
	err    error
}
type groupsMsg struct {
	groups      []domain.ProxyGroup
	err         error
	requestedAt time.Time
}
type profilesMsg struct {
	profiles []domain.Profile
	err      error
}
type connectionsMsg struct {
	connections []domain.Connection
	err         error
}
type operationMsg struct {
	message string
	err     error
}
type groupTestMsg struct {
	group       string
	delays      map[string]uint16
	err         error
	completedAt time.Time
}
type logMsg struct {
	entry domain.LogEntry
	ok    bool
}
type logErrMsg struct{ err error }

func New(ctx context.Context, backend Backend) Model {
	child, cancel := context.WithCancel(ctx)
	input := textinput.New()
	input.CharLimit = 4096
	input.SetWidth(64)
	return Model{
		ctx:      child,
		cancel:   cancel,
		backend:  backend,
		logLevel: "info",
		input:    input,
	}
}

func Run(ctx context.Context, backend Backend) error {
	if ctx == nil {
		ctx = context.Background()
	}
	model := New(ctx, backend)
	_, err := tea.NewProgram(model, tea.WithContext(ctx)).Run()
	return err
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.refreshStatus(), m.refreshPage(), tick())
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) refreshStatus() tea.Cmd {
	return func() tea.Msg {
		status, err := m.backend.Status(m.ctx)
		return statusMsg{status: status, err: err}
	}
}

func (m Model) refreshPage() tea.Cmd {
	switch m.page {
	case pageProxies:
		return m.refreshGroups()
	case pageProfiles:
		return func() tea.Msg {
			profiles, err := m.backend.Profiles(m.ctx)
			return profilesMsg{profiles: profiles, err: err}
		}
	case pageConnections:
		return func() tea.Msg {
			connections, err := m.backend.Connections(m.ctx)
			return connectionsMsg{connections: connections, err: err}
		}
	case pageLogs:
		if m.logCh == nil {
			return m.startLogs()
		}
	case pageSettings:
		return m.refreshSchedule()
	}
	return nil
}

func (m Model) refreshGroups() tea.Cmd {
	requestedAt := time.Now()
	return func() tea.Msg {
		groups, err := m.backend.Groups(m.ctx)
		return groupsMsg{groups: groups, err: err, requestedAt: requestedAt}
	}
}

type scheduleBackend interface {
	ScheduleStatus(context.Context) (domain.ScheduleStatus, error)
}

func (m Model) refreshSchedule() tea.Cmd {
	backend, ok := m.backend.(scheduleBackend)
	if !ok {
		return nil
	}
	return func() tea.Msg {
		status, err := backend.ScheduleStatus(m.ctx)
		return scheduleMsg{status: status, err: err}
	}
}

func (m Model) startLogs() tea.Cmd {
	return func() tea.Msg {
		entries, errs := m.backend.WatchLogs(m.ctx, m.logLevel)
		return logChannelsMsg{entries: entries, errs: errs}
	}
}

type logChannelsMsg struct {
	entries <-chan domain.LogEntry
	errs    <-chan error
}

func waitLog(entries <-chan domain.LogEntry) tea.Cmd {
	return func() tea.Msg {
		entry, ok := <-entries
		return logMsg{entry: entry, ok: ok}
	}
}

func waitLogError(errs <-chan error) tea.Cmd {
	return func() tea.Msg {
		err, ok := <-errs
		if !ok {
			return logErrMsg{}
		}
		return logErrMsg{err: err}
	}
}

func (m Model) operation(message string, fn func(context.Context) error) tea.Cmd {
	return func() tea.Msg {
		err := fn(m.ctx)
		return operationMsg{message: message, err: err}
	}
}

func (m Model) testGroup(group string) tea.Cmd {
	return func() tea.Msg {
		delays, err := m.backend.TestGroup(m.ctx, group)
		return groupTestMsg{group: group, delays: delays, err: err, completedAt: time.Now()}
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.inMode != inputNone {
		return m.updateInput(msg)
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.input.SetWidth(max(20, min(72, m.width-10)))
		m.syncViewports()
	case tea.KeyPressMsg:
		return m.updateKey(msg)
	case tickMsg:
		if m.toast != "" {
			m.toast = ""
			m.toastWarning = false
		}
		return m, tea.Batch(m.refreshStatus(), m.refreshPage(), tick())
	case statusMsg:
		if msg.err != nil {
			m.err = msg.err.Error()
		} else {
			m.status = msg.status
			m.err = ""
		}
	case scheduleMsg:
		if msg.err != nil {
			m.scheduleOK = false
			m.err = msg.err.Error()
		} else {
			m.schedule = msg.status
			m.scheduleOK = true
			m.err = ""
		}
	case groupsMsg:
		if !msg.requestedAt.IsZero() && msg.requestedAt.Before(m.groupSnapshotFloor) {
			return m, nil
		}
		m.loading = false
		if msg.err != nil {
			m.err = msg.err.Error()
		} else {
			m.groups = msg.groups
			m.applyGroupDelayOverlays()
			m.clampCursors()
		}
	case profilesMsg:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err.Error()
		} else {
			m.profiles = msg.profiles
			m.clampCursors()
		}
	case connectionsMsg:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err.Error()
		} else {
			m.connections = msg.connections
			m.clampCursors()
		}
	case operationMsg:
		m.loading = false
		m.confirm = confirmNone
		if msg.err != nil {
			m.err = msg.err.Error()
		} else {
			m.err = ""
			m.toast = msg.message
			m.toastWarning = false
		}
		return m, tea.Batch(m.refreshStatus(), m.refreshPage())
	case groupTestMsg:
		m.loading = false
		m.toast = ""
		m.toastWarning = false
		completedAt := msg.completedAt
		if completedAt.IsZero() {
			completedAt = time.Now()
		}
		if completedAt.After(m.groupSnapshotFloor) {
			m.groupSnapshotFloor = completedAt
		}
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, m.refreshGroups()
		}

		m.err = ""
		stats := m.applyGroupDelays(msg.group, msg.delays)
		if stats.succeeded == 0 {
			if stats.total == 0 {
				m.toast = "测速完成，未获得可用延迟"
			} else {
				m.toast = stats.notice()
			}
			m.toastWarning = true
			return m, m.refreshGroups()
		}

		m.toast = stats.notice()
		m.toastWarning = stats.failed() > 0
		m.rememberGroupDelays(msg.group, msg.delays)
		return m, m.refreshGroups()
	case logChannelsMsg:
		m.logCh, m.logErrCh = msg.entries, msg.errs
		return m, tea.Batch(waitLog(m.logCh), waitLogError(m.logErrCh))
	case logMsg:
		if msg.ok {
			if !m.logPaused {
				m.logs = append(m.logs, msg.entry)
				if len(m.logs) > 1000 {
					m.logs = append([]domain.LogEntry(nil), m.logs[len(m.logs)-1000:]...)
				}
			}
			return m, waitLog(m.logCh)
		}
		m.logCh = nil
	case logErrMsg:
		if msg.err != nil && m.ctx.Err() == nil {
			m.err = msg.err.Error()
		}
		m.logErrCh = nil
	}
	return m, nil
}

func (m Model) updateKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if m.confirm != confirmNone {
		switch key {
		case "y", "enter":
			return m.runConfirmed()
		case "n", "esc", "q":
			m.confirm = confirmNone
		}
		return m, nil
	}
	if m.help {
		if key == "?" || key == "esc" || key == "q" {
			m.help = false
		}
		return m, nil
	}

	switch key {
	case "ctrl+c", "q":
		m.cancel()
		return m, tea.Quit
	case "?":
		m.help = true
		return m, nil
	case "tab", "right", "l":
		m.applyFilter("")
		m.page = (m.page + 1) % page(len(pageNames))
		m.loading = true
		return m, m.refreshPage()
	case "shift+tab", "left", "h":
		m.applyFilter("")
		m.page = (m.page + page(len(pageNames)) - 1) % page(len(pageNames))
		m.loading = true
		return m, m.refreshPage()
	case "up", "k":
		m.moveCursor(-1)
	case "down", "j":
		m.moveCursor(1)
	case "[":
		if m.page == pageProxies {
			m.moveGroup(-1)
		}
	case "]":
		if m.page == pageProxies {
			m.moveGroup(1)
		}
	case "r":
		m.loading = true
		return m, tea.Batch(m.refreshStatus(), m.refreshPage())
	case "/":
		if m.page == pageProxies || m.page == pageConnections || m.page == pageLogs {
			return m, m.focusInput(inputFilter, "筛选", m.filter)
		}
	case "enter":
		return m.activate()
	case "t":
		groups := m.filteredGroups()
		if m.page == pageProxies && len(groups) > 0 {
			group := groups[clamp(m.groupCursor, 0, len(groups)-1)].Name
			m.loading = true
			return m, m.testGroup(group)
		}
	case "a":
		if m.page == pageProfiles {
			return m, m.focusInput(inputProfile, "订阅 URL、本地路径或节点 URI", "")
		}
	case "u":
		if m.page == pageProfiles && len(m.profiles) > 0 {
			name := m.profiles[m.profileCursor].Name
			m.loading = true
			return m, m.operation("配置已更新", func(ctx context.Context) error {
				return m.backend.UpdateProfile(ctx, name)
			})
		}
	case "d":
		switch m.page {
		case pageProfiles:
			if len(m.profiles) > 0 && !m.profiles[m.profileCursor].Active {
				m.confirm = confirmRemoveProfile
			}
		case pageConnections:
			if len(m.filteredConnections()) > 0 {
				m.confirm = confirmCloseConnection
			}
		}
	case "x":
		if m.page == pageConnections && len(m.connections) > 0 {
			m.confirm = confirmCloseAll
		}
	case "space":
		if m.page == pageLogs {
			m.logPaused = !m.logPaused
		}
	}
	return m, nil
}

func (m Model) updateInput(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "esc":
			m.resetInput()
			return m, nil
		case "enter":
			value := strings.TrimSpace(m.input.Value())
			mode := m.inMode
			m.resetInput()
			if mode == inputFilter {
				m.applyFilter(value)
				return m, nil
			}
			if value == "" {
				return m, nil
			}
			m.loading = true
			return m, m.operation("配置已添加，请选中后按 Enter 激活", func(ctx context.Context) error {
				return m.backend.AddProfile(ctx, "", value, 24*time.Hour)
			})
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *Model) focusInput(mode inputMode, placeholder, value string) tea.Cmd {
	m.inMode = mode
	m.input.Placeholder = placeholder
	m.input.SetValue(value)
	m.input.EchoMode = textinput.EchoNormal
	m.input.EchoCharacter = profileInputMask
	if mode == inputProfile {
		m.input.EchoMode = textinput.EchoPassword
	}
	return m.input.Focus()
}

func (m *Model) resetInput() {
	m.input.Blur()
	m.input.SetValue("")
	m.input.Placeholder = ""
	m.input.EchoMode = textinput.EchoNormal
	m.input.EchoCharacter = profileInputMask
	m.inMode = inputNone
}

func (m Model) activate() (tea.Model, tea.Cmd) {
	switch m.page {
	case pageOverview:
		if m.status.Service.Active {
			m.confirm = confirmStopService
			return m, nil
		}
		m.loading = true
		return m, m.operation("Mihomo 已启动", func(ctx context.Context) error {
			return m.backend.Service(ctx, "start")
		})
	case pageProxies:
		groups := m.filteredGroups()
		if len(groups) == 0 {
			return m, nil
		}
		group := groups[min(m.groupCursor, len(groups)-1)]
		nodes := filteredProxies(group, m.filter)
		if len(nodes) == 0 {
			return m, nil
		}
		node := nodes[min(m.proxyCursor, len(nodes)-1)].Name
		m.loading = true
		return m, m.operation("已切换到 "+node, func(ctx context.Context) error {
			return m.backend.SelectProxy(ctx, group.Name, node)
		})
	case pageProfiles:
		if len(m.profiles) > 0 {
			name := m.profiles[m.profileCursor].Name
			if m.profiles[m.profileCursor].Active {
				return m, nil
			}
			m.loading = true
			return m, m.operation("已启用 "+name, func(ctx context.Context) error {
				return m.backend.UseProfile(ctx, name)
			})
		}
	case pageConnections:
		if len(m.filteredConnections()) > 0 {
			m.confirm = confirmCloseConnection
		}
	case pageSettings:
		return m.activateSetting()
	}
	return m, nil
}

func (m Model) activateSetting() (tea.Model, tea.Cmd) {
	m.loading = true
	switch m.settingCursor {
	case 0:
		action, message := "start", "Mihomo 已启动"
		if m.status.Service.Active {
			m.loading = false
			m.confirm = confirmStopService
			return m, nil
		}
		return m, m.operation(message, func(ctx context.Context) error { return m.backend.Service(ctx, action) })
	case 1:
		action := "enable"
		message := "开机启动已启用"
		if m.status.Service.Enabled {
			action, message = "disable", "开机启动已关闭"
		}
		return m, m.operation(message, func(ctx context.Context) error { return m.backend.Service(ctx, action) })
	case 2:
		next := domain.ModeRule
		if m.status.Mode == domain.ModeRule {
			next = domain.ModeGlobal
		} else if m.status.Mode == domain.ModeGlobal {
			next = domain.ModeDirect
		}
		return m, m.operation("运行模式已切换", func(ctx context.Context) error { return m.backend.SetMode(ctx, next) })
	case 3:
		return m, m.operation("TUN 设置已更新", func(ctx context.Context) error { return m.backend.SetTUN(ctx, !m.status.TUN) })
	case 4:
		enabled := true
		if m.scheduleOK {
			enabled = !m.schedule.Enabled
		}
		return m, m.operation("定时更新设置已更新", func(ctx context.Context) error { return m.backend.SetSchedule(ctx, enabled) })
	}
	return m, nil
}

func (m Model) runConfirmed() (tea.Model, tea.Cmd) {
	m.loading = true
	switch m.confirm {
	case confirmRemoveProfile:
		name := m.profiles[m.profileCursor].Name
		return m, m.operation("配置已删除", func(ctx context.Context) error { return m.backend.RemoveProfile(ctx, name) })
	case confirmCloseConnection:
		connections := m.filteredConnections()
		id := connections[min(m.connectionCursor, len(connections)-1)].ID
		return m, m.operation("连接已关闭", func(ctx context.Context) error { return m.backend.CloseConnection(ctx, id) })
	case confirmCloseAll:
		return m, m.operation("全部连接已关闭", m.backend.CloseAllConnections)
	case confirmStopService:
		return m, m.operation("Mihomo 已停止", func(ctx context.Context) error { return m.backend.Service(ctx, "stop") })
	}
	return m, nil
}

func (m *Model) moveCursor(delta int) {
	defer m.syncViewports()
	switch m.page {
	case pageProxies:
		groups := m.filteredGroups()
		if len(groups) == 0 {
			return
		}
		group := groups[clamp(m.groupCursor, 0, len(groups)-1)]
		m.proxyCursor = clamp(m.proxyCursor+delta, 0, max(0, len(filteredProxies(group, m.filter))-1))
	case pageProfiles:
		m.profileCursor = clamp(m.profileCursor+delta, 0, max(0, len(m.profiles)-1))
	case pageConnections:
		m.connectionCursor = clamp(m.connectionCursor+delta, 0, max(0, len(m.filteredConnections())-1))
	case pageSettings:
		m.settingCursor = clamp(m.settingCursor+delta, 0, 4)
	}
}

func (m *Model) moveGroup(delta int) {
	groups := m.filteredGroups()
	if len(groups) == 0 {
		return
	}
	m.groupCursor = (m.groupCursor + delta + len(groups)) % len(groups)
	m.proxyCursor = 0
	m.proxyOffset = 0
	m.syncViewports()
}

func (m *Model) clampCursors() {
	groups := m.filteredGroups()
	m.groupCursor = clamp(m.groupCursor, 0, max(0, len(groups)-1))
	if len(groups) > 0 {
		m.proxyCursor = clamp(m.proxyCursor, 0, max(0, len(filteredProxies(groups[m.groupCursor], m.filter))-1))
	} else {
		m.proxyCursor = 0
	}
	m.profileCursor = clamp(m.profileCursor, 0, max(0, len(m.profiles)-1))
	m.connectionCursor = clamp(m.connectionCursor, 0, max(0, len(m.filteredConnections())-1))
	m.settingCursor = clamp(m.settingCursor, 0, 4)
	m.syncViewports()
}

func (m *Model) applyFilter(value string) {
	selectedGroup, selectedProxy := "", ""
	selectedConnection := ""
	if m.page == pageProxies {
		groups := m.filteredGroups()
		if len(groups) > 0 {
			group := groups[clamp(m.groupCursor, 0, len(groups)-1)]
			selectedGroup = group.Name
			proxies := filteredProxies(group, m.filter)
			if len(proxies) > 0 {
				selectedProxy = proxies[clamp(m.proxyCursor, 0, len(proxies)-1)].Name
			}
		}
	} else if m.page == pageConnections {
		connections := m.filteredConnections()
		if len(connections) > 0 {
			selectedConnection = connections[clamp(m.connectionCursor, 0, len(connections)-1)].ID
		}
	}

	m.filter = value
	if m.page == pageProxies {
		m.groupCursor, m.proxyCursor = 0, 0
		groups := m.filteredGroups()
		for groupIndex := range groups {
			if groups[groupIndex].Name != selectedGroup {
				continue
			}
			m.groupCursor = groupIndex
			for proxyIndex, proxy := range filteredProxies(groups[groupIndex], m.filter) {
				if proxy.Name == selectedProxy {
					m.proxyCursor = proxyIndex
					break
				}
			}
			break
		}
	} else if m.page == pageConnections {
		m.connectionCursor = 0
		if selectedConnection != "" {
			for connectionIndex, connection := range m.filteredConnections() {
				if connection.ID == selectedConnection {
					m.connectionCursor = connectionIndex
					break
				}
			}
		}
	}
	m.clampCursors()
}

func (m Model) listCapacity() int {
	return max(1, max(8, m.height-6)-3)
}

func (m Model) settingsCapacity() int {
	return max(1, max(8, m.height-6)-5)
}

func (m *Model) syncViewports() {
	groups := m.filteredGroups()
	m.groupOffset = viewportOffset(len(groups), m.groupCursor, m.groupOffset, m.listCapacity())
	nodeCount := 0
	if len(groups) > 0 {
		groupIndex := clamp(m.groupCursor, 0, len(groups)-1)
		nodeCount = len(filteredProxies(groups[groupIndex], m.filter))
	}
	m.proxyOffset = viewportOffset(nodeCount, m.proxyCursor, m.proxyOffset, m.listCapacity())
	m.profileOffset = viewportOffset(len(m.profiles), m.profileCursor, m.profileOffset, m.listCapacity())
	m.connectionOffset = viewportOffset(len(m.filteredConnections()), m.connectionCursor, m.connectionOffset, m.listCapacity())
	m.settingOffset = viewportOffset(5, m.settingCursor, m.settingOffset, m.settingsCapacity())
}

func viewportOffset(total, cursor, offset, capacity int) int {
	if total <= 0 {
		return 0
	}
	capacity = max(1, capacity)
	cursor = clamp(cursor, 0, total-1)
	offset = clamp(offset, 0, max(0, total-capacity))
	if cursor < offset {
		offset = cursor
	} else if cursor >= offset+capacity {
		offset = cursor - capacity + 1
	}
	return clamp(offset, 0, max(0, total-capacity))
}

func viewportBounds(total, cursor, offset, capacity int) (int, int) {
	start := viewportOffset(total, cursor, offset, capacity)
	return start, min(total, start+max(1, capacity))
}

type groupDelayStats struct {
	total            int
	succeeded        int
	leafSucceeded    int
	groupSucceeded   int
	builtinSucceeded int
	unknownSucceeded int
}

func (s groupDelayStats) failed() int {
	return max(0, s.total-s.succeeded)
}

func (s groupDelayStats) notice() string {
	kinds := fmt.Sprintf("叶%d/组%d/内置%d", s.leafSucceeded, s.groupSucceeded, s.builtinSucceeded)
	if s.unknownSucceeded > 0 {
		kinds += fmt.Sprintf("/未知%d", s.unknownSucceeded)
	}
	return fmt.Sprintf("测速：成功%d（%s）失败%d", s.succeeded, kinds, s.failed())
}

func (m *Model) applyGroupDelays(group string, delays map[string]uint16) groupDelayStats {
	for groupIndex := range m.groups {
		if m.groups[groupIndex].Name != group {
			continue
		}
		stats := groupDelayStats{total: len(m.groups[groupIndex].Proxies)}
		for proxyIndex := range m.groups[groupIndex].Proxies {
			if _, ok := groupMemberDelay(m.groups[groupIndex], proxyIndex, delays); ok {
				stats.succeeded++
				switch proxyDelayKind(m.groups[groupIndex].Proxies[proxyIndex].Type) {
				case "leaf":
					stats.leafSucceeded++
				case "group":
					stats.groupSucceeded++
				case "builtin":
					stats.builtinSucceeded++
				default:
					stats.unknownSucceeded++
				}
			}
		}
		if stats.succeeded == 0 {
			return stats
		}
		for proxyIndex := range m.groups[groupIndex].Proxies {
			proxy := &m.groups[groupIndex].Proxies[proxyIndex]
			delay, ok := groupMemberDelay(m.groups[groupIndex], proxyIndex, delays)
			if ok {
				proxy.Delay = delay
				proxy.Alive = true
			} else {
				proxy.Delay = 0
				proxy.Alive = false
			}
		}
		return stats
	}
	return groupDelayStats{}
}

func proxyDelayKind(proxyType string) string {
	normalized := strings.ToLower(proxyType)
	normalized = strings.NewReplacer("-", "", "_", "", " ", "").Replace(normalized)
	switch normalized {
	case "selector", "urltest", "fallback", "loadbalance", "relay":
		return "group"
	case "direct", "reject", "rejectdrop", "pass", "compatible":
		return "builtin"
	case "":
		return "unknown"
	default:
		return "leaf"
	}
}

func groupMemberDelay(group domain.ProxyGroup, proxyIndex int, delays map[string]uint16) (uint16, bool) {
	proxy := group.Proxies[proxyIndex]
	member := proxy.Name
	if proxyIndex < len(group.All) && group.All[proxyIndex] != "" {
		member = group.All[proxyIndex]
	}
	delay, ok := delays[member]
	if !ok && member != proxy.Name {
		delay, ok = delays[proxy.Name]
	}
	return delay, ok
}

func (m *Model) rememberGroupDelays(group string, delays map[string]uint16) {
	if m.pendingGroupDelays == nil {
		m.pendingGroupDelays = make(map[string]map[string]uint16)
	}
	copyOfDelays := make(map[string]uint16, len(delays))
	for name, delay := range delays {
		copyOfDelays[name] = delay
	}
	m.pendingGroupDelays[group] = copyOfDelays
}

func (m *Model) applyGroupDelayOverlays() {
	for group, delays := range m.pendingGroupDelays {
		_ = m.applyGroupDelays(group, delays)
		delete(m.pendingGroupDelays, group)
	}
}

func (m Model) filteredGroups() []domain.ProxyGroup {
	if m.filter == "" {
		return m.groups
	}
	needle := strings.ToLower(m.filter)
	result := make([]domain.ProxyGroup, 0, len(m.groups))
	for _, group := range m.groups {
		if strings.Contains(strings.ToLower(group.Name), needle) || len(filteredProxies(group, m.filter)) > 0 {
			result = append(result, group)
		}
	}
	return result
}

func filteredProxies(group domain.ProxyGroup, filter string) []domain.Proxy {
	if filter == "" {
		return group.Proxies
	}
	needle := strings.ToLower(filter)
	if strings.Contains(strings.ToLower(group.Name), needle) {
		return group.Proxies
	}
	result := make([]domain.Proxy, 0, len(group.Proxies))
	for _, proxy := range group.Proxies {
		if strings.Contains(strings.ToLower(proxy.Name), needle) || strings.Contains(strings.ToLower(proxy.Type), needle) {
			result = append(result, proxy)
		}
	}
	return result
}

func (m Model) filteredConnections() []domain.Connection {
	if m.filter == "" {
		return m.connections
	}
	needle := strings.ToLower(m.filter)
	result := make([]domain.Connection, 0, len(m.connections))
	for _, connection := range m.connections {
		haystack := strings.ToLower(strings.Join([]string{connection.Host, connection.Process, connection.Destination, connection.Rule}, " "))
		if strings.Contains(haystack, needle) {
			result = append(result, connection)
		}
	}
	return result
}

func (m Model) filteredLogs() []domain.LogEntry {
	if m.filter == "" {
		return m.logs
	}
	needle := strings.ToLower(m.filter)
	result := make([]domain.LogEntry, 0, len(m.logs))
	for _, entry := range m.logs {
		haystack := strings.ToLower(entry.Time + " " + entry.Level + " " + entry.Message)
		if strings.Contains(haystack, needle) {
			result = append(result, entry)
		}
	}
	return result
}

func clamp(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func modeLabel(mode domain.Mode) string {
	switch mode {
	case domain.ModeRule:
		return "规则"
	case domain.ModeGlobal:
		return "全局"
	case domain.ModeDirect:
		return "直连"
	default:
		if mode == "" {
			return "未连接"
		}
		return string(mode)
	}
}
