package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"mihomoctl/internal/domain"
)

func settingCursorFor(t *testing.T, target settingID) int {
	t.Helper()
	for index, id := range settingOrder {
		if id == target {
			return index
		}
	}
	t.Fatalf("setting %q is not in settingOrder", target)
	return 0
}

func openSetting(t *testing.T, m Model, target settingID) (Model, tea.Cmd) {
	t.Helper()
	m.page = pageSettings
	m.settingCursor = settingCursorFor(t, target)
	return updateUIModel(t, m, keyPress(tea.KeyEnter, ""))
}

func TestMixedPortInputValidation(t *testing.T) {
	for _, value := range []string{"", "abc", "0", "65536"} {
		t.Run("invalid-"+value, func(t *testing.T) {
			m := testModel()
			backend := m.backend.(*fakeBackend)
			m, _ = openSetting(t, m, settingMixedPort)
			if m.inMode != inputMixedPort || m.input.Value() != "7890" {
				t.Fatalf("mixed-port input = mode %v value %q", m.inMode, m.input.Value())
			}
			m.input.SetValue(value)
			var cmd tea.Cmd
			m, cmd = updateUIModel(t, m, keyPress(tea.KeyEnter, ""))
			if cmd != nil || m.inMode != inputMixedPort || m.inputError == "" {
				t.Fatalf("invalid port closed or submitted input: cmd=%v mode=%v error=%q", cmd != nil, m.inMode, m.inputError)
			}
			if backend.configCalls != 0 {
				t.Fatalf("invalid port called backend %d times", backend.configCalls)
			}
			if view := ansi.Strip(m.render()); !strings.Contains(view, "1 到 65535") {
				t.Fatalf("inline validation error is not visible:\n%s", view)
			}
			m, _ = updateUIModel(t, m, struct{}{})
			if m.inputError == "" {
				t.Fatal("non-key input message cleared the validation error")
			}
		})
	}

	for _, test := range []struct {
		value, want string
	}{{"1", "1"}, {"65535", "65535"}, {"00080", "80"}} {
		t.Run("valid-"+test.value, func(t *testing.T) {
			m := testModel()
			backend := m.backend.(*fakeBackend)
			m, _ = openSetting(t, m, settingMixedPort)
			m.input.SetValue(test.value)
			var cmd tea.Cmd
			m, cmd = updateUIModel(t, m, keyPress(tea.KeyEnter, ""))
			if cmd == nil || m.inMode != inputNone || m.inputError != "" {
				t.Fatalf("valid port was not submitted: cmd=%v mode=%v error=%q", cmd != nil, m.inMode, m.inputError)
			}
			m, _ = updateUIModel(t, m, cmd())
			if backend.configCalls != 1 || backend.configKey != "mixed-port" || backend.configValue != test.want {
				t.Fatalf("config calls=%d key=%q value=%q", backend.configCalls, backend.configKey, backend.configValue)
			}
		})
	}
}

func TestAllowLANRequiresConfirmationWhenEnabling(t *testing.T) {
	m := testModel()
	backend := m.backend.(*fakeBackend)
	m.status.AllowLAN = false
	m, cmd := openSetting(t, m, settingAllowLAN)
	if cmd != nil || m.confirm != confirmEnableLAN || backend.configCalls != 0 {
		t.Fatalf("LAN enable skipped confirmation: cmd=%v confirm=%v calls=%d", cmd != nil, m.confirm, backend.configCalls)
	}
	prompt := ansi.Strip(m.confirmText())
	for _, required := range []string{"同一局域网开放", "控制器仍仅限本机"} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("LAN confirmation missing %q: %s", required, prompt)
		}
	}
	m, _ = updateUIModel(t, m, keyPress('n', "n"))
	if backend.configCalls != 0 || m.confirm != confirmNone {
		t.Fatalf("canceling LAN enable changed config: calls=%d confirm=%v", backend.configCalls, m.confirm)
	}

	m, _ = openSetting(t, m, settingAllowLAN)
	m, cmd = updateUIModel(t, m, keyPress('y', "y"))
	if cmd == nil {
		t.Fatal("confirmed LAN enable returned no operation")
	}
	_ = cmd()
	if backend.configCalls != 1 || backend.configKey != "allow-lan" || backend.configValue != "on" {
		t.Fatalf("LAN enable call = %d %q=%q", backend.configCalls, backend.configKey, backend.configValue)
	}
}

func TestAllowLANDisableIsImmediateAndUnknownUsesPicker(t *testing.T) {
	t.Run("known enabled", func(t *testing.T) {
		m := testModel()
		m.status.AllowLAN = true
		backend := m.backend.(*fakeBackend)
		m, cmd := openSetting(t, m, settingAllowLAN)
		if cmd == nil || m.confirm != confirmNone {
			t.Fatalf("LAN disable did not start directly: cmd=%v confirm=%v", cmd != nil, m.confirm)
		}
		_ = cmd()
		if backend.configKey != "allow-lan" || backend.configValue != "off" {
			t.Fatalf("LAN disable = %q=%q", backend.configKey, backend.configValue)
		}
	})

	t.Run("unknown", func(t *testing.T) {
		m := testModel()
		m.status.ConfigAvailable = false
		backend := m.backend.(*fakeBackend)
		m, cmd := openSetting(t, m, settingAllowLAN)
		if cmd != nil || m.picker != pickerAllowLAN || backend.configCalls != 0 {
			t.Fatalf("unknown LAN state did not open picker: cmd=%v picker=%v", cmd != nil, m.picker)
		}
		m, cmd = updateUIModel(t, m, keyPress(tea.KeyEnter, ""))
		if cmd == nil || m.picker != pickerNone {
			t.Fatalf("explicit LAN-off selection did not submit: cmd=%v picker=%v", cmd != nil, m.picker)
		}
		_ = cmd()
		if backend.configKey != "allow-lan" || backend.configValue != "off" {
			t.Fatalf("unknown LAN choice = %q=%q", backend.configKey, backend.configValue)
		}
	})
}

func TestIPv6ToggleAndUnknownPicker(t *testing.T) {
	t.Run("known", func(t *testing.T) {
		m := testModel()
		m.status.IPv6 = false
		backend := m.backend.(*fakeBackend)
		m, cmd := openSetting(t, m, settingIPv6)
		if cmd == nil {
			t.Fatal("known IPv6 toggle returned no operation")
		}
		_ = cmd()
		if backend.configKey != "ipv6" || backend.configValue != "on" {
			t.Fatalf("IPv6 toggle = %q=%q", backend.configKey, backend.configValue)
		}
	})

	t.Run("unknown", func(t *testing.T) {
		m := testModel()
		m.status.ConfigAvailable = false
		backend := m.backend.(*fakeBackend)
		m, _ = openSetting(t, m, settingIPv6)
		if m.picker != pickerIPv6 {
			t.Fatalf("unknown IPv6 picker = %v", m.picker)
		}
		m, _ = updateUIModel(t, m, keyPress(tea.KeyDown, ""))
		m, cmd := updateUIModel(t, m, keyPress(tea.KeyEnter, ""))
		if cmd == nil {
			t.Fatal("explicit IPv6-on choice returned no operation")
		}
		_ = cmd()
		if backend.configKey != "ipv6" || backend.configValue != "on" {
			t.Fatalf("unknown IPv6 choice = %q=%q", backend.configKey, backend.configValue)
		}
	})
}

func TestLogLevelPickerAppliesOneChoiceAndSyncsStreamLevel(t *testing.T) {
	m := testModel()
	backend := m.backend.(*fakeBackend)
	m.status.LogLevel = "info"
	m, cmd := openSetting(t, m, settingLogLevel)
	if cmd != nil || m.picker != pickerLogLevel || m.pickerCursor != 1 {
		t.Fatalf("log picker state: cmd=%v picker=%v cursor=%d", cmd != nil, m.picker, m.pickerCursor)
	}
	if view := ansi.Strip(m.render()); !strings.Contains(view, "SILENT") || !strings.Contains(view, "Enter 确认") {
		t.Fatalf("log picker options or hints missing:\n%s", view)
	}
	m, _ = updateUIModel(t, m, keyPress(tea.KeyEscape, ""))
	if m.picker != pickerNone || backend.configCalls != 0 {
		t.Fatalf("canceling log picker changed config: picker=%v calls=%d", m.picker, backend.configCalls)
	}
	m, _ = openSetting(t, m, settingLogLevel)
	m, _ = updateUIModel(t, m, keyPress(tea.KeyDown, ""))
	m, cmd = updateUIModel(t, m, keyPress(tea.KeyEnter, ""))
	if cmd == nil || backend.configCalls != 0 {
		t.Fatalf("log choice submission state: cmd=%v calls=%d", cmd != nil, backend.configCalls)
	}
	m, _ = updateUIModel(t, m, cmd())
	if backend.configCalls != 1 || backend.configKey != "log-level" || backend.configValue != "warning" {
		t.Fatalf("log config call = %d %q=%q", backend.configCalls, backend.configKey, backend.configValue)
	}
	if m.logLevel != "warning" {
		t.Fatalf("log stream level = %q, want warning", m.logLevel)
	}
}

func TestSettingsUnknownValuesAndNarrowViewport(t *testing.T) {
	m := testModel()
	m.width, m.height, m.page = 60, 16, pageSettings
	m.status.ConfigAvailable = false
	for _, row := range m.settingRows() {
		switch row.id {
		case settingMode, settingTUN, settingMixedPort, settingAllowLAN, settingIPv6, settingLogLevel:
			if row.value != "未知" {
				t.Errorf("unknown setting %q rendered as %q", row.id, row.value)
			}
		}
	}
	m.settingCursor = len(settingOrder) - 1
	m.syncViewports()
	view := m.render()
	if !selectedItemVisible(view, "日志级别") {
		t.Fatalf("last setting is not highlighted in narrow viewport:\n%s", ansi.Strip(view))
	}
	if plain := ansi.Strip(view); !strings.Contains(plain, "9/9") || !strings.Contains(plain, "监听与网络") || !strings.Contains(plain, "日志") {
		t.Fatalf("grouped setting viewport is incomplete:\n%s", plain)
	}
	for lineNumber, line := range strings.Split(view, "\n") {
		if width := ansi.StringWidth(line); width > m.width {
			t.Fatalf("line %d width = %d, want <= %d", lineNumber+1, width, m.width)
		}
	}
}

func TestStatusLogLevelChangeRestartsActiveLogStream(t *testing.T) {
	m := testModel()
	backend := &uiLogRecordingBackend{fakeBackend: m.backend.(*fakeBackend)}
	m.backend = backend
	m.page = pageLogs
	connect := m.beginLogs(false)
	if connect == nil {
		t.Fatal("initial log stream returned no command")
	}
	m, _ = updateUIModel(t, m, connect())
	if len(backend.levels) != 1 || backend.levels[0] != "info" {
		t.Fatalf("initial log levels = %#v", backend.levels)
	}

	m.statusRequest = requestState{inFlight: true, generation: 1}
	status := m.status
	status.LogLevel = "error"
	var cmd tea.Cmd
	m, cmd = updateUIModel(t, m, statusMsg{
		status: status, generation: 1, requestedAt: time.Now(),
	})
	if cmd == nil || m.logLevel != "error" || !m.logConnecting {
		t.Fatalf("changed status did not restart logs: cmd=%v level=%q connecting=%v", cmd != nil, m.logLevel, m.logConnecting)
	}
	batchMessage := cmd()
	batch, ok := batchMessage.(tea.BatchMsg)
	if !ok {
		t.Fatalf("status refresh command = %T, want tea.BatchMsg", batchMessage)
	}
	for _, command := range batch {
		if command != nil {
			_ = command()
		}
	}
	if len(backend.levels) != 2 || backend.levels[1] != "error" {
		t.Fatalf("reconnected log levels = %#v", backend.levels)
	}
}

func TestStatusErrorKeepsReportedConfigSnapshot(t *testing.T) {
	m := testModel()
	m.status.MixedPort = 7890
	m.status.AllowLAN = false
	m.statusRequest = requestState{inFlight: true, generation: 3}
	reported := domain.RuntimeStatus{
		ConfigAvailable: true,
		Mode:            domain.ModeGlobal,
		TUN:             true,
		MixedPort:       8899,
		AllowLAN:        true,
		IPv6:            true,
		LogLevel:        "error",
	}

	m, _ = updateUIModel(t, m, statusMsg{
		status: reported, err: errors.New("控制器不可用"), generation: 3, requestedAt: time.Now(),
	})
	if !m.status.ConfigAvailable || m.status.Mode != domain.ModeGlobal || !m.status.TUN ||
		m.status.MixedPort != 8899 || !m.status.AllowLAN || !m.status.IPv6 || m.status.LogLevel != "error" {
		t.Fatalf("status error discarded reported config: %#v", m.status)
	}
	if m.logLevel != "error" || !strings.Contains(m.err, "控制器不可用") {
		t.Fatalf("partial status state: logLevel=%q error=%q", m.logLevel, m.err)
	}
}
