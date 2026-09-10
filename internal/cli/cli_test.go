package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	appbackend "mihomoctl/internal/app"
	"mihomoctl/internal/domain"
)

type cliPrivilegeExecutor struct {
	command appbackend.PrivilegeCommand
}

func (e *cliPrivilegeExecutor) Run(_ context.Context, command appbackend.PrivilegeCommand) (int, error) {
	command.Args = append([]string(nil), command.Args...)
	e.command = command
	_, _ = io.WriteString(command.Stderr, "sudo: a password is required\n")
	return 1, errors.New("sudo failed")
}

type addCall struct {
	name     string
	source   string
	interval time.Duration
}

type configCall struct {
	key   string
	value string
}

type fakeBackend struct {
	statusValue   domain.RuntimeStatus
	groupsValue   []domain.ProxyGroup
	profilesValue []domain.Profile
	connsValue    []domain.Connection
	scheduleValue domain.ScheduleStatus
	doctorValue   []domain.DoctorCheck
	delaysValue   map[string]uint16
	logEntries    <-chan domain.LogEntry
	logErrors     <-chan error
	logContext    context.Context
	err           error

	initOptions     domain.InitOptions
	initCalls       int
	updateOptions   domain.ProfileUpdateOptions
	serviceActions  []string
	mode            domain.Mode
	tun             *bool
	schedule        *bool
	selection       [2]string
	add             addCall
	config          configCall
	syncCalls       int
	clientSyncCalls int
	usedProfile     string
	removedProfile  string
	closedID        string
	closedAll       bool
	doctorFix       bool
	tuiRuns         int
	tuiNoColor      bool
}

type elevatedFakeBackend struct{ *fakeBackend }

func (elevatedFakeBackend) NeedsElevation() bool { return true }

func (f *fakeBackend) Status(context.Context) (domain.RuntimeStatus, error) {
	return f.statusValue, f.err
}

func (f *fakeBackend) Groups(context.Context) ([]domain.ProxyGroup, error) {
	return f.groupsValue, f.err
}

func (f *fakeBackend) Profiles(context.Context) ([]domain.Profile, error) {
	return f.profilesValue, f.err
}

func (f *fakeBackend) Connections(context.Context) ([]domain.Connection, error) {
	return f.connsValue, f.err
}

func (f *fakeBackend) WatchTraffic(context.Context) (<-chan domain.Traffic, <-chan error) {
	traffic := make(chan domain.Traffic)
	errs := make(chan error)
	return traffic, errs
}

func (f *fakeBackend) WatchLogs(ctx context.Context, _ string) (<-chan domain.LogEntry, <-chan error) {
	f.logContext = ctx
	return f.logEntries, f.logErrors
}

func (f *fakeBackend) Service(_ context.Context, action string) error {
	f.serviceActions = append(f.serviceActions, action)
	return f.err
}

func (f *fakeBackend) SetMode(_ context.Context, mode domain.Mode) error {
	f.mode = mode
	return f.err
}

func (f *fakeBackend) SetTUN(_ context.Context, enabled bool) error {
	f.tun = &enabled
	return f.err
}

func (f *fakeBackend) SetSchedule(_ context.Context, enabled bool) error {
	f.schedule = &enabled
	return f.err
}

func (f *fakeBackend) SelectProxy(_ context.Context, group, proxy string) error {
	f.selection = [2]string{group, proxy}
	return f.err
}

func (f *fakeBackend) TestGroup(context.Context, string) (map[string]uint16, error) {
	return f.delaysValue, f.err
}

func (f *fakeBackend) AddProfile(_ context.Context, name, source string, interval time.Duration) error {
	f.add = addCall{name: name, source: source, interval: interval}
	return f.err
}

func (f *fakeBackend) UpdateProfile(context.Context, string) error { return f.err }

func (f *fakeBackend) UseProfile(_ context.Context, name string) error {
	f.usedProfile = name
	return f.err
}

func (f *fakeBackend) RemoveProfile(_ context.Context, name string) error {
	f.removedProfile = name
	return f.err
}

func (f *fakeBackend) CloseConnection(_ context.Context, id string) error {
	f.closedID = id
	return f.err
}

func (f *fakeBackend) CloseAllConnections(context.Context) error {
	f.closedAll = true
	return f.err
}

func (f *fakeBackend) Initialize(_ context.Context, options domain.InitOptions) error {
	f.initCalls++
	f.initOptions = options
	return f.err
}

func (f *fakeBackend) UpdateProfiles(_ context.Context, options domain.ProfileUpdateOptions) error {
	f.updateOptions = options
	return f.err
}

func (f *fakeBackend) SetConfig(_ context.Context, key, value string) error {
	f.config = configCall{key: key, value: value}
	return f.err
}

func (f *fakeBackend) SyncPublicState(context.Context) error {
	f.syncCalls++
	return f.err
}

func (f *fakeBackend) SyncClientState(context.Context) error {
	f.clientSyncCalls++
	return f.err
}

func (f *fakeBackend) ScheduleStatus(context.Context) (domain.ScheduleStatus, error) {
	return f.scheduleValue, f.err
}

func (f *fakeBackend) Doctor(_ context.Context, fix bool) ([]domain.DoctorCheck, error) {
	f.doctorFix = fix
	return f.doctorValue, f.err
}

func (f *fakeBackend) RunTUI() error {
	f.tuiRuns++
	_, f.tuiNoColor = os.LookupEnv("NO_COLOR")
	return f.err
}

func runCommand(t *testing.T, backend Backend, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	command := New(backend, &stdout, &stderr)
	command.SetArgs(args)
	err := command.ExecuteContext(context.Background())
	return stdout.String(), stderr.String(), err
}

func runCommandWithInput(t *testing.T, backend Backend, input string, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	command := New(backend, &stdout, &stderr)
	command.SetIn(strings.NewReader(input))
	command.SetArgs(args)
	err := command.ExecuteContext(context.Background())
	return stdout.String(), stderr.String(), err
}

func TestCommandRoutingAndFlags(t *testing.T) {
	t.Run("init", func(t *testing.T) {
		backend := &fakeBackend{}
		_, _, err := runCommand(t, backend, "init", "--service", "mihomo.service", "--config", "/etc/mihomo/config.yaml", "--controller", "127.0.0.1:9191", "--force")
		if err != nil {
			t.Fatal(err)
		}
		want := domain.InitOptions{Service: "mihomo.service", ConfigPath: "/etc/mihomo/config.yaml", Controller: "127.0.0.1:9191", Force: true}
		if !reflect.DeepEqual(backend.initOptions, want) {
			t.Fatalf("Initialize options = %#v, want %#v", backend.initOptions, want)
		}
	})

	t.Run("service enable now", func(t *testing.T) {
		backend := &fakeBackend{}
		if _, _, err := runCommand(t, backend, "service", "enable", "--now"); err != nil {
			t.Fatal(err)
		}
		if want := []string{"enable-now"}; !reflect.DeepEqual(backend.serviceActions, want) {
			t.Fatalf("service actions = %v, want %v", backend.serviceActions, want)
		}
	})

	t.Run("mode tun and config", func(t *testing.T) {
		backend := &fakeBackend{}
		if _, _, err := runCommand(t, backend, "mode", "global"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := runCommand(t, backend, "tun", "on"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := runCommand(t, backend, "config", "set", "allow-lan", "yes"); err != nil {
			t.Fatal(err)
		}
		if backend.mode != domain.ModeGlobal || backend.tun == nil || !*backend.tun {
			t.Fatalf("mode/tun routing failed: mode=%q tun=%v", backend.mode, backend.tun)
		}
		if backend.config != (configCall{key: "allow-lan", value: "true"}) {
			t.Fatalf("SetConfig call = %#v", backend.config)
		}
	})

	t.Run("hidden public state sync", func(t *testing.T) {
		backend := &fakeBackend{}
		stdout, _, err := runCommand(t, backend, "config", "sync-public-state", "--output", "json")
		if err != nil {
			t.Fatal(err)
		}
		if backend.syncCalls != 1 || !strings.Contains(stdout, `"synced":true`) {
			t.Fatalf("sync routing failed: calls=%d output=%q", backend.syncCalls, stdout)
		}
		help, _, err := runCommand(t, backend, "config", "--help")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(help, "sync-public-state") {
			t.Fatalf("hidden migration command appeared in help: %s", help)
		}
	})

	t.Run("client state sync", func(t *testing.T) {
		backend := &fakeBackend{}
		stdout, _, err := runCommand(t, backend, "config", "sync-client", "--output", "json")
		if err != nil {
			t.Fatal(err)
		}
		if backend.clientSyncCalls != 1 || !strings.Contains(stdout, `"synced":true`) {
			t.Fatalf("client sync routing failed: calls=%d output=%q", backend.clientSyncCalls, stdout)
		}
	})

	t.Run("proxy profile and connections", func(t *testing.T) {
		backend := &fakeBackend{}
		if _, _, err := runCommand(t, backend, "proxy", "select", "PROXY", "香港 01"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := runCommand(t, backend, "profile", "add", "https://example.test/sub", "--name", "日常", "--interval", "12h"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := runCommand(t, backend, "profile", "update", "--due"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := runCommand(t, backend, "connections", "close", "--all"); err != nil {
			t.Fatal(err)
		}
		if backend.selection != [2]string{"PROXY", "香港 01"} {
			t.Fatalf("selection = %#v", backend.selection)
		}
		if backend.add != (addCall{name: "日常", source: "https://example.test/sub", interval: 12 * time.Hour}) {
			t.Fatalf("add call = %#v", backend.add)
		}
		if backend.updateOptions != (domain.ProfileUpdateOptions{Due: true}) || !backend.closedAll {
			t.Fatalf("update/close routing failed: options=%#v closedAll=%v", backend.updateOptions, backend.closedAll)
		}
	})

	t.Run("profile source from stdin", func(t *testing.T) {
		backend := &fakeBackend{}
		source := "vless://user@example.test:443?security=tls#node\n"
		if _, _, err := runCommandWithInput(t, backend, source, "profile", "add", "-", "--name", "stdin"); err != nil {
			t.Fatal(err)
		}
		if backend.add.name != "stdin" || backend.add.source != source {
			t.Fatalf("add call = %#v", backend.add)
		}
	})

	t.Run("local profile output reports immutable snapshot", func(t *testing.T) {
		backend := &fakeBackend{}
		path := filepath.Join(t.TempDir(), "local.yaml")
		if err := os.WriteFile(path, []byte("proxies: []\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		stdout, _, err := runCommand(t, elevatedFakeBackend{backend}, "profile", "add", path, "--output", "json")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(stdout, `"immutable":true`) || !strings.Contains(stdout, `"update_interval":"0s"`) {
			t.Fatalf("local profile output = %q", stdout)
		}
		stdout, _, err = runCommand(t, backend, "profile", "add", path, "--output", "json")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(stdout, `"immutable":false`) || !strings.Contains(stdout, "update_interval") {
			t.Fatalf("root-style local profile output = %q", stdout)
		}
	})

	t.Run("schedule and doctor", func(t *testing.T) {
		backend := &fakeBackend{}
		if _, _, err := runCommand(t, backend, "schedule", "enable"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := runCommand(t, backend, "doctor", "--fix"); err != nil {
			t.Fatal(err)
		}
		if backend.schedule == nil || !*backend.schedule || !backend.doctorFix {
			t.Fatalf("schedule/doctor routing failed: schedule=%v fix=%v", backend.schedule, backend.doctorFix)
		}
	})
}

func TestProfileUseReportsPartialSuccessAsWarning(t *testing.T) {
	backend := &fakeBackend{err: &appbackend.OperationWarning{Message: "部分节点未恢复"}}
	stdout, _, err := runCommand(t, backend, "profile", "use", "daily", "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	if backend.usedProfile != "daily" || !strings.Contains(stdout, `"warning":"部分节点未恢复"`) {
		t.Fatalf("profile warning output = %q, used=%q", stdout, backend.usedProfile)
	}
	stdout, _, err = runCommand(t, backend, "profile", "use", "daily")
	if err != nil || !strings.Contains(stdout, "配置已激活") || !strings.Contains(stdout, "警告: 部分节点未恢复") {
		t.Fatalf("profile table warning = %q, %v", stdout, err)
	}
}

func TestJSONEnvelopeAndProfileRedaction(t *testing.T) {
	secret := "https://user:password@example.test/sub?token=secret"
	backend := &fakeBackend{profilesValue: []domain.Profile{{
		ID: "daily", Name: "日常", Kind: domain.ProfileRemote, Source: secret, Active: true, UpdateInterval: 24 * time.Hour,
	}}}
	stdout, _, err := runCommand(t, backend, "profile", "list", "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout, "password") || strings.Contains(stdout, "token") || strings.Contains(stdout, "secret") {
		t.Fatalf("profile JSON leaked source: %s", stdout)
	}
	var result struct {
		SchemaVersion int `json:"schema_version"`
		Data          []struct {
			ID     string `json:"id"`
			Source string `json:"source"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON %q: %v", stdout, err)
	}
	if result.SchemaVersion != 1 || len(result.Data) != 1 || result.Data[0].ID != "daily" || result.Data[0].Source != "" {
		t.Fatalf("unexpected envelope: %#v", result)
	}
}

func TestFollowLogsWritesJSONLines(t *testing.T) {
	entries := make(chan domain.LogEntry, 2)
	entries <- domain.LogEntry{Time: "10:00:00", Level: "info", Message: "first"}
	entries <- domain.LogEntry{Time: "10:00:01", Level: "warning", Message: "second"}
	close(entries)
	errs := make(chan error)
	close(errs)
	backend := &fakeBackend{logEntries: entries, logErrors: errs}
	stdout, _, err := runCommand(t, backend, "logs", "--follow", "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 2 {
		t.Fatalf("JSONL line count = %d, output %q", len(lines), stdout)
	}
	for index, line := range lines {
		var value struct {
			SchemaVersion int             `json:"schema_version"`
			Data          domain.LogEntry `json:"data"`
		}
		if err := json.Unmarshal([]byte(line), &value); err != nil {
			t.Fatalf("line %d is not JSON: %v", index, err)
		}
		if value.SchemaVersion != 1 {
			t.Fatalf("line %d schema_version = %d", index, value.SchemaVersion)
		}
	}
}

func TestNonFollowLogsTimesOutAndCancelsStream(t *testing.T) {
	previousTimeout := logSampleTimeout
	logSampleTimeout = 10 * time.Millisecond
	t.Cleanup(func() { logSampleTimeout = previousTimeout })

	backend := &fakeBackend{logEntries: make(chan domain.LogEntry), logErrors: make(chan error)}
	stdout, _, err := runCommand(t, backend, "logs", "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q", stdout)
	}
	if backend.logContext == nil {
		t.Fatal("WatchLogs did not receive a context")
	}
	select {
	case <-backend.logContext.Done():
	default:
		t.Fatal("log context remains active after sampling")
	}
}

func TestEveryNonStreamingCommandSupportsJSON(t *testing.T) {
	tests := [][]string{
		{"init"},
		{"status"},
		{"service", "start"},
		{"service", "stop"},
		{"service", "restart"},
		{"service", "enable"},
		{"service", "disable"},
		{"mode", "rule"},
		{"tun", "off"},
		{"config", "show"},
		{"config", "set", "mixed-port", "7890"},
		{"proxy", "list"},
		{"proxy", "select", "PROXY", "DIRECT"},
		{"proxy", "test", "PROXY"},
		{"profile", "list"},
		{"profile", "add", "https://example.test/sub"},
		{"profile", "update", "--due"},
		{"profile", "use", "daily"},
		{"profile", "remove", "daily"},
		{"connections", "list"},
		{"connections", "close", "connection-id"},
		{"connections", "close", "--all"},
		{"schedule", "enable"},
		{"schedule", "disable"},
		{"schedule", "status"},
		{"doctor"},
		{"version"},
	}
	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			backend := &fakeBackend{}
			commandArgs := append(append([]string(nil), args...), "--output", "json")
			stdout, stderr, err := runCommand(t, backend, commandArgs...)
			if err != nil {
				t.Fatal(err)
			}
			if stderr != "" {
				t.Fatalf("stderr = %q", stderr)
			}
			var result struct {
				SchemaVersion int             `json:"schema_version"`
				Data          json.RawMessage `json:"data"`
			}
			if err := json.Unmarshal([]byte(stdout), &result); err != nil {
				t.Fatalf("output is not one JSON envelope: %q: %v", stdout, err)
			}
			if result.SchemaVersion != 1 || len(result.Data) == 0 {
				t.Fatalf("unexpected envelope: %s", stdout)
			}
			if reflect.DeepEqual(args, []string{"profile", "update", "--due"}) && backend.updateOptions != (domain.ProfileUpdateOptions{Due: true}) {
				t.Fatalf("timer route options = %#v", backend.updateOptions)
			}
		})
	}
}

func TestValidationErrorsDoNotReachBackend(t *testing.T) {
	tests := [][]string{
		{"init", "--controller", "0.0.0.0:9090"},
		{"init", "--controller", "http://127.0.0.1:9090"},
		{"init", "--controller", "127.0.0.1:70000"},
		{"mode", "unknown"},
		{"tun", "maybe"},
		{"config", "set", "secret", "value"},
		{"config", "set", "mixed-port", "70000"},
		{"profile", "update"},
		{"profile", "update", "daily", "--all"},
		{"profile", "add", " "},
		{"profile", "update", " "},
		{"profile", "use", " "},
		{"proxy", "select", " ", "DIRECT"},
		{"proxy", "test", " "},
		{"connections", "close"},
		{"connections", "close", "id", "--all"},
		{"connections", "close", " "},
		{"status", "extra"},
		{"status", "--output", "yaml"},
	}
	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			backend := &fakeBackend{}
			_, _, err := runCommand(t, backend, args...)
			var exitErr *ExitError
			if !errors.As(err, &exitErr) || exitErr.Code != ExitInvalid {
				t.Fatalf("error = %#v, want ExitInvalid", err)
			}
			if backend.initCalls != 0 {
				t.Fatalf("invalid command reached Initialize: %d calls", backend.initCalls)
			}
		})
	}
}

func TestTerminalOutputStripsUntrustedControlSequences(t *testing.T) {
	malicious := "safe\x1b]52;c;Y2xpcGJvYXJk\a\x1b[31mred\x1b[0m\nnext\x00"
	cleaned := cleanCell(malicious)
	if strings.ContainsAny(cleaned, "\x1b\a\n\r\t\x00") || strings.Contains(cleaned, "Y2xpcGJvYXJk") {
		t.Fatalf("cleanCell retained terminal controls: %q", cleaned)
	}
	for _, text := range []string{"safe", "red", "next"} {
		if !strings.Contains(cleaned, text) {
			t.Fatalf("cleanCell removed printable text %q: %q", text, cleaned)
		}
	}

	var output bytes.Buffer
	if err := writeTable(&output, []string{"NAME"}, [][]string{{malicious}}); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(output.String(), "\x1b\a\x00") || strings.Contains(output.String(), "Y2xpcGJvYXJk") {
		t.Fatalf("table retained terminal controls: %q", output.String())
	}
}

func TestProfileInputLimit(t *testing.T) {
	backend := &fakeBackend{}
	input := strings.Repeat("x", maxProfileInput+1)
	_, _, err := runCommandWithInput(t, backend, input, "profile", "add", "-")
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != ExitInvalid {
		t.Fatalf("error = %#v, want ExitInvalid", err)
	}
	if backend.add.source != "" {
		t.Fatal("backend received oversized input")
	}
}

func TestRootRunsTUI(t *testing.T) {
	previousCheck := isTerminalStream
	isTerminalStream = func(any) bool { return true }
	t.Cleanup(func() { isTerminalStream = previousCheck })

	backend := &fakeBackend{}
	if _, _, err := runCommand(t, backend); err != nil {
		t.Fatal(err)
	}
	if backend.tuiRuns != 1 {
		t.Fatalf("RunTUI calls = %d", backend.tuiRuns)
	}
}

func TestRootWithoutTTYShowsHelp(t *testing.T) {
	previousCheck := isTerminalStream
	isTerminalStream = func(any) bool { return false }
	t.Cleanup(func() { isTerminalStream = previousCheck })

	backend := &fakeBackend{}
	stdout, _, err := runCommand(t, backend)
	if err != nil {
		t.Fatal(err)
	}
	if backend.tuiRuns != 0 {
		t.Fatalf("RunTUI calls = %d", backend.tuiRuns)
	}
	if !strings.Contains(stdout, "Usage:") || !strings.Contains(stdout, "mihomoctl") {
		t.Fatalf("help output = %q", stdout)
	}
}

func TestProxyTestHelpDescribesDirectMembers(t *testing.T) {
	stdout, _, err := runCommand(t, &fakeBackend{}, "proxy", "test", "--help")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "直接成员") || strings.Contains(stdout, "全部节点") {
		t.Fatalf("proxy test help = %q", stdout)
	}
}

func TestRootRequiresBothInputAndOutputTTY(t *testing.T) {
	previousCheck := isTerminalStream
	checks := 0
	isTerminalStream = func(any) bool {
		checks++
		return checks == 1
	}
	t.Cleanup(func() { isTerminalStream = previousCheck })

	backend := &fakeBackend{}
	stdout, _, err := runCommand(t, backend)
	if err != nil {
		t.Fatal(err)
	}
	if checks != 2 || backend.tuiRuns != 0 || !strings.Contains(stdout, "Usage:") {
		t.Fatalf("checks=%d tuiRuns=%d output=%q", checks, backend.tuiRuns, stdout)
	}
}

func TestNoColorIsAppliedOnlyWhileTUIRuns(t *testing.T) {
	previousCheck := isTerminalStream
	isTerminalStream = func(any) bool { return true }
	t.Cleanup(func() { isTerminalStream = previousCheck })

	previous, existed := os.LookupEnv("NO_COLOR")
	if err := os.Unsetenv("NO_COLOR"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv("NO_COLOR", previous)
		} else {
			_ = os.Unsetenv("NO_COLOR")
		}
	})

	backend := &fakeBackend{}
	if _, _, err := runCommand(t, backend, "--no-color"); err != nil {
		t.Fatal(err)
	}
	if !backend.tuiNoColor {
		t.Fatal("RunTUI did not observe NO_COLOR")
	}
	if _, exists := os.LookupEnv("NO_COLOR"); exists {
		t.Fatal("NO_COLOR was not restored after RunTUI")
	}
}

func TestExecutePrintsErrorsAndReturnsStableCodes(t *testing.T) {
	previous := os.Args
	t.Cleanup(func() { os.Args = previous })

	t.Run("invalid", func(t *testing.T) {
		os.Args = []string{"mihomoctl", "config", "set", "unknown", "value"}
		var stdout, stderr bytes.Buffer
		code := Execute(context.Background(), &fakeBackend{}, &stdout, &stderr)
		if code != ExitInvalid || !strings.Contains(stderr.String(), "mihomoctl:") {
			t.Fatalf("code=%d stderr=%q", code, stderr.String())
		}
	})

	t.Run("unknown command", func(t *testing.T) {
		os.Args = []string{"mihomoctl", "does-not-exist"}
		var stderr bytes.Buffer
		code := Execute(context.Background(), &fakeBackend{}, io.Discard, &stderr)
		if code != ExitInvalid || stderr.Len() == 0 {
			t.Fatalf("code=%d stderr=%q", code, stderr.String())
		}
	})

	t.Run("unknown flag", func(t *testing.T) {
		os.Args = []string{"mihomoctl", "status", "--does-not-exist"}
		code := Execute(context.Background(), &fakeBackend{}, io.Discard, io.Discard)
		if code != ExitInvalid {
			t.Fatalf("code=%d", code)
		}
	})

	t.Run("invalid duration", func(t *testing.T) {
		os.Args = []string{"mihomoctl", "profile", "add", "source", "--interval", "tomorrow"}
		code := Execute(context.Background(), &fakeBackend{}, io.Discard, io.Discard)
		if code != ExitInvalid {
			t.Fatalf("code=%d", code)
		}
	})

	t.Run("permission", func(t *testing.T) {
		os.Args = []string{"mihomoctl", "status"}
		var stderr bytes.Buffer
		code := Execute(context.Background(), &fakeBackend{err: os.ErrPermission}, io.Discard, &stderr)
		if code != ExitPermission || stderr.Len() == 0 {
			t.Fatalf("code=%d stderr=%q", code, stderr.String())
		}
	})

	t.Run("unavailable", func(t *testing.T) {
		os.Args = []string{"mihomoctl", "status"}
		code := Execute(context.Background(), &fakeBackend{err: &ExitError{Code: ExitUnavailable, Err: errors.New("offline")}}, io.Discard, io.Discard)
		if code != ExitUnavailable {
			t.Fatalf("code=%d", code)
		}
	})
}

func TestCLIInitializationUsesInteractiveSudoAndReturnsPermissionCode(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	executor := &cliPrivilegeExecutor{}
	backend, err := appbackend.New(
		appbackend.WithPaths(appbackend.Paths{
			ConfigFile: "/etc/mihomoctl/config.yaml", DataDir: "/var/lib/mihomoctl",
			ProfileRoot: "/var/lib/mihomoctl/store", BackupDir: "/var/lib/mihomoctl/backups",
			OperationLock: "/var/lib/mihomoctl/.operation.lock", PublicFile: "/var/lib/mihomoctl/public.json",
			ClientFile:     filepath.Join(home, ".config", "mihomoctl", "client.yaml"),
			DefaultService: "mihomo.service", TimerUnit: "mihomoctl-update.timer",
		}),
		appbackend.WithPrivilegeExecutor(executor),
		appbackend.WithEUID(func() int { return 1000 }),
		appbackend.WithExecutable(func() (string, error) { return "/usr/bin/mihomoctl", nil }),
		appbackend.WithExecutableValidator(func(path string) (string, error) { return path, nil }),
		appbackend.WithSudoPathResolver(func() (string, error) { return "/usr/bin/sudo", nil }),
	)
	if err != nil {
		t.Fatal(err)
	}

	previous := os.Args
	os.Args = []string{"mihomoctl", "init"}
	t.Cleanup(func() { os.Args = previous })
	var stderr bytes.Buffer
	code := Execute(context.Background(), backend, io.Discard, &stderr)
	if code != ExitPermission || !strings.Contains(stderr.String(), "sudo 身份验证未完成") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if executor.command.Name != "/usr/bin/sudo" || len(executor.command.Args) == 0 || executor.command.Args[0] == "-n" {
		t.Fatalf("CLI sudo command = %q %#v", executor.command.Name, executor.command.Args)
	}
}
