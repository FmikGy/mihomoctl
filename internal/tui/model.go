package tui

import (
	"context"
	"fmt"
	"strconv"
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
	inputMixedPort
)

type settingID string

const (
	settingService   settingID = "service"
	settingStartup   settingID = "startup"
	settingMode      settingID = "mode"
	settingTUN       settingID = "tun"
	settingSchedule  settingID = "schedule"
	settingMixedPort settingID = "mixed-port"
	settingAllowLAN  settingID = "allow-lan"
	settingIPv6      settingID = "ipv6"
	settingLogLevel  settingID = "log-level"
)

var settingOrder = []settingID{
	settingService,
	settingStartup,
	settingMode,
	settingTUN,
	settingSchedule,
	settingMixedPort,
	settingAllowLAN,
	settingIPv6,
	settingLogLevel,
}

type settingPicker int

const (
	pickerNone settingPicker = iota
	pickerTUN
	pickerAllowLAN
	pickerIPv6
	pickerLogLevel
)

type pickerOption struct {
	label string
	value string
}

const (
	profileInputMask       = '*'
	trafficHistoryLimit    = 120
	statusRefreshEvery     = 5 * time.Second
	groupRefreshEvery      = 5 * time.Second
	connectionRefreshEvery = 2 * time.Second
	profileRefreshEvery    = 30 * time.Second
	settingsRefreshEvery   = 30 * time.Second
	toastLifetime          = 3 * time.Second
	streamRetryMaximum     = 30 * time.Second
)

type confirmMode int

const (
	confirmNone confirmMode = iota
	confirmRemoveProfile
	confirmCloseConnection
	confirmCloseAll
	confirmStopService
	confirmEnableLAN
)

type confirmTarget struct {
	label string
	id    string
	name  string
}

type proxyTestState int

const (
	proxyTestUnknown proxyTestState = iota
	proxyTestSuccess
	proxyTestTimeout
	proxyTestFailed
)

type proxyTestResult struct {
	state    proxyTestState
	delay    uint16
	testedAt time.Time
}

type requestState struct {
	inFlight   bool
	queued     bool
	generation uint64
	lastStart  time.Time
	cancel     context.CancelFunc
}

type errorSource string

const (
	errorStatus      errorSource = "status"
	errorGroups      errorSource = "groups"
	errorProfiles    errorSource = "profiles"
	errorConnections errorSource = "connections"
	errorSchedule    errorSource = "schedule"
	errorTraffic     errorSource = "traffic"
	errorLogs        errorSource = "logs"
	errorOperation   errorSource = "operation"
	errorGroupTest   errorSource = "group-test"
)

type sourceError struct {
	message  string
	sequence uint64
}

type Model struct {
	ctx     context.Context
	cancel  context.CancelFunc
	backend Backend

	width  int
	height int
	page   page

	status                  domain.RuntimeStatus
	trafficHistory          []trafficSample
	statusSnapshotFloor     time.Time
	lastTrafficAt           time.Time
	schedule                domain.ScheduleStatus
	scheduleOK              bool
	groups                  []domain.ProxyGroup
	profiles                []domain.Profile
	connections             []domain.Connection
	logs                    []domain.LogEntry
	proxyTestStates         map[string]map[string]proxyTestResult
	logLevel                string
	logPaused               bool
	logOffset               int
	logUnread               int
	logCh                   <-chan domain.LogEntry
	logErrCh                <-chan error
	logCancel               context.CancelFunc
	logConnecting           bool
	logGeneration           uint64
	logRetry                int
	logReconnectPending     bool
	trafficCh               <-chan domain.Traffic
	trafficErrCh            <-chan error
	trafficCancel           context.CancelFunc
	trafficConnecting       bool
	trafficGeneration       uint64
	trafficRetry            int
	trafficReconnectPending bool

	statusRequest     requestState
	groupRequest      requestState
	profileRequest    requestState
	connectionRequest requestState
	scheduleRequest   requestState

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

	// loading only tracks a user-triggered mutation or delay test. Background
	// refreshes remain non-blocking and never change it.
	loading            bool
	err                string
	errors             map[errorSource]sourceError
	errorSequence      uint64
	toast              string
	toastWarning       bool
	toastExpires       time.Time
	help               bool
	filter             string
	input              textinput.Model
	inMode             inputMode
	inputError         string
	picker             settingPicker
	pickerCursor       int
	confirm            confirmMode
	confirmTarget      confirmTarget
	mutationGeneration uint64
}

type tickMsg time.Time
type bootstrapMsg struct{}
type trafficSample struct {
	at   time.Time
	up   int64
	down int64
}
type statusMsg struct {
	status      domain.RuntimeStatus
	err         error
	requestedAt time.Time
	generation  uint64
}
type scheduleMsg struct {
	status     domain.ScheduleStatus
	err        error
	generation uint64
}
type groupsMsg struct {
	groups      []domain.ProxyGroup
	err         error
	requestedAt time.Time
	generation  uint64
}
type profilesMsg struct {
	profiles   []domain.Profile
	err        error
	generation uint64
}
type connectionsMsg struct {
	connections []domain.Connection
	err         error
	generation  uint64
}
type operationMsg struct {
	message     string
	err         error
	generation  uint64
	configKey   string
	configValue string
}
type groupTestMsg struct {
	group       string
	delays      map[string]uint16
	err         error
	completedAt time.Time
	generation  uint64
}
type logMsg struct {
	entry      domain.LogEntry
	ok         bool
	generation uint64
}
type logErrMsg struct {
	err        error
	ok         bool
	generation uint64
}
type logChannelsMsg struct {
	entries    <-chan domain.LogEntry
	errs       <-chan error
	generation uint64
}
type logReconnectMsg struct{ generation uint64 }
type trafficMsg struct {
	traffic    domain.Traffic
	at         time.Time
	ok         bool
	generation uint64
}
type trafficErrMsg struct {
	err        error
	ok         bool
	generation uint64
}
type trafficChannelsMsg struct {
	traffic    <-chan domain.Traffic
	errs       <-chan error
	generation uint64
}
type trafficReconnectMsg struct{ generation uint64 }

func New(ctx context.Context, backend Backend) Model {
	if ctx == nil {
		ctx = context.Background()
	}
	child, cancel := context.WithCancel(ctx)
	input := textinput.New()
	input.CharLimit = 4096
	input.SetWidth(64)
	return Model{
		ctx:             child,
		cancel:          cancel,
		backend:         backend,
		logLevel:        "info",
		input:           input,
		errors:          make(map[errorSource]sourceError),
		proxyTestStates: make(map[string]map[string]proxyTestResult),
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
	return tea.Batch(func() tea.Msg { return bootstrapMsg{} }, tick())
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

type scheduleBackend interface {
	ScheduleStatus(context.Context) (domain.ScheduleStatus, error)
}

func requestDue(state requestState, now time.Time, interval time.Duration) bool {
	return state.lastStart.IsZero() || !now.Before(state.lastStart.Add(interval))
}

func acceptResponse(state *requestState, generation uint64) bool {
	// A zero generation is accepted for compatibility with synthetic messages
	// used by in-package callers. Production requests always carry a generation.
	if generation == 0 {
		if state.cancel != nil {
			state.cancel()
		}
		state.cancel = nil
		state.inFlight = false
		return true
	}
	if !state.inFlight || state.generation != generation {
		return false
	}
	if state.cancel != nil {
		state.cancel()
	}
	state.cancel = nil
	state.inFlight = false
	return true
}

func queueOrStart(state *requestState, now time.Time, interval time.Duration, force bool) (uint64, bool) {
	if state.inFlight {
		if force {
			state.queued = true
		}
		return 0, false
	}
	if !force && !requestDue(*state, now, interval) {
		return 0, false
	}
	state.inFlight = true
	state.queued = false
	state.generation++
	state.lastStart = now
	return state.generation, true
}

func takeQueued(state *requestState) bool {
	queued := state.queued
	state.queued = false
	return queued
}

func cancelRequest(state *requestState) {
	if state.cancel != nil {
		state.cancel()
	}
	state.cancel = nil
	state.inFlight = false
	state.queued = false
	state.generation++
}

func (m *Model) beginStatusRefresh(now time.Time, force bool) tea.Cmd {
	generation, ok := queueOrStart(&m.statusRequest, now, statusRefreshEvery, force)
	if !ok {
		return nil
	}
	requestCtx, cancel := context.WithCancel(m.ctx)
	m.statusRequest.cancel = cancel
	requestedAt := now
	return func() tea.Msg {
		status, err := m.backend.Status(requestCtx)
		return statusMsg{status: status, err: err, requestedAt: requestedAt, generation: generation}
	}
}

func (m *Model) beginGroupsRefresh(now time.Time, force bool) tea.Cmd {
	generation, ok := queueOrStart(&m.groupRequest, now, groupRefreshEvery, force)
	if !ok {
		return nil
	}
	requestCtx, cancel := context.WithCancel(m.ctx)
	m.groupRequest.cancel = cancel
	requestedAt := now
	return func() tea.Msg {
		groups, err := m.backend.Groups(requestCtx)
		return groupsMsg{groups: groups, err: err, requestedAt: requestedAt, generation: generation}
	}
}

func (m *Model) beginProfilesRefresh(now time.Time, force bool) tea.Cmd {
	generation, ok := queueOrStart(&m.profileRequest, now, profileRefreshEvery, force)
	if !ok {
		return nil
	}
	requestCtx, cancel := context.WithCancel(m.ctx)
	m.profileRequest.cancel = cancel
	return func() tea.Msg {
		profiles, err := m.backend.Profiles(requestCtx)
		return profilesMsg{profiles: profiles, err: err, generation: generation}
	}
}

func (m *Model) beginConnectionsRefresh(now time.Time, force bool) tea.Cmd {
	generation, ok := queueOrStart(&m.connectionRequest, now, connectionRefreshEvery, force)
	if !ok {
		return nil
	}
	requestCtx, cancel := context.WithCancel(m.ctx)
	m.connectionRequest.cancel = cancel
	return func() tea.Msg {
		connections, err := m.backend.Connections(requestCtx)
		return connectionsMsg{connections: connections, err: err, generation: generation}
	}
}

func (m *Model) beginScheduleRefresh(now time.Time, force bool) tea.Cmd {
	backend, ok := m.backend.(scheduleBackend)
	if !ok {
		m.scheduleRequest.lastStart = now
		return nil
	}
	generation, ok := queueOrStart(&m.scheduleRequest, now, settingsRefreshEvery, force)
	if !ok {
		return nil
	}
	requestCtx, cancel := context.WithCancel(m.ctx)
	m.scheduleRequest.cancel = cancel
	return func() tea.Msg {
		status, err := backend.ScheduleStatus(requestCtx)
		return scheduleMsg{status: status, err: err, generation: generation}
	}
}

func (m *Model) beginPageRefresh(target page, now time.Time, force bool) tea.Cmd {
	switch target {
	case pageProxies:
		return m.beginGroupsRefresh(now, force)
	case pageProfiles:
		return m.beginProfilesRefresh(now, force)
	case pageConnections:
		return m.beginConnectionsRefresh(now, force)
	case pageLogs:
		return m.beginLogs(force)
	case pageSettings:
		return m.beginScheduleRefresh(now, force)
	default:
		return nil
	}
}

func (m *Model) cancelPageWork(target page) {
	switch target {
	case pageProxies:
		cancelRequest(&m.groupRequest)
	case pageProfiles:
		cancelRequest(&m.profileRequest)
	case pageConnections:
		cancelRequest(&m.connectionRequest)
	case pageLogs:
		m.stopLogs()
		m.clearSourceError(errorLogs)
	case pageSettings:
		cancelRequest(&m.scheduleRequest)
	}
}

func waitLog(entries <-chan domain.LogEntry, generation uint64) tea.Cmd {
	return func() tea.Msg {
		entry, ok := <-entries
		return logMsg{entry: entry, ok: ok, generation: generation}
	}
}

func waitLogError(errs <-chan error, generation uint64) tea.Cmd {
	return func() tea.Msg {
		err, ok := <-errs
		return logErrMsg{err: err, ok: ok, generation: generation}
	}
}

func waitTraffic(traffic <-chan domain.Traffic, generation uint64) tea.Cmd {
	return func() tea.Msg {
		sample, ok := <-traffic
		return trafficMsg{traffic: sample, at: time.Now(), ok: ok, generation: generation}
	}
}

func waitTrafficError(errs <-chan error, generation uint64) tea.Cmd {
	return func() tea.Msg {
		err, ok := <-errs
		return trafficErrMsg{err: err, ok: ok, generation: generation}
	}
}

func streamBackoff(attempt int) time.Duration {
	delay := time.Second
	for range min(max(attempt, 0), 5) {
		delay *= 2
	}
	return minDuration(delay, streamRetryMaximum)
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func (m *Model) beginLogs(force bool) tea.Cmd {
	if force {
		m.stopLogs()
		m.clearSourceError(errorLogs)
	}
	if m.page != pageLogs || !m.status.Service.Active || m.logConnecting || m.logCh != nil || m.logErrCh != nil || m.logReconnectPending || m.ctx.Err() != nil {
		return nil
	}
	m.logGeneration++
	generation := m.logGeneration
	streamCtx, cancel := context.WithCancel(m.ctx)
	m.logCancel = cancel
	m.logConnecting = true
	return func() tea.Msg {
		entries, errs := m.backend.WatchLogs(streamCtx, m.logLevel)
		return logChannelsMsg{entries: entries, errs: errs, generation: generation}
	}
}

func (m *Model) beginTraffic(force bool) tea.Cmd {
	if force {
		m.stopTraffic()
	}
	if m.trafficConnecting || m.trafficCh != nil || m.trafficErrCh != nil || m.trafficReconnectPending || m.ctx.Err() != nil {
		return nil
	}
	m.trafficGeneration++
	generation := m.trafficGeneration
	streamCtx, cancel := context.WithCancel(m.ctx)
	m.trafficCancel = cancel
	m.trafficConnecting = true
	return func() tea.Msg {
		traffic, errs := m.backend.WatchTraffic(streamCtx)
		return trafficChannelsMsg{traffic: traffic, errs: errs, generation: generation}
	}
}

func (m *Model) scheduleLogReconnect() tea.Cmd {
	if m.page != pageLogs || !m.status.Service.Active || m.logReconnectPending || m.ctx.Err() != nil {
		return nil
	}
	m.logReconnectPending = true
	generation := m.logGeneration
	delay := streamBackoff(m.logRetry)
	m.logRetry++
	return tea.Tick(delay, func(time.Time) tea.Msg { return logReconnectMsg{generation: generation} })
}

func (m *Model) scheduleTrafficReconnect() tea.Cmd {
	if m.trafficReconnectPending || m.ctx.Err() != nil {
		return nil
	}
	m.trafficReconnectPending = true
	generation := m.trafficGeneration
	delay := streamBackoff(m.trafficRetry)
	m.trafficRetry++
	return tea.Tick(delay, func(time.Time) tea.Msg { return trafficReconnectMsg{generation: generation} })
}

func (m *Model) disconnectLogs(generation uint64, err error) tea.Cmd {
	if generation != m.logGeneration {
		return nil
	}
	if m.logCancel != nil {
		m.logCancel()
	}
	m.logCancel = nil
	m.logCh, m.logErrCh = nil, nil
	m.logConnecting = false
	m.logGeneration++
	if err != nil && m.ctx.Err() == nil && m.status.Service.Active {
		m.setSourceError(errorLogs, err)
	}
	return m.scheduleLogReconnect()
}

func (m *Model) disconnectTraffic(generation uint64, err error) tea.Cmd {
	if generation != m.trafficGeneration {
		return nil
	}
	if m.trafficCancel != nil {
		m.trafficCancel()
	}
	m.trafficCancel = nil
	m.trafficCh, m.trafficErrCh = nil, nil
	m.trafficConnecting = false
	m.trafficGeneration++
	if err != nil && m.ctx.Err() == nil && m.status.Service.Active {
		m.setSourceError(errorTraffic, err)
	}
	return m.scheduleTrafficReconnect()
}

func (m *Model) stopLogs() {
	if m.logCancel != nil {
		m.logCancel()
	}
	m.logCancel = nil
	m.logCh, m.logErrCh = nil, nil
	m.logConnecting = false
	m.logReconnectPending = false
	m.logRetry = 0
	m.logGeneration++
}

func (m *Model) stopTraffic() {
	if m.trafficCancel != nil {
		m.trafficCancel()
	}
	m.trafficCancel = nil
	m.trafficCh, m.trafficErrCh = nil, nil
	m.trafficConnecting = false
	m.trafficReconnectPending = false
	m.trafficRetry = 0
	m.trafficGeneration++
}

func (m *Model) beginOperation(message string, fn func(context.Context) error) tea.Cmd {
	if m.loading {
		return nil
	}
	m.loading = true
	m.mutationGeneration++
	m.clearSourceError(errorOperation)
	generation := m.mutationGeneration
	return func() tea.Msg {
		err := fn(m.ctx)
		return operationMsg{message: message, err: err, generation: generation}
	}
}

func (m *Model) beginConfigOperation(message, key, value string) tea.Cmd {
	operation := m.beginOperation(message, func(ctx context.Context) error {
		return m.backend.SetConfig(ctx, key, value)
	})
	if operation == nil {
		return nil
	}
	return func() tea.Msg {
		result := operation().(operationMsg)
		result.configKey = key
		result.configValue = value
		return result
	}
}

func (m *Model) beginGroupTest(group string) tea.Cmd {
	if m.loading {
		return nil
	}
	m.loading = true
	m.mutationGeneration++
	m.clearSourceError(errorGroupTest)
	generation := m.mutationGeneration
	return func() tea.Msg {
		delays, err := m.backend.TestGroup(m.ctx, group)
		return groupTestMsg{group: group, delays: delays, err: err, completedAt: time.Now(), generation: generation}
	}
}

func (m *Model) setSourceError(source errorSource, err error) {
	if err == nil {
		m.clearSourceError(source)
		return
	}
	if m.errors == nil {
		m.errors = make(map[errorSource]sourceError)
	}
	m.errorSequence++
	m.errors[source] = sourceError{message: err.Error(), sequence: m.errorSequence}
	m.syncVisibleError()
}

func (m *Model) clearSourceError(source errorSource) {
	delete(m.errors, source)
	m.syncVisibleError()
}

func (m *Model) syncVisibleError() {
	m.err = ""
	var latest uint64
	for _, item := range m.errors {
		if item.sequence >= latest {
			latest = item.sequence
			m.err = item.message
		}
	}
}

func (m *Model) showToast(message string, warning bool, now time.Time) {
	if now.IsZero() {
		now = time.Now()
	}
	m.toast = message
	m.toastWarning = warning
	m.toastExpires = now.Add(toastLifetime)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case bootstrapMsg:
		now := time.Now()
		return m, tea.Batch(
			m.beginStatusRefresh(now, true),
			m.beginPageRefresh(m.page, now, true),
			m.beginTraffic(false),
		)
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.input.SetWidth(max(20, min(72, m.width-10)))
		m.syncViewports()
	case tea.KeyPressMsg:
		if m.inMode != inputNone {
			return m.updateInput(msg)
		}
		if m.picker != pickerNone {
			return m.updatePicker(msg)
		}
		return m.updateKey(msg)
	case tickMsg:
		now := time.Time(msg)
		if m.toast != "" && !m.toastExpires.IsZero() && !now.Before(m.toastExpires) {
			m.toast = ""
			m.toastWarning = false
			m.toastExpires = time.Time{}
		}
		return m, tea.Batch(
			m.beginStatusRefresh(now, false),
			m.beginPageRefresh(m.page, now, false),
			tick(),
		)
	case statusMsg:
		if !acceptResponse(&m.statusRequest, msg.generation) {
			return m, nil
		}
		queued := takeQueued(&m.statusRequest)
		next := func() tea.Cmd {
			if queued {
				return m.beginStatusRefresh(time.Now(), true)
			}
			return nil
		}
		if !msg.requestedAt.IsZero() && msg.requestedAt.Before(m.statusSnapshotFloor) {
			return m, next()
		}
		if msg.requestedAt.After(m.statusSnapshotFloor) {
			m.statusSnapshotFloor = msg.requestedAt
		}
		if msg.err != nil {
			if msg.status.ConfigAvailable {
				mergeConfigStatus(&m.status, msg.status)
			}
			m.setSourceError(errorStatus, msg.err)
		} else {
			currentTraffic := m.status.Traffic
			m.status = msg.status
			if !msg.requestedAt.IsZero() && msg.requestedAt.Before(m.lastTrafficAt) {
				m.status.Traffic.Up = currentTraffic.Up
				m.status.Traffic.Down = currentTraffic.Down
			} else if msg.status.Service.Active && !m.trafficStreamActive() {
				m.recordTrafficAt(msg.status.Traffic, msg.requestedAt)
			} else {
				m.status.Traffic.Up = currentTraffic.Up
				m.status.Traffic.Down = currentTraffic.Down
			}
			if !msg.status.Service.Active {
				m.trafficHistory = nil
				m.lastTrafficAt = time.Time{}
				m.status.Traffic = msg.status.Traffic
				m.status.Traffic.Up, m.status.Traffic.Down = 0, 0
				m.clearSourceError(errorTraffic)
				m.clearSourceError(errorLogs)
				if m.trafficStreamPresent() {
					m.stopTraffic()
				}
				if m.logStreamPresent() {
					m.stopLogs()
				}
			}
			m.clearSourceError(errorStatus)
		}
		logLevelChanged := false
		if m.status.ConfigAvailable {
			if level, ok := normalizeLogLevel(m.status.LogLevel); ok && level != m.logLevel {
				m.logLevel = level
				logLevelChanged = true
			}
		}
		var trafficCmd, logCmd tea.Cmd
		if msg.generation != 0 && msg.err == nil && msg.status.Service.Active {
			trafficCmd = m.beginTraffic(false)
			if m.page == pageLogs {
				logCmd = m.beginLogs(logLevelChanged)
			}
		}
		return m, tea.Batch(next(), trafficCmd, logCmd)
	case scheduleMsg:
		if !acceptResponse(&m.scheduleRequest, msg.generation) {
			return m, nil
		}
		queued := takeQueued(&m.scheduleRequest)
		if msg.err != nil {
			m.scheduleOK = false
			m.setSourceError(errorSchedule, msg.err)
		} else {
			m.schedule = msg.status
			m.scheduleOK = true
			m.clearSourceError(errorSchedule)
		}
		if queued && m.page == pageSettings {
			return m, m.beginScheduleRefresh(time.Now(), true)
		}
		return m, nil
	case groupsMsg:
		if !acceptResponse(&m.groupRequest, msg.generation) {
			return m, nil
		}
		queued := takeQueued(&m.groupRequest)
		if msg.err != nil {
			m.setSourceError(errorGroups, msg.err)
		} else {
			groupName, proxyName := m.selectedProxyKeys()
			m.groups = msg.groups
			m.restoreProxySelection(groupName, proxyName)
			m.pruneProxyTestStates()
			m.clearSourceError(errorGroups)
		}
		if queued && m.page == pageProxies {
			return m, m.beginGroupsRefresh(time.Now(), true)
		}
		return m, nil
	case profilesMsg:
		if !acceptResponse(&m.profileRequest, msg.generation) {
			return m, nil
		}
		queued := takeQueued(&m.profileRequest)
		if msg.err != nil {
			m.setSourceError(errorProfiles, msg.err)
		} else {
			selected := m.selectedProfileKey()
			m.profiles = msg.profiles
			m.restoreProfileSelection(selected)
			m.clearSourceError(errorProfiles)
		}
		if queued && m.page == pageProfiles {
			return m, m.beginProfilesRefresh(time.Now(), true)
		}
		return m, nil
	case connectionsMsg:
		if !acceptResponse(&m.connectionRequest, msg.generation) {
			return m, nil
		}
		queued := takeQueued(&m.connectionRequest)
		if msg.err != nil {
			m.setSourceError(errorConnections, msg.err)
		} else {
			selected := m.selectedConnectionKey()
			m.connections = msg.connections
			m.restoreConnectionSelection(selected)
			m.clearSourceError(errorConnections)
		}
		if queued && m.page == pageConnections {
			return m, m.beginConnectionsRefresh(time.Now(), true)
		}
		return m, nil
	case operationMsg:
		if msg.generation != 0 && msg.generation != m.mutationGeneration {
			return m, nil
		}
		m.loading = false
		m.confirm = confirmNone
		m.confirmTarget = confirmTarget{}
		if msg.err != nil {
			m.setSourceError(errorOperation, msg.err)
		} else {
			if msg.configKey == string(settingLogLevel) {
				if level, ok := normalizeLogLevel(msg.configValue); ok {
					m.logLevel = level
				}
			}
			m.clearSourceError(errorOperation)
			m.showToast(msg.message, false, time.Now())
		}
		now := time.Now()
		return m, tea.Batch(m.beginStatusRefresh(now, true), m.beginPageRefresh(m.page, now, true))
	case groupTestMsg:
		if msg.generation != 0 && msg.generation != m.mutationGeneration {
			return m, nil
		}
		m.loading = false
		completedAt := msg.completedAt
		if completedAt.IsZero() {
			completedAt = time.Now()
		}
		if msg.err != nil {
			m.setSourceError(errorGroupTest, msg.err)
			if m.page == pageProxies {
				return m, m.beginGroupsRefresh(time.Now(), true)
			}
			return m, nil
		}
		m.clearSourceError(errorGroupTest)
		stats := m.applyGroupDelaysAt(msg.group, msg.delays, completedAt)
		message := stats.notice()
		if stats.total == 0 {
			message = "测速完成，未找到策略组"
		}
		m.showToast(message, stats.timedOut > 0 || stats.total == 0, completedAt)
		if m.page == pageProxies {
			return m, m.beginGroupsRefresh(time.Now(), true)
		}
		return m, nil
	case logChannelsMsg:
		if msg.generation != m.logGeneration {
			return m, nil
		}
		m.logConnecting = false
		m.logCh, m.logErrCh = msg.entries, msg.errs
		if m.logCh == nil && m.logErrCh == nil {
			return m, m.disconnectLogs(msg.generation, fmt.Errorf("日志流未返回数据通道"))
		}
		var entryCmd, errorCmd tea.Cmd
		if m.logCh != nil {
			entryCmd = waitLog(m.logCh, msg.generation)
		}
		if m.logErrCh != nil {
			errorCmd = waitLogError(m.logErrCh, msg.generation)
		}
		return m, tea.Batch(entryCmd, errorCmd)
	case logMsg:
		if msg.generation != m.logGeneration {
			return m, nil
		}
		if msg.ok {
			m.appendLog(msg.entry)
			m.logRetry = 0
			m.clearSourceError(errorLogs)
			if m.logCh != nil {
				return m, waitLog(m.logCh, msg.generation)
			}
			return m, nil
		}
		m.logCh = nil
		if m.logErrCh == nil {
			return m, m.disconnectLogs(msg.generation, nil)
		}
		return m, nil
	case logErrMsg:
		if msg.generation != m.logGeneration {
			return m, nil
		}
		if msg.err != nil {
			return m, m.disconnectLogs(msg.generation, msg.err)
		}
		m.logErrCh = nil
		if m.logCh == nil {
			return m, m.disconnectLogs(msg.generation, nil)
		}
		return m, nil
	case logReconnectMsg:
		if msg.generation != m.logGeneration || !m.logReconnectPending || m.page != pageLogs || !m.status.Service.Active {
			return m, nil
		}
		m.logReconnectPending = false
		return m, m.beginLogs(false)
	case trafficChannelsMsg:
		if msg.generation != m.trafficGeneration {
			return m, nil
		}
		m.trafficConnecting = false
		m.trafficCh, m.trafficErrCh = msg.traffic, msg.errs
		if m.trafficCh == nil && m.trafficErrCh == nil {
			return m, m.disconnectTraffic(msg.generation, fmt.Errorf("流量流未返回数据通道"))
		}
		var trafficCmd, errorCmd tea.Cmd
		if m.trafficCh != nil {
			trafficCmd = waitTraffic(m.trafficCh, msg.generation)
		}
		if m.trafficErrCh != nil {
			errorCmd = waitTrafficError(m.trafficErrCh, msg.generation)
		}
		return m, tea.Batch(trafficCmd, errorCmd)
	case trafficMsg:
		if msg.generation != m.trafficGeneration {
			return m, nil
		}
		if msg.ok {
			m.status.Traffic.Up = msg.traffic.Up
			m.status.Traffic.Down = msg.traffic.Down
			m.status.Traffic.UpTotal = max64(m.status.Traffic.UpTotal, msg.traffic.UpTotal)
			m.status.Traffic.DownTotal = max64(m.status.Traffic.DownTotal, msg.traffic.DownTotal)
			m.recordTrafficAt(msg.traffic, msg.at)
			m.trafficRetry = 0
			m.clearSourceError(errorTraffic)
			if m.trafficCh != nil {
				return m, waitTraffic(m.trafficCh, msg.generation)
			}
			return m, nil
		}
		m.trafficCh = nil
		if m.trafficErrCh == nil {
			return m, m.disconnectTraffic(msg.generation, nil)
		}
		return m, nil
	case trafficErrMsg:
		if msg.generation != m.trafficGeneration {
			return m, nil
		}
		if msg.err != nil {
			return m, m.disconnectTraffic(msg.generation, msg.err)
		}
		m.trafficErrCh = nil
		if m.trafficCh == nil {
			return m, m.disconnectTraffic(msg.generation, nil)
		}
		return m, nil
	case trafficReconnectMsg:
		if msg.generation != m.trafficGeneration || !m.trafficReconnectPending {
			return m, nil
		}
		m.trafficReconnectPending = false
		return m, m.beginTraffic(false)
	default:
		if m.inMode != inputNone {
			return m.updateInput(msg)
		}
	}
	return m, nil
}

func (m *Model) recordTrafficAt(traffic domain.Traffic, at time.Time) {
	if at.IsZero() {
		at = time.Now()
	}
	if count := len(m.trafficHistory); count > 0 && !at.After(m.trafficHistory[count-1].at) {
		if at.Equal(m.trafficHistory[count-1].at) {
			m.trafficHistory[count-1].up = nonNegativeTraffic(traffic.Up)
			m.trafficHistory[count-1].down = nonNegativeTraffic(traffic.Down)
		}
		return
	}
	m.trafficHistory = append(m.trafficHistory, trafficSample{
		at:   at,
		up:   nonNegativeTraffic(traffic.Up),
		down: nonNegativeTraffic(traffic.Down),
	})
	m.lastTrafficAt = at
	if len(m.trafficHistory) <= trafficHistoryLimit {
		return
	}
	copy(m.trafficHistory, m.trafficHistory[len(m.trafficHistory)-trafficHistoryLimit:])
	m.trafficHistory = m.trafficHistory[:trafficHistoryLimit]
}

func (m Model) trafficStreamActive() bool {
	return m.trafficConnecting || m.trafficCh != nil
}

func (m Model) trafficStreamPresent() bool {
	return m.trafficConnecting || m.trafficCh != nil || m.trafficErrCh != nil || m.trafficReconnectPending || m.trafficCancel != nil
}

func (m Model) logStreamPresent() bool {
	return m.logConnecting || m.logCh != nil || m.logErrCh != nil || m.logReconnectPending || m.logCancel != nil
}

func nonNegativeTraffic(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func (m Model) updateKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if m.confirm != confirmNone {
		switch key {
		case "y", "enter":
			return m.runConfirmed()
		case "n", "esc", "q":
			m.confirm = confirmNone
			m.confirmTarget = confirmTarget{}
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
		return m.switchPage((m.page + 1) % page(len(pageNames)))
	case "shift+tab", "left", "h":
		m.applyFilter("")
		return m.switchPage((m.page + page(len(pageNames)) - 1) % page(len(pageNames)))
	case "up", "k":
		if m.page == pageLogs {
			m.scrollLogs(1)
		} else {
			m.moveCursor(-1)
		}
	case "down", "j":
		if m.page == pageLogs {
			m.scrollLogs(-1)
		} else {
			m.moveCursor(1)
		}
	case "home":
		m.moveToBoundary(false)
	case "end":
		m.moveToBoundary(true)
	case "pgup":
		m.moveByPage(-1)
	case "pgdown":
		m.moveByPage(1)
	case "[":
		if m.page == pageProxies {
			m.moveGroup(-1)
		}
	case "]":
		if m.page == pageProxies {
			m.moveGroup(1)
		}
	case "r":
		now := time.Now()
		return m, tea.Batch(m.beginStatusRefresh(now, true), m.beginPageRefresh(m.page, now, true))
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
			return m, m.beginGroupTest(group)
		}
	case "a":
		if m.page == pageProfiles {
			return m, m.focusInput(inputProfile, "订阅 URL、本地路径或节点 URI", "")
		}
	case "u":
		if m.page == pageProfiles && len(m.profiles) > 0 {
			name := m.profiles[clamp(m.profileCursor, 0, len(m.profiles)-1)].Name
			return m, m.beginOperation("配置已更新", func(ctx context.Context) error {
				return m.backend.UpdateProfile(ctx, name)
			})
		}
	case "d":
		switch m.page {
		case pageProfiles:
			if len(m.profiles) > 0 {
				profile := m.profiles[clamp(m.profileCursor, 0, len(m.profiles)-1)]
				if profile.Active {
					break
				}
				m.confirm = confirmRemoveProfile
				m.confirmTarget = confirmTarget{label: profile.Name, name: profile.Name, id: profile.ID}
			}
		case pageConnections:
			connections := m.filteredConnections()
			if len(connections) > 0 {
				connection := connections[clamp(m.connectionCursor, 0, len(connections)-1)]
				m.confirm = confirmCloseConnection
				m.confirmTarget = confirmTarget{label: connectionLabel(connection), id: connection.ID}
			}
		}
	case "x":
		if m.page == pageConnections && len(m.connections) > 0 {
			m.confirm = confirmCloseAll
			m.confirmTarget = confirmTarget{label: "全部活动连接"}
		}
	case "space":
		if m.page == pageLogs {
			if m.logPaused {
				m.logPaused = false
				m.logOffset = 0
				m.logUnread = 0
			} else {
				m.logPaused = true
				m.logUnread = 0
			}
		}
	}
	return m, nil
}

func (m Model) switchPage(next page) (tea.Model, tea.Cmd) {
	if next == m.page {
		return m, nil
	}
	m.cancelPageWork(m.page)
	m.page = next
	m.syncViewports()
	return m, m.beginPageRefresh(next, time.Now(), true)
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
			if mode == inputFilter {
				m.resetInput()
				m.applyFilter(value)
				return m, nil
			}
			if mode == inputMixedPort {
				normalized, err := normalizeMixedPort(value)
				if err != nil {
					m.inputError = err.Error()
					return m, nil
				}
				m.resetInput()
				return m, m.beginConfigOperation("混合端口已更新", string(settingMixedPort), normalized)
			}
			m.resetInput()
			if value == "" {
				return m, nil
			}
			return m, m.beginOperation("配置已添加，请选中后按 Enter 激活", func(ctx context.Context) error {
				return m.backend.AddProfile(ctx, "", value, 24*time.Hour)
			})
		}
	}
	if _, ok := msg.(tea.KeyPressMsg); ok {
		m.inputError = ""
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *Model) focusInput(mode inputMode, placeholder, value string) tea.Cmd {
	m.inMode = mode
	m.inputError = ""
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
	m.inputError = ""
}

func (m Model) updatePicker(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	options := m.pickerOptions()
	if len(options) == 0 {
		m.closePicker()
		return m, nil
	}
	switch msg.String() {
	case "esc", "q":
		m.closePicker()
	case "up", "k":
		m.pickerCursor = clamp(m.pickerCursor-1, 0, len(options)-1)
	case "down", "j":
		m.pickerCursor = clamp(m.pickerCursor+1, 0, len(options)-1)
	case "home":
		m.pickerCursor = 0
	case "end":
		m.pickerCursor = len(options) - 1
	case "enter", "space":
		kind := m.picker
		value := options[clamp(m.pickerCursor, 0, len(options)-1)].value
		m.closePicker()
		switch kind {
		case pickerTUN:
			enabled := value == "on"
			return m, m.beginOperation("TUN 设置已更新", func(ctx context.Context) error {
				return m.backend.SetTUN(ctx, enabled)
			})
		case pickerAllowLAN:
			if value == "on" {
				m.confirm = confirmEnableLAN
				return m, nil
			}
			return m, m.beginConfigOperation("局域网访问已关闭", string(settingAllowLAN), value)
		case pickerIPv6:
			return m, m.beginConfigOperation("IPv6 设置已更新", string(settingIPv6), value)
		case pickerLogLevel:
			return m, m.beginConfigOperation("日志级别已更新", string(settingLogLevel), value)
		}
	}
	return m, nil
}

func (m *Model) openPicker(kind settingPicker, current string) {
	m.picker = kind
	m.pickerCursor = 0
	for index, option := range m.pickerOptions() {
		if option.value == current {
			m.pickerCursor = index
			break
		}
	}
}

func (m *Model) closePicker() {
	m.picker = pickerNone
	m.pickerCursor = 0
}

func (m Model) pickerOptions() []pickerOption {
	switch m.picker {
	case pickerTUN, pickerAllowLAN, pickerIPv6:
		return []pickerOption{{label: "关闭", value: "off"}, {label: "开启", value: "on"}}
	case pickerLogLevel:
		return []pickerOption{
			{label: "DEBUG", value: "debug"},
			{label: "INFO", value: "info"},
			{label: "WARNING", value: "warning"},
			{label: "ERROR", value: "error"},
			{label: "SILENT", value: "silent"},
		}
	default:
		return nil
	}
}

func normalizeLogLevel(value string) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "debug", "info", "warning", "error", "silent":
		return value, true
	default:
		return "", false
	}
}

func mergeConfigStatus(target *domain.RuntimeStatus, source domain.RuntimeStatus) {
	target.ConfigAvailable = true
	target.Mode = source.Mode
	target.TUN = source.TUN
	target.MixedPort = source.MixedPort
	target.AllowLAN = source.AllowLAN
	target.IPv6 = source.IPv6
	target.LogLevel = source.LogLevel
}

func normalizeMixedPort(value string) (string, error) {
	port, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("端口必须是 1 到 65535")
	}
	return strconv.Itoa(port), nil
}

func (m Model) activate() (tea.Model, tea.Cmd) {
	switch m.page {
	case pageOverview:
		if m.status.Service.Active {
			m.confirm = confirmStopService
			m.confirmTarget = confirmTarget{label: "Mihomo 服务"}
			return m, nil
		}
		return m, m.beginOperation("Mihomo 已启动", func(ctx context.Context) error {
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
		return m, m.beginOperation("已切换到 "+node, func(ctx context.Context) error {
			return m.backend.SelectProxy(ctx, group.Name, node)
		})
	case pageProfiles:
		if len(m.profiles) > 0 {
			profile := m.profiles[clamp(m.profileCursor, 0, len(m.profiles)-1)]
			name := profile.Name
			if profile.Active {
				return m, nil
			}
			return m, m.beginOperation("已启用 "+name, func(ctx context.Context) error {
				return m.backend.UseProfile(ctx, name)
			})
		}
	case pageConnections:
		connections := m.filteredConnections()
		if len(connections) > 0 {
			connection := connections[clamp(m.connectionCursor, 0, len(connections)-1)]
			m.confirm = confirmCloseConnection
			m.confirmTarget = confirmTarget{label: connectionLabel(connection), id: connection.ID}
		}
	case pageSettings:
		return m.activateSetting()
	}
	return m, nil
}

func (m Model) activateSetting() (tea.Model, tea.Cmd) {
	switch m.selectedSetting() {
	case settingService:
		action, message := "start", "Mihomo 已启动"
		if m.status.Service.Active {
			m.confirm = confirmStopService
			m.confirmTarget = confirmTarget{label: "Mihomo 服务"}
			return m, nil
		}
		return m, m.beginOperation(message, func(ctx context.Context) error { return m.backend.Service(ctx, action) })
	case settingStartup:
		action := "enable"
		message := "开机启动已启用"
		if m.status.Service.Enabled {
			action, message = "disable", "开机启动已关闭"
		}
		return m, m.beginOperation(message, func(ctx context.Context) error { return m.backend.Service(ctx, action) })
	case settingMode:
		next := domain.ModeRule
		if m.status.Mode == domain.ModeRule {
			next = domain.ModeGlobal
		} else if m.status.Mode == domain.ModeGlobal {
			next = domain.ModeDirect
		}
		return m, m.beginOperation("运行模式已切换", func(ctx context.Context) error { return m.backend.SetMode(ctx, next) })
	case settingTUN:
		if !m.status.ConfigAvailable {
			m.openPicker(pickerTUN, "")
			return m, nil
		}
		return m, m.beginOperation("TUN 设置已更新", func(ctx context.Context) error { return m.backend.SetTUN(ctx, !m.status.TUN) })
	case settingSchedule:
		enabled := true
		if m.scheduleOK {
			enabled = !m.schedule.Enabled
		}
		return m, m.beginOperation("定时更新设置已更新", func(ctx context.Context) error { return m.backend.SetSchedule(ctx, enabled) })
	case settingMixedPort:
		value := "7890"
		if m.status.ConfigAvailable && m.status.MixedPort > 0 {
			value = strconv.Itoa(m.status.MixedPort)
		}
		return m, m.focusInput(inputMixedPort, "1 - 65535", value)
	case settingAllowLAN:
		if !m.status.ConfigAvailable {
			m.openPicker(pickerAllowLAN, "")
			return m, nil
		}
		if m.status.AllowLAN {
			return m, m.beginConfigOperation("局域网访问已关闭", string(settingAllowLAN), "off")
		}
		m.confirm = confirmEnableLAN
		return m, nil
	case settingIPv6:
		if !m.status.ConfigAvailable {
			m.openPicker(pickerIPv6, "")
			return m, nil
		}
		value := "on"
		if m.status.IPv6 {
			value = "off"
		}
		return m, m.beginConfigOperation("IPv6 设置已更新", string(settingIPv6), value)
	case settingLogLevel:
		current := "info"
		if m.status.ConfigAvailable {
			if level, ok := normalizeLogLevel(m.status.LogLevel); ok {
				current = level
			}
		}
		m.openPicker(pickerLogLevel, current)
		return m, nil
	}
	return m, nil
}

func (m Model) selectedSetting() settingID {
	if len(settingOrder) == 0 {
		return ""
	}
	return settingOrder[clamp(m.settingCursor, 0, len(settingOrder)-1)]
}

func (m Model) runConfirmed() (tea.Model, tea.Cmd) {
	mode, target := m.confirm, m.confirmTarget
	m.confirm = confirmNone
	m.confirmTarget = confirmTarget{}
	switch mode {
	case confirmRemoveProfile:
		if target.name == "" {
			return m, nil
		}
		return m, m.beginOperation("配置已删除", func(ctx context.Context) error { return m.backend.RemoveProfile(ctx, target.name) })
	case confirmCloseConnection:
		if target.id == "" {
			return m, nil
		}
		return m, m.beginOperation("连接已关闭", func(ctx context.Context) error { return m.backend.CloseConnection(ctx, target.id) })
	case confirmCloseAll:
		return m, m.beginOperation("全部连接已关闭", m.backend.CloseAllConnections)
	case confirmStopService:
		return m, m.beginOperation("Mihomo 已停止", func(ctx context.Context) error { return m.backend.Service(ctx, "stop") })
	case confirmEnableLAN:
		return m, m.beginConfigOperation("局域网访问已开启", string(settingAllowLAN), "on")
	}
	return m, nil
}

func connectionLabel(connection domain.Connection) string {
	if connection.Host != "" {
		return connection.Host
	}
	if connection.Destination != "" {
		return connection.Destination
	}
	return connection.ID
}

func (m *Model) appendLog(entry domain.LogEntry) {
	visible := m.logMatchesFilter(entry)
	if m.logPaused {
		m.logUnread++
	}
	if visible && (m.logPaused || m.logOffset > 0) {
		m.logOffset++
	}
	m.logs = append(m.logs, entry)
	if len(m.logs) > 1000 {
		m.logs = append([]domain.LogEntry(nil), m.logs[len(m.logs)-1000:]...)
	}
	m.logOffset = clamp(m.logOffset, 0, max(0, len(m.filteredLogs())-m.logCapacity()))
}

func (m Model) logMatchesFilter(entry domain.LogEntry) bool {
	if m.filter == "" {
		return true
	}
	haystack := strings.ToLower(entry.Time + " " + entry.Level + " " + entry.Message)
	return strings.Contains(haystack, strings.ToLower(m.filter))
}

func (m Model) logCapacity() int {
	return max(1, max(8, m.height-5)-4)
}

func (m *Model) scrollLogs(delta int) {
	maximum := max(0, len(m.filteredLogs())-m.logCapacity())
	m.logOffset = clamp(m.logOffset+delta, 0, maximum)
	if m.logOffset == 0 && !m.logPaused {
		m.logUnread = 0
	}
}

func (m *Model) moveToBoundary(end bool) {
	if m.page == pageLogs {
		if end {
			m.logOffset, m.logUnread = 0, 0
		} else {
			m.logOffset = max(0, len(m.filteredLogs())-m.logCapacity())
		}
		return
	}
	m.moveCursorBoundary(end)
}

func (m *Model) moveByPage(direction int) {
	if m.page == pageLogs {
		m.scrollLogs(-direction * m.logCapacity())
		return
	}
	capacity := m.listCapacity()
	if m.page == pageSettings {
		capacity = m.settingsCapacity()
	}
	m.moveCursor(direction * capacity)
}

func (m *Model) moveCursorBoundary(end bool) {
	last := 0
	switch m.page {
	case pageProxies:
		groups := m.filteredGroups()
		if len(groups) > 0 {
			last = max(0, len(filteredProxies(groups[clamp(m.groupCursor, 0, len(groups)-1)], m.filter))-1)
		}
		m.proxyCursor = 0
		if end {
			m.proxyCursor = last
		}
	case pageProfiles:
		last = max(0, len(m.profiles)-1)
		m.profileCursor = 0
		if end {
			m.profileCursor = last
		}
	case pageConnections:
		last = max(0, len(m.filteredConnections())-1)
		m.connectionCursor = 0
		if end {
			m.connectionCursor = last
		}
	case pageSettings:
		m.settingCursor = 0
		if end {
			m.settingCursor = max(0, len(settingOrder)-1)
		}
	}
	m.syncViewports()
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
		m.settingCursor = clamp(m.settingCursor+delta, 0, max(0, len(settingOrder)-1))
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
	m.settingCursor = clamp(m.settingCursor, 0, max(0, len(settingOrder)-1))
	m.syncViewports()
}

func (m Model) selectedProxyKeys() (string, string) {
	groups := m.filteredGroups()
	if len(groups) == 0 {
		return "", ""
	}
	group := groups[clamp(m.groupCursor, 0, len(groups)-1)]
	proxies := filteredProxies(group, m.filter)
	if len(proxies) == 0 {
		return group.Name, ""
	}
	return group.Name, proxies[clamp(m.proxyCursor, 0, len(proxies)-1)].Name
}

func (m *Model) restoreProxySelection(groupName, proxyName string) {
	m.groupCursor, m.proxyCursor = 0, 0
	groups := m.filteredGroups()
	for groupIndex, group := range groups {
		if group.Name != groupName {
			continue
		}
		m.groupCursor = groupIndex
		for proxyIndex, proxy := range filteredProxies(group, m.filter) {
			if proxy.Name == proxyName {
				m.proxyCursor = proxyIndex
				break
			}
		}
		break
	}
	m.clampCursors()
}

func profileKey(profile domain.Profile) string {
	if profile.ID != "" {
		return "id:" + profile.ID
	}
	return "name:" + profile.Name
}

func (m Model) selectedProfileKey() string {
	if len(m.profiles) == 0 {
		return ""
	}
	return profileKey(m.profiles[clamp(m.profileCursor, 0, len(m.profiles)-1)])
}

func (m *Model) restoreProfileSelection(selected string) {
	if selected != "" {
		for index, profile := range m.profiles {
			if profileKey(profile) == selected {
				m.profileCursor = index
				m.clampCursors()
				return
			}
		}
	}
	m.clampCursors()
}

func connectionKey(connection domain.Connection) string {
	if connection.ID != "" {
		return "id:" + connection.ID
	}
	return "fallback:" + strings.Join([]string{
		connection.Host,
		connection.Destination,
		connection.Source,
		connection.Process,
		connection.Network,
		connection.Start.UTC().Format(time.RFC3339Nano),
	}, "\x00")
}

func (m Model) selectedConnectionKey() string {
	connections := m.filteredConnections()
	if len(connections) == 0 {
		return ""
	}
	return connectionKey(connections[clamp(m.connectionCursor, 0, len(connections)-1)])
}

func (m *Model) restoreConnectionSelection(selected string) {
	if selected != "" {
		for index, connection := range m.filteredConnections() {
			if connectionKey(connection) == selected {
				m.connectionCursor = index
				m.clampCursors()
				return
			}
		}
	}
	m.clampCursors()
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
			selectedConnection = connectionKey(connections[clamp(m.connectionCursor, 0, len(connections)-1)])
		}
	}

	m.filter = value
	if m.page == pageLogs {
		m.logOffset = 0
		m.logUnread = 0
	}
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
				if connectionKey(connection) == selectedConnection {
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
	m.settingOffset = viewportOffset(len(settingOrder), m.settingCursor, m.settingOffset, m.settingsCapacity())
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
	timedOut         int
	leafSucceeded    int
	groupSucceeded   int
	builtinSucceeded int
	unknownSucceeded int
}

func (s groupDelayStats) notice() string {
	kinds := fmt.Sprintf("叶%d/组%d/内置%d", s.leafSucceeded, s.groupSucceeded, s.builtinSucceeded)
	if s.unknownSucceeded > 0 {
		kinds += fmt.Sprintf("/未知%d", s.unknownSucceeded)
	}
	return fmt.Sprintf("测速：成功%d（%s）超时%d", s.succeeded, kinds, s.timedOut)
}

func (m *Model) applyGroupDelays(group string, delays map[string]uint16) groupDelayStats {
	return m.applyGroupDelaysAt(group, delays, time.Now())
}

func (m *Model) applyGroupDelaysAt(group string, delays map[string]uint16, testedAt time.Time) groupDelayStats {
	for groupIndex := range m.groups {
		if m.groups[groupIndex].Name != group {
			continue
		}
		stats := groupDelayStats{total: len(m.groups[groupIndex].Proxies)}
		results := make(map[string]proxyTestResult, stats.total)
		for proxyIndex := range m.groups[groupIndex].Proxies {
			delay, found := groupMemberDelay(m.groups[groupIndex], proxyIndex, delays)
			key := groupMemberKey(m.groups[groupIndex], proxyIndex)
			result := proxyTestResult{state: proxyTestTimeout, testedAt: testedAt}
			if found && delay > 0 {
				result.state, result.delay = proxyTestSuccess, delay
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
			} else {
				stats.timedOut++
			}
			results[key] = result
		}
		if m.proxyTestStates == nil {
			m.proxyTestStates = make(map[string]map[string]proxyTestResult)
		}
		m.proxyTestStates[group] = results
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
	member := groupMemberKey(group, proxyIndex)
	delay, ok := delays[member]
	if !ok && member != proxy.Name {
		delay, ok = delays[proxy.Name]
	}
	return delay, ok
}

func groupMemberKey(group domain.ProxyGroup, proxyIndex int) string {
	if proxyIndex >= 0 && proxyIndex < len(group.All) && group.All[proxyIndex] != "" {
		return group.All[proxyIndex]
	}
	if proxyIndex >= 0 && proxyIndex < len(group.Proxies) {
		return group.Proxies[proxyIndex].Name
	}
	return ""
}

func (m Model) proxyTestResultFor(groupName string, group domain.ProxyGroup, proxyIndex int) (proxyTestResult, bool) {
	results := m.proxyTestStates[groupName]
	if len(results) == 0 {
		return proxyTestResult{}, false
	}
	key := groupMemberKey(group, proxyIndex)
	result, ok := results[key]
	if !ok && proxyIndex >= 0 && proxyIndex < len(group.Proxies) && key != group.Proxies[proxyIndex].Name {
		result, ok = results[group.Proxies[proxyIndex].Name]
	}
	return result, ok
}

func (m Model) proxyDisplayState(groupName string, group domain.ProxyGroup, proxyIndex int) (proxyTestState, uint16) {
	if result, ok := m.proxyTestResultFor(groupName, group, proxyIndex); ok {
		return result.state, result.delay
	}
	if proxyIndex >= 0 && proxyIndex < len(group.Proxies) {
		return proxyTestUnknown, group.Proxies[proxyIndex].Delay
	}
	return proxyTestUnknown, 0
}

func (m *Model) pruneProxyTestStates() {
	for groupName, results := range m.proxyTestStates {
		var group *domain.ProxyGroup
		for index := range m.groups {
			if m.groups[index].Name == groupName {
				group = &m.groups[index]
				break
			}
		}
		if group == nil {
			delete(m.proxyTestStates, groupName)
			continue
		}
		valid := make(map[string]struct{}, len(group.Proxies)*2)
		for index, proxy := range group.Proxies {
			valid[groupMemberKey(*group, index)] = struct{}{}
			valid[proxy.Name] = struct{}{}
		}
		for member := range results {
			if _, ok := valid[member]; !ok {
				delete(results, member)
			}
		}
		if len(results) == 0 {
			delete(m.proxyTestStates, groupName)
		}
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
