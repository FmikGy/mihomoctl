package tui

import (
	"context"
	"errors"
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
	overviewRefreshEvery   = 5 * time.Second
	profileRefreshEvery    = 30 * time.Second
	settingsRefreshEvery   = 30 * time.Second
	toastLifetime          = 3 * time.Second
	streamRetryMaximum     = 30 * time.Second
	runtimeRecoveryGrace   = 6 * time.Second
	logBatchWindow         = 50 * time.Millisecond
	logBatchMaximum        = 128
	logBufferMaximum       = 1000
	refreshRequestTimeout  = 5 * time.Second
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
	errorOverview    errorSource = "overview"
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

type proxyView struct {
	proxyIndex int
}

type proxyGroupView struct {
	groupIndex int
	proxies    []proxyView
}

type proxyGroupSearch struct {
	name    string
	proxies []string
}

type connectionView struct {
	connectionIndex int
}

type Model struct {
	ctx        context.Context
	cancel     context.CancelFunc
	backend    Backend
	operations *operationTracker

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
	logSearch               []string
	logStart                int
	logVersion              uint64
	logViewVersion          uint64
	logViewFilter           string
	logViewStart            int
	logViewLength           int
	logFilteredIndices      []int
	groupsVersion           uint64
	groupViewVersion        uint64
	groupViewFilter         string
	groupViewValid          bool
	groupViews              []proxyGroupView
	groupProxyViewBuffers   [][]proxyView
	groupSearchVersion      uint64
	groupSearchValid        bool
	groupSearch             []proxyGroupSearch
	connectionsVersion      uint64
	connectionViewVersion   uint64
	connectionViewFilter    string
	connectionViewValid     bool
	connectionViews         []connectionView
	connectionSearchVersion uint64
	connectionSearchValid   bool
	connectionSearch        []string
	proxyTestStates         map[string]map[string]proxyTestResult
	logLevel                string
	logPaused               bool
	logOffset               int
	logUnread               int
	logCh                   <-chan domain.LogEntry
	logErrCh                <-chan error
	logDone                 <-chan struct{}
	logCancel               context.CancelFunc
	logConnecting           bool
	logGeneration           uint64
	logRetry                int
	logReconnectPending     bool
	trafficCh               <-chan domain.Traffic
	trafficErrCh            <-chan error
	trafficDone             <-chan struct{}
	trafficCancel           context.CancelFunc
	trafficConnecting       bool
	trafficGeneration       uint64
	trafficRetry            int
	trafficReconnectPending bool

	statusRequest     requestState
	groupRequest      requestState
	profileRequest    requestState
	connectionRequest requestState
	overviewRequest   requestState
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
	loading              bool
	err                  string
	errors               map[errorSource]sourceError
	errorSequence        uint64
	toast                string
	toastWarning         bool
	toastExpires         time.Time
	help                 bool
	filter               string
	input                textinput.Model
	inMode               inputMode
	inputError           string
	picker               settingPicker
	pickerCursor         int
	confirm              confirmMode
	confirmTarget        confirmTarget
	mutationGeneration   uint64
	privilegedOperation  bool
	authorizing          bool
	runtimeMutation      bool
	coreMutation         bool
	runtimeRecoveryUntil time.Time
	quitPending          bool
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
	coreOnly    bool
}
type overviewTelemetryMsg struct {
	telemetry   OverviewTelemetry
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
	action       string
	message      string
	err          error
	generation   uint64
	configKey    string
	configValue  string
	privileged   bool
	interactive  bool
	coreMutation bool
	retry        func(context.Context) error
}
type groupTestMsg struct {
	group       string
	delays      map[string]uint16
	err         error
	completedAt time.Time
	generation  uint64
}
type logBatchMsg struct {
	entries    []domain.LogEntry
	closed     bool
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
	done       <-chan struct{}
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
	done       <-chan struct{}
	generation uint64
}
type trafficReconnectMsg struct{ generation uint64 }

func New(ctx context.Context, backend Backend) Model {
	if ctx == nil {
		ctx = context.Background()
	}
	child, cancel := context.WithCancel(ctx)
	input := textinput.New()
	input.Prompt = ""
	input.CharLimit = 4096
	input.SetWidth(62)
	return Model{
		ctx:             child,
		cancel:          cancel,
		backend:         backend,
		operations:      newOperationTracker(),
		logLevel:        "info",
		input:           input,
		errors:          make(map[errorSource]sourceError),
		proxyTestStates: make(map[string]map[string]proxyTestResult),
	}
}

var runTeaProgram = func(ctx context.Context, model Model) error {
	_, err := tea.NewProgram(model, tea.WithContext(ctx)).Run()
	return err
}

func Run(ctx context.Context, backend Backend) error {
	if ctx == nil {
		ctx = context.Background()
	}
	model := New(ctx, backend)
	err := runTeaProgram(ctx, model)
	model.cancel()
	model.operations.stopAndWait()
	if errors.Is(err, tea.ErrInterrupted) || errors.Is(err, context.Canceled) {
		return nil
	}
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
	if m.runtimeMutation {
		return nil
	}
	generation, ok := queueOrStart(&m.statusRequest, now, statusRefreshEvery, force)
	if !ok {
		return nil
	}
	requestCtx, cancel := context.WithTimeout(m.ctx, refreshRequestTimeout)
	m.statusRequest.cancel = cancel
	requestedAt := now
	return func() tea.Msg {
		if backend, ok := m.backend.(CoreStatusBackend); ok {
			status, err := backend.CoreStatus(requestCtx)
			return statusMsg{status: status, err: err, requestedAt: requestedAt, generation: generation, coreOnly: true}
		}
		status, err := m.backend.Status(requestCtx)
		return statusMsg{status: status, err: err, requestedAt: requestedAt, generation: generation}
	}
}

func (m *Model) beginOverviewRefresh(now time.Time, force bool) tea.Cmd {
	if m.runtimeMutation || !m.status.Service.Active {
		return nil
	}
	backend, ok := m.backend.(OverviewTelemetryBackend)
	if !ok {
		m.overviewRequest.lastStart = now
		return nil
	}
	generation, ok := queueOrStart(&m.overviewRequest, now, overviewRefreshEvery, force)
	if !ok {
		return nil
	}
	requestCtx, cancel := context.WithTimeout(m.ctx, refreshRequestTimeout)
	m.overviewRequest.cancel = cancel
	requestedAt := now
	return func() tea.Msg {
		telemetry, err := backend.OverviewTelemetry(requestCtx)
		return overviewTelemetryMsg{telemetry: telemetry, err: err, requestedAt: requestedAt, generation: generation}
	}
}

func (m *Model) beginGroupsRefresh(now time.Time, force bool) tea.Cmd {
	if m.runtimeMutation || !m.status.Service.Active {
		return nil
	}
	generation, ok := queueOrStart(&m.groupRequest, now, groupRefreshEvery, force)
	if !ok {
		return nil
	}
	requestCtx, cancel := context.WithTimeout(m.ctx, refreshRequestTimeout)
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
	requestCtx, cancel := context.WithTimeout(m.ctx, refreshRequestTimeout)
	m.profileRequest.cancel = cancel
	return func() tea.Msg {
		profiles, err := m.backend.Profiles(requestCtx)
		return profilesMsg{profiles: profiles, err: err, generation: generation}
	}
}

func (m *Model) beginConnectionsRefresh(now time.Time, force bool) tea.Cmd {
	if m.runtimeMutation || !m.status.Service.Active {
		return nil
	}
	generation, ok := queueOrStart(&m.connectionRequest, now, connectionRefreshEvery, force)
	if !ok {
		return nil
	}
	requestCtx, cancel := context.WithTimeout(m.ctx, refreshRequestTimeout)
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
	requestCtx, cancel := context.WithTimeout(m.ctx, refreshRequestTimeout)
	m.scheduleRequest.cancel = cancel
	return func() tea.Msg {
		status, err := backend.ScheduleStatus(requestCtx)
		return scheduleMsg{status: status, err: err, generation: generation}
	}
}

func (m *Model) beginPageRefresh(target page, now time.Time, force bool) tea.Cmd {
	switch target {
	case pageOverview:
		return m.beginOverviewRefresh(now, force)
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
	case pageOverview:
		cancelRequest(&m.overviewRequest)
		m.clearSourceError(errorOverview)
	case pageProxies:
		cancelRequest(&m.groupRequest)
		m.clearSourceError(errorGroups)
	case pageProfiles:
		cancelRequest(&m.profileRequest)
		m.clearSourceError(errorProfiles)
	case pageConnections:
		cancelRequest(&m.connectionRequest)
		m.clearSourceError(errorConnections)
	case pageLogs:
		m.stopLogs()
		m.clearSourceError(errorLogs)
	case pageSettings:
		cancelRequest(&m.scheduleRequest)
		m.clearSourceError(errorSchedule)
	}
}

func (m *Model) invalidatePageSnapshot(target page) {
	switch target {
	case pageOverview:
		cancelRequest(&m.overviewRequest)
	case pageProxies:
		cancelRequest(&m.groupRequest)
	case pageProfiles:
		cancelRequest(&m.profileRequest)
	case pageConnections:
		cancelRequest(&m.connectionRequest)
	case pageSettings:
		cancelRequest(&m.scheduleRequest)
	}
}

func waitLogBatch(entries <-chan domain.LogEntry, generation uint64, done ...<-chan struct{}) tea.Cmd {
	return func() tea.Msg {
		var canceled <-chan struct{}
		if len(done) > 0 {
			canceled = done[0]
		}
		var entry domain.LogEntry
		var ok bool
		select {
		case entry, ok = <-entries:
		case <-canceled:
			return logBatchMsg{closed: true, generation: generation}
		}
		if !ok {
			return logBatchMsg{closed: true, generation: generation}
		}
		batch := make([]domain.LogEntry, 1, logBatchMaximum)
		batch[0] = entry
		timer := time.NewTimer(logBatchWindow)
		defer timer.Stop()
		for len(batch) < logBatchMaximum {
			select {
			case entry, ok = <-entries:
				if !ok {
					return logBatchMsg{entries: batch, closed: true, generation: generation}
				}
				batch = append(batch, entry)
			case <-timer.C:
				return logBatchMsg{entries: batch, generation: generation}
			case <-canceled:
				return logBatchMsg{entries: batch, closed: true, generation: generation}
			}
		}
		return logBatchMsg{entries: batch, generation: generation}
	}
}

func waitLogError(errs <-chan error, generation uint64, done ...<-chan struct{}) tea.Cmd {
	return func() tea.Msg {
		var canceled <-chan struct{}
		if len(done) > 0 {
			canceled = done[0]
		}
		select {
		case err, ok := <-errs:
			return logErrMsg{err: err, ok: ok, generation: generation}
		case <-canceled:
			return logErrMsg{generation: generation}
		}
	}
}

func waitTraffic(traffic <-chan domain.Traffic, generation uint64, done ...<-chan struct{}) tea.Cmd {
	return func() tea.Msg {
		var canceled <-chan struct{}
		if len(done) > 0 {
			canceled = done[0]
		}
		select {
		case sample, ok := <-traffic:
			return trafficMsg{traffic: sample, at: time.Now(), ok: ok, generation: generation}
		case <-canceled:
			return trafficMsg{generation: generation}
		}
	}
}

func waitTrafficError(errs <-chan error, generation uint64, done ...<-chan struct{}) tea.Cmd {
	return func() tea.Msg {
		var canceled <-chan struct{}
		if len(done) > 0 {
			canceled = done[0]
		}
		select {
		case err, ok := <-errs:
			return trafficErrMsg{err: err, ok: ok, generation: generation}
		case <-canceled:
			return trafficErrMsg{generation: generation}
		}
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
	if m.runtimeMutation || m.page != pageLogs || !m.status.Service.Active || m.logConnecting || m.logCh != nil || m.logErrCh != nil || m.logReconnectPending || m.ctx.Err() != nil {
		return nil
	}
	m.logGeneration++
	generation := m.logGeneration
	streamCtx, cancel := context.WithCancel(m.ctx)
	m.logCancel = cancel
	m.logConnecting = true
	return func() tea.Msg {
		entries, errs := m.backend.WatchLogs(streamCtx, m.logLevel)
		return logChannelsMsg{entries: entries, errs: errs, done: streamCtx.Done(), generation: generation}
	}
}

func (m *Model) beginTraffic(force bool) tea.Cmd {
	if force {
		m.stopTraffic()
	}
	if m.runtimeMutation || !m.status.Service.Active || m.trafficConnecting || m.trafficCh != nil || m.trafficErrCh != nil || m.trafficReconnectPending || m.ctx.Err() != nil {
		return nil
	}
	m.trafficGeneration++
	generation := m.trafficGeneration
	streamCtx, cancel := context.WithCancel(m.ctx)
	m.trafficCancel = cancel
	m.trafficConnecting = true
	return func() tea.Msg {
		traffic, errs := m.backend.WatchTraffic(streamCtx)
		return trafficChannelsMsg{traffic: traffic, errs: errs, done: streamCtx.Done(), generation: generation}
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
	if !m.status.Service.Active || m.trafficReconnectPending || m.ctx.Err() != nil {
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
	m.logCh, m.logErrCh, m.logDone = nil, nil, nil
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
	m.trafficCh, m.trafficErrCh, m.trafficDone = nil, nil, nil
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
	m.logCh, m.logErrCh, m.logDone = nil, nil, nil
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
	m.trafficCh, m.trafficErrCh, m.trafficDone = nil, nil, nil
	m.trafficConnecting = false
	m.trafficReconnectPending = false
	m.trafficRetry = 0
	m.trafficGeneration++
}

func (m *Model) beginOperation(message string, fn func(context.Context) error) tea.Cmd {
	return m.beginOperationAttempt("", message, "", "", false, false, fn)
}

func (m *Model) beginGroupTest(group string) tea.Cmd {
	if m.loading {
		return nil
	}
	m.loading = true
	m.mutationGeneration++
	m.clearForegroundErrors()
	generation := m.mutationGeneration
	ticket := reserveOperation(m.operations)
	testURL := ""
	for index := range m.groups {
		if m.groups[index].Name == group {
			testURL = m.groups[index].TestURL
			break
		}
	}
	return func() tea.Msg {
		if !ticket.start() {
			return groupTestMsg{group: group, err: context.Canceled, completedAt: time.Now(), generation: generation}
		}
		defer ticket.finish()
		var delays map[string]uint16
		var err error
		if backend, ok := m.backend.(GroupURLTestBackend); ok {
			delays, err = backend.TestGroupAtURL(m.ctx, group, testURL)
		} else {
			delays, err = m.backend.TestGroup(m.ctx, group)
		}
		return groupTestMsg{group: group, delays: delays, err: err, completedAt: time.Now(), generation: generation}
	}
}

func (m *Model) setSourceError(source errorSource, err error) {
	if err == nil {
		m.clearSourceError(source)
		return
	}
	if isRuntimeErrorSource(source) && m.runtimeErrorSuppressed(time.Now()) {
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

func isRuntimeErrorSource(source errorSource) bool {
	switch source {
	case errorStatus, errorOverview, errorGroups, errorConnections, errorTraffic, errorLogs:
		return true
	default:
		return false
	}
}

func (m Model) runtimeErrorSuppressed(now time.Time) bool {
	return m.runtimeMutation || (!m.runtimeRecoveryUntil.IsZero() && now.Before(m.runtimeRecoveryUntil))
}

func (m *Model) suspendRuntimeObservers() {
	cancelRequest(&m.statusRequest)
	cancelRequest(&m.overviewRequest)
	cancelRequest(&m.groupRequest)
	cancelRequest(&m.connectionRequest)
	m.statusRequest.lastStart = time.Time{}
	m.overviewRequest.lastStart = time.Time{}
	m.groupRequest.lastStart = time.Time{}
	m.connectionRequest.lastStart = time.Time{}
	m.stopTraffic()
	m.stopLogs()
	for _, source := range []errorSource{errorStatus, errorOverview, errorGroups, errorConnections, errorTraffic, errorLogs} {
		delete(m.errors, source)
	}
	m.syncVisibleError()
}

func (m *Model) clearSourceError(source errorSource) {
	delete(m.errors, source)
	m.syncVisibleError()
}

func (m *Model) syncVisibleError() {
	m.err = ""
	var latest uint64
	for source, item := range m.errors {
		if !foregroundErrorSource(source) {
			continue
		}
		if item.sequence >= latest {
			latest = item.sequence
			m.err = item.message
		}
	}
	if m.err != "" {
		return
	}
	for _, item := range m.errors {
		if item.sequence >= latest {
			latest = item.sequence
			m.err = item.message
		}
	}
}

func foregroundErrorSource(source errorSource) bool {
	return source == errorOperation || source == errorGroupTest
}

func (m *Model) clearForegroundErrors() {
	delete(m.errors, errorOperation)
	delete(m.errors, errorGroupTest)
	m.syncVisibleError()
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
		)
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.syncInputWidth()
		m.syncViewports()
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			return m, m.requestQuit()
		}
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
		previous := m.status
		recoveringRuntime := m.runtimeErrorSuppressed(time.Now())
		serviceKnown := msg.err == nil || serviceStatusAvailable(msg.status.Service)
		if msg.err != nil {
			partial := msg.status
			if recoveringRuntime {
				partial.CoreVersion = ""
			}
			mergePartialStatus(&m.status, partial)
			m.setSourceError(errorStatus, msg.err)
		} else {
			m.status = msg.status
			if msg.coreOnly {
				m.status.Memory = previous.Memory
				m.status.ConnectionCount = previous.ConnectionCount
				m.status.Traffic = previous.Traffic
			} else if !msg.requestedAt.IsZero() && msg.requestedAt.Before(m.lastTrafficAt) {
				m.status.Traffic.Up = previous.Traffic.Up
				m.status.Traffic.Down = previous.Traffic.Down
			} else if msg.status.Service.Active && !m.trafficStreamActive() {
				m.recordTrafficAt(msg.status.Traffic, msg.requestedAt)
			} else {
				m.status.Traffic.Up = previous.Traffic.Up
				m.status.Traffic.Down = previous.Traffic.Down
			}
			m.clearSourceError(errorStatus)
			if serviceKnown {
				m.runtimeRecoveryUntil = time.Time{}
			}
		}
		if msg.generation != 0 && msg.err != nil && !serviceKnown && !recoveringRuntime {
			m.status.Service = domain.ServiceStatus{}
			m.resetInactiveRuntime()
		} else if serviceKnown && !m.status.Service.Active {
			m.resetInactiveRuntime()
		}
		logLevelChanged := false
		if m.status.ConfigAvailable {
			if level, ok := normalizeLogLevel(m.status.LogLevel); ok && level != m.logLevel {
				m.logLevel = level
				logLevelChanged = true
			}
		}
		becameActive := serviceKnown && m.status.Service.Active && !previous.Service.Active
		var trafficCmd, logCmd, overviewCmd, runtimePageCmd tea.Cmd
		if msg.generation != 0 && serviceKnown && m.status.Service.Active {
			trafficCmd = m.beginTraffic(false)
			if m.page == pageLogs {
				logCmd = m.beginLogs(logLevelChanged)
			} else if m.page == pageOverview {
				overviewCmd = m.beginOverviewRefresh(time.Now(), becameActive)
			} else if m.page == pageProxies || m.page == pageConnections {
				runtimePageCmd = m.beginPageRefresh(m.page, time.Now(), becameActive)
			}
		}
		return m, tea.Batch(next(), trafficCmd, logCmd, overviewCmd, runtimePageCmd)
	case overviewTelemetryMsg:
		if !acceptResponse(&m.overviewRequest, msg.generation) {
			return m, nil
		}
		queued := takeQueued(&m.overviewRequest)
		next := func() tea.Cmd {
			if queued && m.page == pageOverview {
				return m.beginOverviewRefresh(time.Now(), true)
			}
			return nil
		}
		if !m.status.Service.Active {
			return m, nil
		}
		if msg.telemetry.MemoryValid {
			m.status.Memory = nonNegativeTraffic(msg.telemetry.Memory)
		}
		if msg.telemetry.ConnectionCountValid {
			m.status.ConnectionCount = max(0, msg.telemetry.ConnectionCount)
		}
		if msg.telemetry.TrafficTotalsValid {
			m.status.Traffic.UpTotal = max64(m.status.Traffic.UpTotal, msg.telemetry.Traffic.UpTotal)
			m.status.Traffic.DownTotal = max64(m.status.Traffic.DownTotal, msg.telemetry.Traffic.DownTotal)
		}
		if msg.telemetry.TrafficRatesValid && m.status.Service.Active && !m.trafficStreamActive() && (msg.requestedAt.IsZero() || !msg.requestedAt.Before(m.lastTrafficAt)) {
			m.status.Traffic.Up = msg.telemetry.Traffic.Up
			m.status.Traffic.Down = msg.telemetry.Traffic.Down
			m.recordTrafficAt(msg.telemetry.Traffic, msg.requestedAt)
		}
		if msg.err != nil {
			m.setSourceError(errorOverview, msg.err)
			return m, next()
		}
		m.clearSourceError(errorOverview)
		return m, next()
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
			m.groupsVersion++
			m.invalidateGroupViews()
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
			m.connectionsVersion++
			m.invalidateConnectionViews()
			m.restoreConnectionSelection(selected)
			m.status.ConnectionCount = len(msg.connections)
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
		if !m.quitPending && msg.privileged && !msg.interactive && isAuthorizationRequired(msg.err) {
			if m.authorizing {
				return m, nil
			}
			m.authorizing = true
			return m, m.retryPrivilegedOperation(msg)
		}
		runtimeMutation := m.runtimeMutation
		coreMutation := m.coreMutation || msg.coreMutation
		m.runtimeMutation = false
		m.coreMutation = false
		m.loading = false
		m.privilegedOperation = false
		m.authorizing = false
		m.confirm = confirmNone
		m.confirmTarget = confirmTarget{}
		warning := ""
		if isOperationWarning(msg.err) {
			warning = msg.err.Error()
			msg.err = nil
		}
		if coreMutation {
			m.resetTrafficEpoch()
		}
		if msg.err != nil {
			m.setSourceError(errorOperation, msg.err)
			if m.quitPending {
				return m, tea.Quit
			}
			if runtimeMutation {
				m.runtimeRecoveryUntil = time.Now().Add(runtimeRecoveryGrace)
			}
			return m, nil
		}
		if msg.configKey == string(settingMixedPort) {
			if port, err := strconv.Atoi(msg.configValue); err == nil && port >= 1 && port <= 65535 {
				m.status.ConfigAvailable = true
				m.status.MixedPort = port
			}
		}
		if msg.configKey == string(settingLogLevel) {
			if level, ok := normalizeLogLevel(msg.configValue); ok {
				m.logLevel = level
			}
		}
		m.clearSourceError(errorOperation)
		toast := msg.message
		if warning != "" {
			toast += "；" + warning
		}
		m.showToast(toast, warning != "", time.Now())
		if m.quitPending {
			return m, tea.Quit
		}
		now := time.Now()
		if runtimeMutation {
			m.runtimeRecoveryUntil = now.Add(runtimeRecoveryGrace)
		}
		// A request started before this mutation describes the old runtime.
		// Ignore it while the forced post-mutation refresh is queued.
		m.statusSnapshotFloor = now
		m.invalidatePageSnapshot(m.page)
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
			if m.quitPending {
				return m, tea.Quit
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
		if m.quitPending {
			return m, tea.Quit
		}
		return m, nil
	case logChannelsMsg:
		if msg.generation != m.logGeneration {
			return m, nil
		}
		m.logConnecting = false
		m.logCh, m.logErrCh, m.logDone = msg.entries, msg.errs, msg.done
		m.logRetry = 0
		m.clearSourceError(errorLogs)
		if m.logCh == nil && m.logErrCh == nil {
			return m, m.disconnectLogs(msg.generation, fmt.Errorf("日志流未返回数据通道"))
		}
		var entryCmd, errorCmd tea.Cmd
		if m.logCh != nil {
			entryCmd = waitLogBatch(m.logCh, msg.generation, m.logDone)
		}
		if m.logErrCh != nil {
			errorCmd = waitLogError(m.logErrCh, msg.generation, m.logDone)
		}
		return m, tea.Batch(entryCmd, errorCmd)
	case logBatchMsg:
		if msg.generation != m.logGeneration {
			return m, nil
		}
		if len(msg.entries) > 0 {
			m.appendLogBatch(msg.entries)
			m.logRetry = 0
			m.clearSourceError(errorLogs)
		}
		if msg.closed {
			m.logCh = nil
			if m.logErrCh == nil {
				return m, m.disconnectLogs(msg.generation, nil)
			}
			return m, nil
		}
		if m.logCh != nil {
			return m, waitLogBatch(m.logCh, msg.generation, m.logDone)
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
		m.trafficCh, m.trafficErrCh, m.trafficDone = msg.traffic, msg.errs, msg.done
		if m.trafficCh == nil && m.trafficErrCh == nil {
			return m, m.disconnectTraffic(msg.generation, fmt.Errorf("流量流未返回数据通道"))
		}
		var trafficCmd, errorCmd tea.Cmd
		if m.trafficCh != nil {
			trafficCmd = waitTraffic(m.trafficCh, msg.generation, m.trafficDone)
		}
		if m.trafficErrCh != nil {
			errorCmd = waitTrafficError(m.trafficErrCh, msg.generation, m.trafficDone)
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
				return m, waitTraffic(m.trafficCh, msg.generation, m.trafficDone)
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
		if !m.status.Service.Active {
			return m, nil
		}
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
	if m.privilegedOperation {
		switch key {
		case "ctrl+c", "q":
			return m, m.requestQuit()
		default:
			return m, nil
		}
	}
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
		return m, m.requestQuit()
	case "esc":
		m.clearForegroundErrors()
		return m, nil
	case "?":
		m.help = true
		return m, nil
	case "tab", "l":
		m.applyFilter("")
		return m.switchPage((m.page + 1) % page(len(pageNames)))
	case "shift+tab", "h":
		m.applyFilter("")
		return m.switchPage((m.page + page(len(pageNames)) - 1) % page(len(pageNames)))
	case "right":
		if m.page == pageProxies {
			m.moveGroup(1)
			return m, nil
		}
		m.applyFilter("")
		return m.switchPage((m.page + 1) % page(len(pageNames)))
	case "left":
		if m.page == pageProxies {
			m.moveGroup(-1)
			return m, nil
		}
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
		groups := m.currentGroupViews()
		if m.page == pageProxies && len(groups) > 0 {
			group := m.groups[groups[clamp(m.groupCursor, 0, len(groups)-1)].groupIndex].Name
			return m, m.beginGroupTest(group)
		}
	case "a":
		if m.page == pageProfiles {
			return m, m.focusInput(inputProfile, "订阅 URL、本地路径或节点 URI", "")
		}
	case "u":
		if m.page == pageProfiles && len(m.profiles) > 0 {
			target := m.profiles[clamp(m.profileCursor, 0, len(m.profiles)-1)]
			name := target.Name
			begin := m.beginPrivilegedMetadataOperation
			if target.Active {
				begin = m.beginPrivilegedCoreOperation
			}
			return m, begin("更新配置 "+name, "配置已更新", func(ctx context.Context) error {
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
			connections := m.currentConnectionViews()
			if len(connections) > 0 {
				connection := m.connections[connections[clamp(m.connectionCursor, 0, len(connections)-1)].connectionIndex]
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

func (m *Model) requestQuit() tea.Cmd {
	m.cancel()
	if m.loading {
		m.quitPending = true
		return nil
	}
	return tea.Quit
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
				return m, m.beginPrivilegedConfigOperation("修改混合端口", "混合端口已更新", string(settingMixedPort), normalized)
			}
			m.resetInput()
			if value == "" {
				return m, nil
			}
			begin := m.beginPrivilegedMetadataOperation
			if len(m.profiles) == 0 {
				begin = m.beginPrivilegedCoreOperation
			}
			return m, begin("添加配置", "配置已添加，请选中后按 Enter 激活", func(ctx context.Context) error {
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
	m.syncInputWidth()
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
			return m, m.beginPrivilegedCoreOperation("修改 TUN 设置", "TUN 设置已更新", func(ctx context.Context) error {
				return m.backend.SetTUN(ctx, enabled)
			})
		case pickerAllowLAN:
			if value == "on" {
				m.confirm = confirmEnableLAN
				return m, nil
			}
			return m, m.beginPrivilegedConfigOperation("关闭局域网访问", "局域网访问已关闭", string(settingAllowLAN), value)
		case pickerIPv6:
			return m, m.beginPrivilegedConfigOperation("修改 IPv6 设置", "IPv6 设置已更新", string(settingIPv6), value)
		case pickerLogLevel:
			return m, m.beginPrivilegedConfigOperation("修改日志级别", "日志级别已更新", string(settingLogLevel), value)
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

func serviceStatusAvailable(service domain.ServiceStatus) bool {
	return service.State != "" || service.Active || service.Enabled || service.PID > 0
}

func (m Model) serviceStatusKnown() bool {
	return serviceStatusAvailable(m.status.Service)
}

func (m *Model) showUnknownServiceWarning() {
	m.showToast("服务状态未知，请先按 r 刷新后重试", true, time.Now())
}

func (m *Model) resetTrafficEpoch() {
	m.trafficHistory = nil
	m.lastTrafficAt = time.Time{}
	m.status.Traffic = domain.Traffic{}
}

func mergePartialStatus(target *domain.RuntimeStatus, source domain.RuntimeStatus) {
	serviceKnown := serviceStatusAvailable(source.Service)
	if serviceKnown {
		target.Service = source.Service
		target.ActiveProfile = source.ActiveProfile
	}
	if source.ActiveProfile != "" {
		target.ActiveProfile = source.ActiveProfile
	}
	if source.CoreVersion != "" {
		target.CoreVersion = source.CoreVersion
	}
	if source.ConfigAvailable {
		mergeConfigStatus(target, source)
	}
}

func (m *Model) resetInactiveRuntime() {
	cancelRequest(&m.overviewRequest)
	cancelRequest(&m.groupRequest)
	cancelRequest(&m.connectionRequest)
	m.trafficHistory = nil
	m.lastTrafficAt = time.Time{}
	m.status.Memory = 0
	m.status.ConnectionCount = 0
	m.status.Traffic = domain.Traffic{}
	if len(m.groups) > 0 {
		m.groups = nil
		m.groupsVersion++
		m.invalidateGroupViews()
	}
	m.proxyTestStates = make(map[string]map[string]proxyTestResult)
	if len(m.connections) > 0 {
		m.connections = nil
		m.connectionsVersion++
		m.invalidateConnectionViews()
	}
	m.clearSourceError(errorTraffic)
	m.clearSourceError(errorLogs)
	m.clearSourceError(errorOverview)
	m.clearSourceError(errorGroups)
	m.clearSourceError(errorConnections)
	if m.trafficStreamPresent() {
		m.stopTraffic()
	}
	if m.logStreamPresent() {
		m.stopLogs()
	}
}

func (m Model) inputFieldWidth() int {
	return max(12, min(64, m.width-18))
}

func (m *Model) syncInputWidth() {
	m.input.SetWidth(max(1, m.inputFieldWidth()-2))
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
		if !m.serviceStatusKnown() {
			m.showUnknownServiceWarning()
			return m, nil
		}
		if m.status.Service.Active {
			m.confirm = confirmStopService
			m.confirmTarget = confirmTarget{label: "Mihomo 服务"}
			return m, nil
		}
		return m, m.beginPrivilegedCoreOperation("启动 Mihomo 服务", "Mihomo 已启动", func(ctx context.Context) error {
			return m.backend.Service(ctx, "start")
		})
	case pageProxies:
		groups := m.currentGroupViews()
		if len(groups) == 0 {
			return m, nil
		}
		groupView := groups[min(m.groupCursor, len(groups)-1)]
		group := m.groups[groupView.groupIndex]
		nodes := groupView.proxies
		if len(nodes) == 0 {
			return m, nil
		}
		node := group.Proxies[nodes[min(m.proxyCursor, len(nodes)-1)].proxyIndex].Name
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
			return m, m.beginPrivilegedCoreOperation("激活配置 "+name, "已启用 "+name, func(ctx context.Context) error {
				return m.backend.UseProfile(ctx, name)
			})
		}
	case pageConnections:
		connections := m.currentConnectionViews()
		if len(connections) > 0 {
			connection := m.connections[connections[clamp(m.connectionCursor, 0, len(connections)-1)].connectionIndex]
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
		if !m.serviceStatusKnown() {
			m.showUnknownServiceWarning()
			return m, nil
		}
		action, message := "start", "Mihomo 已启动"
		if m.status.Service.Active {
			m.confirm = confirmStopService
			m.confirmTarget = confirmTarget{label: "Mihomo 服务"}
			return m, nil
		}
		return m, m.beginPrivilegedCoreOperation("启动 Mihomo 服务", message, func(ctx context.Context) error { return m.backend.Service(ctx, action) })
	case settingStartup:
		if !m.serviceStatusKnown() {
			m.showUnknownServiceWarning()
			return m, nil
		}
		action := "enable"
		message := "开机启动已启用"
		if m.status.Service.Enabled {
			action, message = "disable", "开机启动已关闭"
		}
		privilegeAction := "启用开机启动"
		if action == "disable" {
			privilegeAction = "关闭开机启动"
		}
		return m, m.beginPrivilegedMetadataOperation(privilegeAction, message, func(ctx context.Context) error { return m.backend.Service(ctx, action) })
	case settingMode:
		next := domain.ModeRule
		if m.status.Mode == domain.ModeRule {
			next = domain.ModeGlobal
		} else if m.status.Mode == domain.ModeGlobal {
			next = domain.ModeDirect
		}
		return m, m.beginPrivilegedCoreOperation("切换运行模式", "运行模式已切换", func(ctx context.Context) error { return m.backend.SetMode(ctx, next) })
	case settingTUN:
		if !m.status.ConfigAvailable {
			m.openPicker(pickerTUN, "")
			return m, nil
		}
		return m, m.beginPrivilegedCoreOperation("修改 TUN 设置", "TUN 设置已更新", func(ctx context.Context) error { return m.backend.SetTUN(ctx, !m.status.TUN) })
	case settingSchedule:
		enabled := true
		if m.scheduleOK {
			enabled = !m.schedule.Enabled
		}
		return m, m.beginPrivilegedMetadataOperation("修改定时更新设置", "定时更新设置已更新", func(ctx context.Context) error { return m.backend.SetSchedule(ctx, enabled) })
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
			return m, m.beginPrivilegedConfigOperation("关闭局域网访问", "局域网访问已关闭", string(settingAllowLAN), "off")
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
		return m, m.beginPrivilegedConfigOperation("修改 IPv6 设置", "IPv6 设置已更新", string(settingIPv6), value)
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
		return m, m.beginPrivilegedMetadataOperation("删除配置 "+target.name, "配置已删除", func(ctx context.Context) error { return m.backend.RemoveProfile(ctx, target.name) })
	case confirmCloseConnection:
		if target.id == "" {
			return m, nil
		}
		return m, m.beginOperation("连接已关闭", func(ctx context.Context) error { return m.backend.CloseConnection(ctx, target.id) })
	case confirmCloseAll:
		return m, m.beginOperation("全部连接已关闭", m.backend.CloseAllConnections)
	case confirmStopService:
		return m, m.beginPrivilegedCoreOperation("停止 Mihomo 服务", "Mihomo 已停止", func(ctx context.Context) error { return m.backend.Service(ctx, "stop") })
	case confirmEnableLAN:
		return m, m.beginPrivilegedConfigOperation("开启局域网访问", "局域网访问已开启", string(settingAllowLAN), "on")
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
	m.appendLogBatch([]domain.LogEntry{entry})
}

func (m *Model) appendLogBatch(entries []domain.LogEntry) {
	if len(entries) == 0 {
		return
	}
	visibleAdded := 0
	needle := strings.ToLower(m.filter)
	maintainSearch := needle != "" || len(m.logSearch) > 0
	if maintainSearch {
		m.ensureLogSearch()
	}
	for _, entry := range entries {
		search := ""
		if maintainSearch {
			search = logSearchText(entry)
		}
		if needle == "" || strings.Contains(search, needle) {
			visibleAdded++
		}
		if len(m.logs) < logBufferMaximum {
			m.logs = append(m.logs, entry)
			if maintainSearch {
				m.logSearch = append(m.logSearch, search)
			}
			continue
		}
		if m.logStart < 0 || m.logStart >= len(m.logs) {
			m.logStart = 0
		}
		m.logs[m.logStart] = entry
		if maintainSearch {
			m.logSearch[m.logStart] = search
		}
		m.logStart = (m.logStart + 1) % len(m.logs)
	}
	if m.logPaused {
		m.logUnread += len(entries)
	}
	if (m.logPaused || m.logOffset > 0) && visibleAdded > 0 {
		m.logOffset += visibleAdded
	}
	m.logVersion++
	m.invalidateLogView()
	m.ensureLogView()
	m.logOffset = clamp(m.logOffset, 0, max(0, m.logViewLen()-m.logCapacity()))
}

func logSearchText(entry domain.LogEntry) string {
	return strings.ToLower(entry.Time + "\x00" + entry.Level + "\x00" + entry.Message)
}

func (m *Model) ensureLogSearch() {
	if len(m.logSearch) == len(m.logs) {
		return
	}
	if cap(m.logSearch) < len(m.logs) {
		m.logSearch = make([]string, len(m.logs))
	} else {
		m.logSearch = m.logSearch[:len(m.logs)]
	}
	for index, entry := range m.logs {
		m.logSearch[index] = logSearchText(entry)
	}
}

func (m *Model) invalidateLogView() {
	m.logViewVersion = 0
}

func (m *Model) ensureLogView() {
	if len(m.logs) == 0 {
		m.logStart = 0
	}
	if m.logStart < 0 || m.logStart >= max(1, len(m.logs)) {
		m.logStart = 0
	}
	if m.logViewVersion == m.logVersion && m.logViewFilter == m.filter && m.logViewStart == m.logStart && m.logViewLength == len(m.logs) {
		return
	}
	m.logFilteredIndices = m.logFilteredIndices[:0]
	if m.filter != "" {
		m.ensureLogSearch()
		needle := strings.ToLower(m.filter)
		for logical := range len(m.logs) {
			physical := m.logPhysicalIndex(logical)
			if strings.Contains(m.logSearch[physical], needle) {
				m.logFilteredIndices = append(m.logFilteredIndices, physical)
			}
		}
	}
	m.logViewVersion = m.logVersion
	m.logViewFilter = m.filter
	m.logViewStart = m.logStart
	m.logViewLength = len(m.logs)
}

func (m Model) logPhysicalIndex(logical int) int {
	if len(m.logs) == 0 {
		return 0
	}
	start := m.logStart
	if start < 0 || start >= len(m.logs) {
		start = 0
	}
	return (start + clamp(logical, 0, len(m.logs)-1)) % len(m.logs)
}

func (m *Model) logViewLen() int {
	m.ensureLogView()
	if m.filter == "" {
		return len(m.logs)
	}
	return len(m.logFilteredIndices)
}

func (m *Model) logViewEntry(index int) domain.LogEntry {
	m.ensureLogView()
	if m.filter == "" {
		return m.logs[m.logPhysicalIndex(index)]
	}
	return m.logs[m.logFilteredIndices[clamp(index, 0, len(m.logFilteredIndices)-1)]]
}

func (m Model) logCapacity() int {
	return max(1, max(8, m.height-5)-4)
}

func (m *Model) scrollLogs(delta int) {
	maximum := max(0, m.logViewLen()-m.logCapacity())
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
			m.logOffset = max(0, m.logViewLen()-m.logCapacity())
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
	if m.page == pageProxies {
		capacity = m.proxyListCapacity()
	} else if m.page == pageSettings {
		capacity = m.settingsCapacity()
	}
	m.moveCursor(direction * capacity)
}

func (m *Model) moveCursorBoundary(end bool) {
	last := 0
	switch m.page {
	case pageProxies:
		groups := m.currentGroupViews()
		if len(groups) > 0 {
			last = max(0, len(groups[clamp(m.groupCursor, 0, len(groups)-1)].proxies)-1)
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
		last = max(0, len(m.currentConnectionViews())-1)
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
		groups := m.currentGroupViews()
		if len(groups) == 0 {
			return
		}
		group := groups[clamp(m.groupCursor, 0, len(groups)-1)]
		m.proxyCursor = clamp(m.proxyCursor+delta, 0, max(0, len(group.proxies)-1))
	case pageProfiles:
		m.profileCursor = clamp(m.profileCursor+delta, 0, max(0, len(m.profiles)-1))
	case pageConnections:
		m.connectionCursor = clamp(m.connectionCursor+delta, 0, max(0, len(m.currentConnectionViews())-1))
	case pageSettings:
		m.settingCursor = clamp(m.settingCursor+delta, 0, max(0, len(settingOrder)-1))
	}
}

func (m *Model) moveGroup(delta int) {
	groups := m.currentGroupViews()
	if len(groups) == 0 {
		return
	}
	m.groupCursor = (m.groupCursor + delta + len(groups)) % len(groups)
	m.proxyCursor = 0
	m.proxyOffset = 0
	m.syncViewports()
}

func (m *Model) clampCursors() {
	groups := m.currentGroupViews()
	m.groupCursor = clamp(m.groupCursor, 0, max(0, len(groups)-1))
	if len(groups) > 0 {
		m.proxyCursor = clamp(m.proxyCursor, 0, max(0, len(groups[m.groupCursor].proxies)-1))
	} else {
		m.proxyCursor = 0
	}
	m.profileCursor = clamp(m.profileCursor, 0, max(0, len(m.profiles)-1))
	m.connectionCursor = clamp(m.connectionCursor, 0, max(0, len(m.currentConnectionViews())-1))
	m.settingCursor = clamp(m.settingCursor, 0, max(0, len(settingOrder)-1))
	m.syncViewports()
}

func (m *Model) selectedProxyKeys() (string, string) {
	groups := m.currentGroupViews()
	if len(groups) == 0 {
		return "", ""
	}
	groupView := groups[clamp(m.groupCursor, 0, len(groups)-1)]
	group := m.groups[groupView.groupIndex]
	proxies := groupView.proxies
	if len(proxies) == 0 {
		return group.Name, ""
	}
	return group.Name, group.Proxies[proxies[clamp(m.proxyCursor, 0, len(proxies)-1)].proxyIndex].Name
}

func (m *Model) restoreProxySelection(groupName, proxyName string) {
	m.groupCursor, m.proxyCursor = 0, 0
	groups := m.currentGroupViews()
	for groupIndex, groupView := range groups {
		group := m.groups[groupView.groupIndex]
		if group.Name != groupName {
			continue
		}
		m.groupCursor = groupIndex
		for proxyIndex, proxyView := range groupView.proxies {
			proxy := group.Proxies[proxyView.proxyIndex]
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

func (m *Model) selectedConnectionKey() string {
	connections := m.currentConnectionViews()
	if len(connections) == 0 {
		return ""
	}
	return connectionKey(m.connections[connections[clamp(m.connectionCursor, 0, len(connections)-1)].connectionIndex])
}

func (m *Model) restoreConnectionSelection(selected string) {
	if selected != "" {
		for index, view := range m.currentConnectionViews() {
			connection := m.connections[view.connectionIndex]
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
		groups := m.currentGroupViews()
		if len(groups) > 0 {
			groupView := groups[clamp(m.groupCursor, 0, len(groups)-1)]
			group := m.groups[groupView.groupIndex]
			selectedGroup = group.Name
			proxies := groupView.proxies
			if len(proxies) > 0 {
				selectedProxy = group.Proxies[proxies[clamp(m.proxyCursor, 0, len(proxies)-1)].proxyIndex].Name
			}
		}
	} else if m.page == pageConnections {
		connections := m.currentConnectionViews()
		if len(connections) > 0 {
			selectedConnection = connectionKey(m.connections[connections[clamp(m.connectionCursor, 0, len(connections)-1)].connectionIndex])
		}
	}

	m.filter = value
	if value == "" {
		m.logSearch = nil
	}
	m.invalidateGroupViews()
	m.invalidateConnectionViews()
	m.invalidateLogView()
	if m.page == pageLogs {
		m.ensureLogView()
		m.logOffset = 0
		m.logUnread = 0
	}
	if m.page == pageProxies {
		m.groupCursor, m.proxyCursor = 0, 0
		groups := m.currentGroupViews()
		for groupIndex := range groups {
			group := m.groups[groups[groupIndex].groupIndex]
			if group.Name != selectedGroup {
				continue
			}
			m.groupCursor = groupIndex
			for proxyIndex, proxyView := range groups[groupIndex].proxies {
				proxy := group.Proxies[proxyView.proxyIndex]
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
			for connectionIndex, view := range m.currentConnectionViews() {
				connection := m.connections[view.connectionIndex]
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

func (m Model) proxyListCapacity() int {
	return max(1, m.listCapacity()-1)
}

func (m Model) settingsCapacity() int {
	return max(1, max(8, m.height-6)-5)
}

func (m *Model) syncViewports() {
	groups := m.currentGroupViews()
	groupCapacity := proxyGroupSelectorLayout(max(1, m.width-4), len(groups)).capacity
	m.groupOffset = viewportOffset(len(groups), m.groupCursor, m.groupOffset, groupCapacity)
	nodeCount := 0
	if len(groups) > 0 {
		groupIndex := clamp(m.groupCursor, 0, len(groups)-1)
		nodeCount = len(groups[groupIndex].proxies)
	}
	m.proxyOffset = viewportOffset(nodeCount, m.proxyCursor, m.proxyOffset, m.proxyListCapacity())
	m.profileOffset = viewportOffset(len(m.profiles), m.profileCursor, m.profileOffset, m.listCapacity())
	m.connectionOffset = viewportOffset(len(m.currentConnectionViews()), m.connectionCursor, m.connectionOffset, m.listCapacity())
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
	groups := make(map[string]*domain.ProxyGroup, len(m.groups))
	for index := range m.groups {
		groups[m.groups[index].Name] = &m.groups[index]
	}
	for groupName, results := range m.proxyTestStates {
		group := groups[groupName]
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

func (m *Model) invalidateGroupViews() {
	m.groupViewValid = false
	if m.groupSearchVersion != m.groupsVersion {
		m.groupSearchValid = false
		for index := range m.groupSearch {
			clear(m.groupSearch[index].proxies)
			m.groupSearch[index] = proxyGroupSearch{}
		}
		if oversizedCapacity(cap(m.groupSearch), len(m.groups)) {
			m.groupSearch = nil
		} else {
			m.groupSearch = m.groupSearch[:0]
		}
	}
}

func (m *Model) ensureGroupViews() {
	if m.groupViewValid && m.groupViewVersion == m.groupsVersion && m.groupViewFilter == m.filter {
		return
	}
	needle := strings.ToLower(m.filter)
	if needle != "" {
		m.ensureGroupSearch()
	}
	clear(m.groupViews)
	m.groupViews = m.groupViews[:0]
	if len(m.groupProxyViewBuffers) != len(m.groups) {
		if oversizedCapacity(cap(m.groupProxyViewBuffers), len(m.groups)) {
			m.groupProxyViewBuffers = make([][]proxyView, len(m.groups))
		} else if cap(m.groupProxyViewBuffers) < len(m.groups) {
			m.groupProxyViewBuffers = make([][]proxyView, len(m.groups))
		} else {
			if len(m.groups) < len(m.groupProxyViewBuffers) {
				clear(m.groupProxyViewBuffers[len(m.groups):])
			}
			m.groupProxyViewBuffers = m.groupProxyViewBuffers[:len(m.groups)]
		}
	}
	for groupIndex := range m.groups {
		group := &m.groups[groupIndex]
		groupMatch := needle == "" || strings.Contains(m.groupSearch[groupIndex].name, needle)
		proxies := m.groupProxyViewBuffers[groupIndex]
		if oversizedCapacity(cap(proxies), len(group.Proxies)) {
			proxies = make([]proxyView, 0, len(group.Proxies))
		} else {
			proxies = proxies[:0]
		}
		for proxyIndex := range group.Proxies {
			if groupMatch || strings.Contains(m.groupSearch[groupIndex].proxies[proxyIndex], needle) {
				proxies = append(proxies, proxyView{proxyIndex: proxyIndex})
			}
		}
		m.groupProxyViewBuffers[groupIndex] = proxies
		if groupMatch || len(proxies) > 0 {
			m.groupViews = append(m.groupViews, proxyGroupView{groupIndex: groupIndex, proxies: proxies})
		}
	}
	m.groupViewVersion = m.groupsVersion
	m.groupViewFilter = m.filter
	m.groupViewValid = true
}

func (m *Model) ensureGroupSearch() {
	if m.groupSearchValid && m.groupSearchVersion == m.groupsVersion && len(m.groupSearch) == len(m.groups) {
		return
	}
	if oversizedCapacity(cap(m.groupSearch), len(m.groups)) || cap(m.groupSearch) < len(m.groups) {
		m.groupSearch = make([]proxyGroupSearch, len(m.groups))
	} else {
		if len(m.groups) < len(m.groupSearch) {
			clear(m.groupSearch[len(m.groups):])
		}
		m.groupSearch = m.groupSearch[:len(m.groups)]
	}
	for groupIndex := range m.groups {
		group := &m.groups[groupIndex]
		search := &m.groupSearch[groupIndex]
		search.name = strings.ToLower(group.Name)
		if oversizedCapacity(cap(search.proxies), len(group.Proxies)) || cap(search.proxies) < len(group.Proxies) {
			search.proxies = make([]string, len(group.Proxies))
		} else {
			if len(group.Proxies) < len(search.proxies) {
				clear(search.proxies[len(group.Proxies):])
			}
			search.proxies = search.proxies[:len(group.Proxies)]
		}
		for proxyIndex, proxy := range group.Proxies {
			search.proxies[proxyIndex] = strings.ToLower(proxy.Name + "\x00" + proxy.Type)
		}
	}
	m.groupSearchVersion = m.groupsVersion
	m.groupSearchValid = true
}

func (m *Model) currentGroupViews() []proxyGroupView {
	m.ensureGroupViews()
	return m.groupViews
}

func (m *Model) invalidateConnectionViews() {
	m.connectionViewValid = false
	if m.connectionSearchVersion != m.connectionsVersion {
		m.connectionSearchValid = false
		clear(m.connectionSearch)
		if oversizedCapacity(cap(m.connectionSearch), len(m.connections)) {
			m.connectionSearch = nil
		} else {
			m.connectionSearch = m.connectionSearch[:0]
		}
	}
}

func (m *Model) ensureConnectionViews() {
	if m.connectionViewValid && m.connectionViewVersion == m.connectionsVersion && m.connectionViewFilter == m.filter {
		return
	}
	needle := strings.ToLower(m.filter)
	if needle != "" {
		m.ensureConnectionSearch()
	}
	m.connectionViews = m.connectionViews[:0]
	for index := range m.connections {
		if needle == "" || strings.Contains(m.connectionSearch[index], needle) {
			m.connectionViews = append(m.connectionViews, connectionView{connectionIndex: index})
		}
	}
	m.connectionViewVersion = m.connectionsVersion
	m.connectionViewFilter = m.filter
	m.connectionViewValid = true
}

func (m *Model) ensureConnectionSearch() {
	if m.connectionSearchValid && m.connectionSearchVersion == m.connectionsVersion && len(m.connectionSearch) == len(m.connections) {
		return
	}
	if oversizedCapacity(cap(m.connectionSearch), len(m.connections)) || cap(m.connectionSearch) < len(m.connections) {
		m.connectionSearch = make([]string, len(m.connections))
	} else {
		if len(m.connections) < len(m.connectionSearch) {
			clear(m.connectionSearch[len(m.connections):])
		}
		m.connectionSearch = m.connectionSearch[:len(m.connections)]
	}
	for index, connection := range m.connections {
		m.connectionSearch[index] = strings.ToLower(connection.Host + "\x00" + connection.Process + "\x00" + connection.Destination + "\x00" + connection.Rule)
	}
	m.connectionSearchVersion = m.connectionsVersion
	m.connectionSearchValid = true
}

func oversizedCapacity(capacity, length int) bool {
	return capacity > 256 && capacity > max(1, length)*4
}

func (m *Model) currentConnectionViews() []connectionView {
	m.ensureConnectionViews()
	return m.connectionViews
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
