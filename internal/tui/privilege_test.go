package tui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"mihomoctl/internal/domain"
	"mihomoctl/internal/platform"
)

type elevationTestBackend struct {
	*fakeBackend
	needsElevation bool
}

func (b *elevationTestBackend) NeedsElevation() bool { return b.needsElevation }

type authorizationRequiredTestError struct{}

func (authorizationRequiredTestError) Error() string               { return "authorization required" }
func (authorizationRequiredTestError) AuthorizationRequired() bool { return true }

func elevatedTestModel() Model {
	m := testModel()
	m.backend = &elevationTestBackend{fakeBackend: m.backend.(*fakeBackend), needsElevation: true}
	return m
}

func TestPrivilegedOperationUsesNonInteractiveFastPath(t *testing.T) {
	m := elevatedTestModel()
	calls := 0
	var received context.Context
	cmd := m.beginPrivilegedOperation("启动 Mihomo 服务", "Mihomo 已启动", func(ctx context.Context) error {
		calls++
		received = ctx
		return nil
	})
	if cmd == nil || !m.loading || !m.privilegedOperation || m.authorizing {
		t.Fatalf("initial privilege state = loading:%v privileged:%v authorizing:%v", m.loading, m.privilegedOperation, m.authorizing)
	}
	raw := cmd()
	msg, ok := raw.(operationMsg)
	if !ok {
		t.Fatalf("command returned %T, want operationMsg", raw)
	}
	if calls != 1 || !platform.NonInteractiveElevation(received) || platform.InteractiveElevation(received) {
		t.Fatalf("fast path calls=%d noninteractive=%v interactive=%v", calls, platform.NonInteractiveElevation(received), platform.InteractiveElevation(received))
	}
	if !msg.privileged || msg.interactive || msg.retry == nil || msg.action != "启动 Mihomo 服务" {
		t.Fatalf("unexpected operation message: %#v", msg)
	}

	m, _ = updateUIModel(t, m, msg)
	if m.loading || m.privilegedOperation || m.authorizing || m.toast != "Mihomo 已启动" {
		t.Fatalf("completed privilege state = loading:%v privileged:%v authorizing:%v toast:%q", m.loading, m.privilegedOperation, m.authorizing, m.toast)
	}
}

func TestAuthorizationRequiredSchedulesOnlyOneInteractiveRetry(t *testing.T) {
	m := elevatedTestModel()
	calls := 0
	cmd := m.beginPrivilegedOperation("修改 TUN 设置", "TUN 设置已更新", func(ctx context.Context) error {
		calls++
		if !platform.NonInteractiveElevation(ctx) {
			t.Fatal("initial attempt was allowed to prompt")
		}
		return authorizationRequiredTestError{}
	})
	msg := cmd().(operationMsg)
	if calls != 1 {
		t.Fatalf("initial calls = %d, want 1", calls)
	}

	var retry tea.Cmd
	m, retry = updateUIModel(t, m, msg)
	if retry == nil || !m.loading || !m.privilegedOperation || !m.authorizing {
		t.Fatalf("authorization state = retry:%v loading:%v privileged:%v authorizing:%v", retry != nil, m.loading, m.privilegedOperation, m.authorizing)
	}
	m, retry = updateUIModel(t, m, msg)
	if retry != nil || calls != 1 {
		t.Fatalf("duplicate response scheduled another retry: retry=%v calls=%d", retry != nil, calls)
	}
}

func TestAuthorizationFailureCompletesTwoStageFlowWithoutThirdAttempt(t *testing.T) {
	previousExec := execInteractiveOperation
	execInteractiveOperation = func(command tea.ExecCommand, callback tea.ExecCallback) tea.Cmd {
		command.SetStdin(strings.NewReader("terminal input"))
		command.SetStdout(io.Discard)
		command.SetStderr(io.Discard)
		return func() tea.Msg { return callback(command.Run()) }
	}
	t.Cleanup(func() { execInteractiveOperation = previousExec })

	m := elevatedTestModel()
	m.page = pageProfiles
	m.profileCursor = 0
	m.filter = "keep"
	calls := 0
	initial := m.beginPrivilegedOperation("更新配置 日常", "配置已更新", func(ctx context.Context) error {
		calls++
		switch calls {
		case 1:
			if !platform.NonInteractiveElevation(ctx) {
				t.Fatal("first attempt was allowed to prompt")
			}
			return authorizationRequiredTestError{}
		case 2:
			if !platform.InteractiveElevation(ctx) {
				t.Fatal("second attempt was not interactive")
			}
			return errors.New("sudo 身份验证未完成，操作未执行")
		default:
			t.Fatalf("unexpected privilege attempt %d", calls)
			return nil
		}
	})

	first := initial().(operationMsg)
	var interactive tea.Cmd
	m, interactive = updateUIModel(t, m, first)
	if interactive == nil || !m.authorizing {
		t.Fatal("authorization request did not schedule the interactive attempt")
	}
	second, ok := interactive().(operationMsg)
	if !ok {
		t.Fatalf("interactive command returned an unexpected message")
	}
	var third tea.Cmd
	m, third = updateUIModel(t, m, second)
	if calls != 2 || third != nil {
		t.Fatalf("attempts=%d third retry=%v, want exactly two attempts", calls, third != nil)
	}
	if m.loading || m.privilegedOperation || m.authorizing || m.err != "sudo 身份验证未完成，操作未执行" {
		t.Fatalf("final state = loading:%v privileged:%v authorizing:%v error:%q", m.loading, m.privilegedOperation, m.authorizing, m.err)
	}
	if m.page != pageProfiles || m.profileCursor != 0 || m.filter != "keep" {
		t.Fatalf("authorization failure changed the current view")
	}
}

func TestInteractiveOperationCommandForwardsTerminalAndExplainsAction(t *testing.T) {
	var stdin bytes.Buffer
	var stdout, stderr bytes.Buffer
	stdin.WriteString("terminal input")
	calls := 0
	command := &interactiveOperationCommand{
		ctx:    context.Background(),
		action: "\x1b[31m修改\x00端口",
		run: func(ctx context.Context) error {
			calls++
			if !platform.InteractiveElevation(ctx) || platform.NonInteractiveElevation(ctx) {
				t.Fatal("retry context is not interactive")
			}
			gotStdin, ok := platform.ElevationInput(ctx)
			if !ok || gotStdin != io.Reader(&stdin) {
				t.Fatal("Bubble Tea terminal input was not forwarded")
			}
			return nil
		},
	}
	command.SetStdin(&stdin)
	command.SetStdout(&stdout)
	command.SetStderr(&stderr)
	if err := command.Run(); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !command.started {
		t.Fatalf("interactive calls=%d started=%v", calls, command.started)
	}
	prompt := stdout.String()
	if !strings.Contains(prompt, "修改 端口") || !strings.Contains(prompt, "sudo 验证") || strings.Contains(prompt, "\x1b") || strings.Contains(prompt, "\x00") {
		t.Fatalf("unsafe or incomplete authorization prompt: %q", prompt)
	}
	if width := ansi.StringWidth(authorizationPrompt(strings.Repeat("很长的操作名称", 20))); width > 60 {
		t.Fatalf("authorization prompt width = %d, want at most 60", width)
	}
}

func TestInteractivePrivilegeFailurePreservesPageAndSelection(t *testing.T) {
	m := elevatedTestModel()
	m.page = pageProfiles
	m.profiles = append(m.profiles, m.profiles[0])
	m.profileCursor = 1
	m.filter = "keep"
	m.loading = true
	m.privilegedOperation = true
	m.authorizing = true
	m.mutationGeneration = 9

	var cmd tea.Cmd
	m, cmd = updateUIModel(t, m, operationMsg{
		message: "配置已更新", err: errors.New("sudo 身份验证未完成，操作未执行"),
		generation: 9, privileged: true, interactive: true,
	})
	if m.page != pageProfiles || m.profileCursor != 1 || m.filter != "keep" {
		t.Fatalf("failure changed view state: page=%v cursor=%d filter=%q", m.page, m.profileCursor, m.filter)
	}
	if m.loading || m.privilegedOperation || m.authorizing || m.err != "sudo 身份验证未完成，操作未执行" {
		t.Fatalf("failure state = loading:%v privileged:%v authorizing:%v error:%q", m.loading, m.privilegedOperation, m.authorizing, m.err)
	}
	if cmd != nil {
		t.Fatal("failed authorization scheduled a refresh that could hide its error")
	}
}

func TestInteractiveConfigSuccessPreservesMetadata(t *testing.T) {
	m := elevatedTestModel()
	m.loading = true
	m.privilegedOperation = true
	m.authorizing = true
	m.mutationGeneration = 3
	m.logLevel = "info"

	m, _ = updateUIModel(t, m, operationMsg{
		message: "日志级别已更新", generation: 3, privileged: true, interactive: true,
		configKey: string(settingLogLevel), configValue: "debug",
	})
	if m.logLevel != "debug" || m.toast != "日志级别已更新" || m.loading || m.privilegedOperation || m.authorizing {
		t.Fatalf("config completion state = level:%q toast:%q loading:%v privileged:%v authorizing:%v", m.logLevel, m.toast, m.loading, m.privilegedOperation, m.authorizing)
	}
}

func TestMixedPortSuccessUpdatesStatusBeforeRefresh(t *testing.T) {
	m := elevatedTestModel()
	m.status.ConfigAvailable = false
	m.status.MixedPort = 0
	m.loading = true
	m.privilegedOperation = true
	m.authorizing = true
	m.mutationGeneration = 4

	m, _ = updateUIModel(t, m, operationMsg{
		message: "混合端口已更新", generation: 4, privileged: true, interactive: true,
		configKey: string(settingMixedPort), configValue: "7980",
	})
	if !m.status.ConfigAvailable || m.status.MixedPort != 7980 {
		t.Fatalf("mixed port status = available:%v port:%d", m.status.ConfigAvailable, m.status.MixedPort)
	}
}

func TestRuntimeMutationSuppressesExpectedControllerDisconnects(t *testing.T) {
	m := elevatedTestModel()
	m.page = pageLogs
	m.status = domain.RuntimeStatus{
		Service:     domain.ServiceStatus{Active: true, State: "running"},
		CoreVersion: "v1.19.30",
	}
	trafficCtx, cancelTraffic := context.WithCancel(context.Background())
	logCtx, cancelLogs := context.WithCancel(context.Background())
	m.trafficCancel = cancelTraffic
	m.trafficCh = make(chan domain.Traffic)
	m.logCancel = cancelLogs
	m.logCh = make(chan domain.LogEntry)
	m.setSourceError(errorTraffic, errors.New("旧流量错误"))
	m.setSourceError(errorLogs, errors.New("旧日志错误"))

	cmd := m.beginPrivilegedConfigOperation("修改混合端口", "混合端口已更新", string(settingMixedPort), "7980")
	if cmd == nil || !m.runtimeMutation || m.trafficStreamPresent() || m.logStreamPresent() {
		t.Fatalf("runtime mutation state = cmd:%v mutation:%v traffic:%v logs:%v", cmd != nil, m.runtimeMutation, m.trafficStreamPresent(), m.logStreamPresent())
	}
	if _, exists := m.errors[errorTraffic]; exists {
		t.Fatal("runtime mutation retained the old traffic error")
	}
	if _, exists := m.errors[errorLogs]; exists {
		t.Fatal("runtime mutation retained the old log error")
	}
	for name, ctx := range map[string]context.Context{"traffic": trafficCtx, "logs": logCtx} {
		select {
		case <-ctx.Done():
		default:
			t.Fatalf("%s stream was not canceled before the mutation", name)
		}
	}
	if refresh := m.beginStatusRefresh(time.Now(), true); refresh != nil {
		t.Fatal("status refresh started while the runtime was being changed")
	}

	operation := cmd().(operationMsg)
	m, _ = updateUIModel(t, m, operation)
	if m.runtimeMutation || m.runtimeRecoveryUntil.IsZero() {
		t.Fatalf("post-mutation recovery state = mutation:%v until:%v", m.runtimeMutation, m.runtimeRecoveryUntil)
	}
	statusGeneration := m.statusRequest.generation
	requestedAt := m.statusSnapshotFloor.Add(time.Nanosecond)
	m, _ = updateUIModel(t, m, statusMsg{
		status: domain.RuntimeStatus{
			Service:     domain.ServiceStatus{Active: true, State: "running"},
			CoreVersion: "控制器不可用",
		},
		err:         errors.New("Mihomo 控制器不可用: 流量流已断开"),
		generation:  statusGeneration,
		requestedAt: requestedAt,
	})
	if _, exists := m.errors[errorStatus]; exists {
		t.Fatalf("recovery status error was shown: %#v", m.errors[errorStatus])
	}
	if m.status.CoreVersion != "v1.19.30" {
		t.Fatalf("transient recovery replaced the core version with %q", m.status.CoreVersion)
	}

	m.trafficGeneration = 20
	m.trafficErrCh = make(chan error)
	m, reconnect := updateUIModel(t, m, trafficErrMsg{
		err: errors.New("流量流已断开"), generation: 20,
	})
	if reconnect == nil || !m.trafficReconnectPending {
		t.Fatal("suppressed stream error did not retain reconnect behavior")
	}
	if _, exists := m.errors[errorTraffic]; exists {
		t.Fatalf("recovery traffic error was shown: %#v", m.errors[errorTraffic])
	}

	m, _ = updateUIModel(t, m, statusMsg{
		status: domain.RuntimeStatus{
			Service:     domain.ServiceStatus{Active: true, State: "running"},
			CoreVersion: "v1.19.30",
		},
	})
	if !m.runtimeRecoveryUntil.IsZero() {
		t.Fatalf("successful status did not end recovery: %v", m.runtimeRecoveryUntil)
	}
	m.setSourceError(errorTraffic, errors.New("持续的流量错误"))
	if _, exists := m.errors[errorTraffic]; !exists {
		t.Fatal("runtime errors remained suppressed after recovery")
	}
}

func TestExpiredRuntimeRecoveryShowsPersistentError(t *testing.T) {
	m := testModel()
	m.runtimeRecoveryUntil = time.Now().Add(-time.Second)
	m.setSourceError(errorStatus, errors.New("Mihomo 控制器仍不可用"))
	if !strings.Contains(m.err, "仍不可用") {
		t.Fatalf("persistent controller error was hidden: %q", m.err)
	}
}

func TestProtectedAndAPIActionsUseSeparateOperationPaths(t *testing.T) {
	tests := []struct {
		name       string
		prepare    func(*Model)
		activate   func(Model) (tea.Model, tea.Cmd)
		privileged bool
	}{
		{
			name: "service start", privileged: true,
			prepare:  func(m *Model) { m.page = pageOverview; m.status.Service.Active = false },
			activate: func(m Model) (tea.Model, tea.Cmd) { return m.activate() },
		},
		{
			name: "service stop", privileged: true,
			prepare: func(m *Model) {
				m.confirm = confirmStopService
				m.confirmTarget = confirmTarget{label: "Mihomo 服务"}
			},
			activate: func(m Model) (tea.Model, tea.Cmd) { return m.runConfirmed() },
		},
		{
			name: "startup setting", privileged: true,
			prepare: func(m *Model) {
				m.page = pageSettings
				m.settingCursor = settingCursorFor(t, settingStartup)
				m.status.Service.Enabled = false
			},
			activate: func(m Model) (tea.Model, tea.Cmd) { return m.activateSetting() },
		},
		{
			name: "profile use", privileged: true,
			prepare:  func(m *Model) { m.page = pageProfiles; m.profiles[0].Active = false },
			activate: func(m Model) (tea.Model, tea.Cmd) { return m.activate() },
		},
		{
			name: "profile update", privileged: true,
			prepare:  func(m *Model) { m.page = pageProfiles },
			activate: func(m Model) (tea.Model, tea.Cmd) { return m.updateKey(keyPress('u', "u")) },
		},
		{
			name: "profile remove", privileged: true,
			prepare: func(m *Model) {
				m.confirm = confirmRemoveProfile
				m.confirmTarget = confirmTarget{name: "旧配置"}
			},
			activate: func(m Model) (tea.Model, tea.Cmd) { return m.runConfirmed() },
		},
		{
			name: "profile add", privileged: true,
			prepare: func(m *Model) {
				m.inMode = inputProfile
				m.input.SetValue("https://example.test/sub")
			},
			activate: func(m Model) (tea.Model, tea.Cmd) { return m.updateInput(keyPress(tea.KeyEnter, "")) },
		},
		{
			name: "mode change", privileged: true,
			prepare:  func(m *Model) { m.page = pageSettings; m.settingCursor = settingCursorFor(t, settingMode) },
			activate: func(m Model) (tea.Model, tea.Cmd) { return m.activateSetting() },
		},
		{
			name: "tun setting", privileged: true,
			prepare:  func(m *Model) { m.page = pageSettings; m.settingCursor = settingCursorFor(t, settingTUN) },
			activate: func(m Model) (tea.Model, tea.Cmd) { return m.activateSetting() },
		},
		{
			name: "tun picker", privileged: true,
			prepare: func(m *Model) {
				m.picker = pickerTUN
				m.pickerCursor = 1
			},
			activate: func(m Model) (tea.Model, tea.Cmd) { return m.updatePicker(keyPress(tea.KeyEnter, "")) },
		},
		{
			name: "schedule setting", privileged: true,
			prepare:  func(m *Model) { m.page = pageSettings; m.settingCursor = settingCursorFor(t, settingSchedule) },
			activate: func(m Model) (tea.Model, tea.Cmd) { return m.activateSetting() },
		},
		{
			name: "mixed port", privileged: true,
			prepare: func(m *Model) {
				m.inMode = inputMixedPort
				m.input.SetValue("7891")
			},
			activate: func(m Model) (tea.Model, tea.Cmd) { return m.updateInput(keyPress(tea.KeyEnter, "")) },
		},
		{
			name: "allow lan", privileged: true,
			prepare: func(m *Model) {
				m.confirm = confirmEnableLAN
				m.confirmTarget = confirmTarget{label: "局域网访问"}
			},
			activate: func(m Model) (tea.Model, tea.Cmd) { return m.runConfirmed() },
		},
		{
			name: "ipv6 setting", privileged: true,
			prepare:  func(m *Model) { m.page = pageSettings; m.settingCursor = settingCursorFor(t, settingIPv6) },
			activate: func(m Model) (tea.Model, tea.Cmd) { return m.activateSetting() },
		},
		{
			name: "log level picker", privileged: true,
			prepare: func(m *Model) {
				m.picker = pickerLogLevel
				m.pickerCursor = 2
			},
			activate: func(m Model) (tea.Model, tea.Cmd) { return m.updatePicker(keyPress(tea.KeyEnter, "")) },
		},
		{
			name: "proxy select", privileged: false,
			prepare:  func(m *Model) { m.page = pageProxies },
			activate: func(m Model) (tea.Model, tea.Cmd) { return m.activate() },
		},
		{
			name: "connection close", privileged: false,
			prepare: func(m *Model) {
				m.page = pageConnections
				m.confirm = confirmCloseConnection
				m.confirmTarget = confirmTarget{id: "1"}
			},
			activate: func(m Model) (tea.Model, tea.Cmd) { return m.runConfirmed() },
		},
		{
			name: "all connections close", privileged: false,
			prepare: func(m *Model) {
				m.confirm = confirmCloseAll
				m.confirmTarget = confirmTarget{label: "全部活动连接"}
			},
			activate: func(m Model) (tea.Model, tea.Cmd) { return m.runConfirmed() },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := elevatedTestModel()
			test.prepare(&m)
			_, cmd := test.activate(m)
			if cmd == nil {
				t.Fatal("action returned no command")
			}
			raw := cmd()
			msg, ok := raw.(operationMsg)
			if !ok {
				t.Fatalf("action returned %T, want operationMsg", raw)
			}
			if msg.privileged != test.privileged {
				t.Fatalf("privileged = %v, want %v", msg.privileged, test.privileged)
			}
		})
	}
}

func TestGroupTestNeverStartsPrivilegeFlow(t *testing.T) {
	m := elevatedTestModel()
	cmd := m.beginGroupTest("PROXY")
	if cmd == nil || m.privilegedOperation || m.authorizing {
		t.Fatalf("group test privilege state = privileged:%v authorizing:%v", m.privilegedOperation, m.authorizing)
	}
	if _, ok := cmd().(groupTestMsg); !ok {
		t.Fatal("group test did not return its normal result message")
	}
}
