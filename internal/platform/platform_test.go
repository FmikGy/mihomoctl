package platform

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestExecRunnerCapturesAndMirrorsOutput(t *testing.T) {
	var mirror bytes.Buffer
	result, err := (ExecRunner{Stdout: &mirror}).Run(context.Background(), "/usr/bin/printf", "%s", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Stdout) != "hello" || mirror.String() != "hello" {
		t.Fatalf("captured=%q mirrored=%q", result.Stdout, mirror.String())
	}
}

func TestCommandErrorDoesNotExposeArguments(t *testing.T) {
	_, err := (ExecRunner{}).Run(context.Background(), "/usr/bin/false", "subscription-secret")
	if err == nil {
		t.Fatal("expected command failure")
	}
	if strings.Contains(err.Error(), "subscription-secret") {
		t.Fatalf("command error exposed arguments: %v", err)
	}
}

type recordedCommand struct {
	name string
	args []string
}

type recordingRunner struct {
	calls []recordedCommand
	run   func(context.Context, string, ...string) (CommandResult, error)
}

func (r *recordingRunner) Run(ctx context.Context, name string, args ...string) (CommandResult, error) {
	r.calls = append(r.calls, recordedCommand{name: name, args: append([]string(nil), args...)})
	if r.run != nil {
		return r.run(ctx, name, args...)
	}
	return CommandResult{}, nil
}

func TestSystemdStatus(t *testing.T) {
	runner := &recordingRunner{run: func(context.Context, string, ...string) (CommandResult, error) {
		return CommandResult{Stdout: []byte(strings.Join([]string{
			"LoadState=loaded",
			"ActiveState=active",
			"SubState=running",
			"UnitFileState=enabled",
			"MainPID=1842",
		}, "\n"))}, nil
	}}
	manager, err := NewSystemd(runner, "mihomo.service")
	if err != nil {
		t.Fatal(err)
	}
	status, err := manager.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Active || !status.Enabled || status.State != "running" || status.PID != 1842 {
		t.Fatalf("unexpected status: %#v", status)
	}
	if len(runner.calls) != 1 || runner.calls[0].name != "systemctl" {
		t.Fatalf("unexpected command: %#v", runner.calls)
	}
}

func TestSystemdActionsAreFixed(t *testing.T) {
	runner := &recordingRunner{}
	manager, err := NewSystemd(runner, "mihomo.service")
	if err != nil {
		t.Fatal(err)
	}
	actions := []struct {
		name string
		run  func(context.Context) error
	}{
		{"start", manager.Start},
		{"stop", manager.Stop},
		{"enable", manager.Enable},
		{"disable", manager.Disable},
	}
	for _, action := range actions {
		if err := action.run(context.Background()); err != nil {
			t.Fatalf("%s: %v", action.name, err)
		}
	}
	for i, action := range actions {
		want := recordedCommand{name: "systemctl", args: []string{action.name, "--", "mihomo.service"}}
		if !reflect.DeepEqual(runner.calls[i], want) {
			t.Fatalf("command %d = %#v, want %#v", i, runner.calls[i], want)
		}
	}
}

func TestSystemdRestartResetsFailureBeforeRestart(t *testing.T) {
	runner := &recordingRunner{}
	manager, err := NewSystemd(runner, "mihomo.service")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Restart(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []recordedCommand{
		{name: "systemctl", args: []string{"reset-failed", "--", "mihomo.service"}},
		{name: "systemctl", args: []string{"restart", "--", "mihomo.service"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("commands = %#v, want %#v", runner.calls, want)
	}
}

func TestSystemdRestartStopsWhenResetFailedFails(t *testing.T) {
	resetErr := errors.New("reset failed")
	runner := &recordingRunner{run: func(_ context.Context, _ string, args ...string) (CommandResult, error) {
		if len(args) > 0 && args[0] == "reset-failed" {
			return CommandResult{}, resetErr
		}
		return CommandResult{}, nil
	}}
	manager, err := NewSystemd(runner, "mihomo.service")
	if err != nil {
		t.Fatal(err)
	}
	err = manager.Restart(context.Background())
	if !errors.Is(err, resetErr) || !strings.Contains(err.Error(), "reset-failed") {
		t.Fatalf("Restart error = %v, want reset-failed error", err)
	}
	want := []recordedCommand{
		{name: "systemctl", args: []string{"reset-failed", "--", "mihomo.service"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("commands = %#v, want %#v", runner.calls, want)
	}
}

func TestSystemdRestartPreservesRestartError(t *testing.T) {
	restartErr := errors.New("restart failed")
	runner := &recordingRunner{run: func(_ context.Context, _ string, args ...string) (CommandResult, error) {
		if len(args) > 0 && args[0] == "restart" {
			return CommandResult{}, restartErr
		}
		return CommandResult{}, nil
	}}
	manager, err := NewSystemd(runner, "mihomo.service")
	if err != nil {
		t.Fatal(err)
	}
	err = manager.Restart(context.Background())
	if !errors.Is(err, restartErr) || !strings.Contains(err.Error(), "systemctl restart") {
		t.Fatalf("Restart error = %v, want restart error", err)
	}
	want := []recordedCommand{
		{name: "systemctl", args: []string{"reset-failed", "--", "mihomo.service"}},
		{name: "systemctl", args: []string{"restart", "--", "mihomo.service"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("commands = %#v, want %#v", runner.calls, want)
	}
}

func TestSystemdRejectsUnsafeUnit(t *testing.T) {
	for _, unit := range []string{"mihomo.service; reboot", "--no-block.service", "other.socket"} {
		if _, err := NewSystemd(&recordingRunner{}, unit); err == nil {
			t.Fatalf("expected unsafe unit name %q to be rejected", unit)
		}
	}
}

func TestSystemdSupportsManagedTimer(t *testing.T) {
	runner := &recordingRunner{}
	manager, err := NewSystemd(runner, "mihomoctl-update.timer")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Enable(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := recordedCommand{name: "systemctl", args: []string{"enable", "--", "mihomoctl-update.timer"}}
	if !reflect.DeepEqual(runner.calls[0], want) {
		t.Fatalf("command = %#v, want %#v", runner.calls[0], want)
	}
}

func TestDiscovererFindsUnitBinaryAndConfig(t *testing.T) {
	temp := t.TempDir()
	binary := filepath.Join(temp, "mihomo")
	if err := os.WriteFile(binary, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{run: func(_ context.Context, name string, args ...string) (CommandResult, error) {
		if name != "systemctl" {
			t.Fatalf("unexpected command %q", name)
		}
		return CommandResult{Stdout: []byte(
			"LoadState=loaded\n" +
				"FragmentPath=/usr/lib/systemd/system/mihomo.service\n" +
				"ExecStart={ path=" + binary + " ; argv[]=" + binary + " -d /etc/mihomo ; ignore_errors=no ; }\n",
		)}, nil
	}}
	discoverer := NewDiscoverer(runner)
	discoverer.LookPath = func(string) (string, error) { return "", errors.New("not found") }
	got, err := discoverer.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.BinaryPath != binary || got.Unit != "mihomo.service" || got.ConfigDir != "/etc/mihomo" || got.ConfigPath != "/etc/mihomo/config.yaml" {
		t.Fatalf("unexpected discovery result: %#v", got)
	}
}

func TestParseSystemdExecStartAndConfigFlags(t *testing.T) {
	path, args, err := parseSystemdExecStart(`{ path=/usr/bin/mihomo ; argv[]=/usr/bin/mihomo -d "/etc/mihomo data" -f=active.yaml ; ignore_errors=no ; }`)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/usr/bin/mihomo" {
		t.Fatalf("path = %q", path)
	}
	dir, config := extractConfigPaths(args)
	if dir != "/etc/mihomo data" || config != "/etc/mihomo data/active.yaml" {
		t.Fatalf("config paths = %q, %q", dir, config)
	}
}

func TestParseSystemdEscapesAndEqualsConfigFlags(t *testing.T) {
	path, args, err := parseSystemdExecStart(`{ path="/opt/mihomo\x20bin/mihomo" ; argv[]="/opt/mihomo\x20bin/mihomo" -d=/etc/mihomo\x20data -f active.yaml ; ignore_errors=no ; }`)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/opt/mihomo bin/mihomo" {
		t.Fatalf("path = %q", path)
	}
	dir, config := extractConfigPaths(args)
	if dir != "/etc/mihomo data" || config != "/etc/mihomo data/active.yaml" {
		t.Fatalf("config paths = %q, %q", dir, config)
	}
}

func TestInlineConfigFlagIsNotTreatedAsAFile(t *testing.T) {
	dir, config := extractConfigPaths([]string{"/usr/bin/mihomo", "-config", "c2VjcmV0"})
	if dir != "" || config != "" {
		t.Fatalf("inline config produced file paths %q, %q", dir, config)
	}
}

func TestSudoReexecutorUsesAllowlistedArguments(t *testing.T) {
	runner := &recordingRunner{}
	reexecutor, err := NewSudoReexecutor(runner, "/usr/bin/mihomoctl")
	if err != nil {
		t.Fatal(err)
	}
	reexecutor.EUID = func() int { return 1000 }
	if _, err := reexecutor.Run(context.Background(), ElevatedServiceRestart); err != nil {
		t.Fatal(err)
	}
	want := recordedCommand{name: "sudo", args: []string{"--", "/usr/bin/mihomoctl", "service", "restart"}}
	if !reflect.DeepEqual(runner.calls[0], want) {
		t.Fatalf("command = %#v, want %#v", runner.calls[0], want)
	}
	if _, err := reexecutor.Run(context.Background(), ElevatedAction("arbitrary")); err == nil {
		t.Fatal("expected unknown action to be rejected")
	}
}

func TestSudoReexecutorSkipsSudoForRoot(t *testing.T) {
	runner := &recordingRunner{}
	reexecutor, err := NewSudoReexecutor(runner, "/usr/bin/mihomoctl")
	if err != nil {
		t.Fatal(err)
	}
	reexecutor.EUID = func() int { return 0 }
	if _, err := reexecutor.Run(context.Background(), ElevatedTUNEnable); err != nil {
		t.Fatal(err)
	}
	want := recordedCommand{name: "/usr/bin/mihomoctl", args: []string{"tun", "on"}}
	if !reflect.DeepEqual(runner.calls[0], want) {
		t.Fatalf("command = %#v, want %#v", runner.calls[0], want)
	}
}

func TestConfigApplierSuccess(t *testing.T) {
	fixture := newApplierFixture(t, []byte("mode: old\n"))
	activationCalls := 0
	fixture.applier.Activate = func(context.Context) error {
		activationCalls++
		assertFileContents(t, fixture.configPath, "mode: new\n")
		return nil
	}
	fixture.applier.HealthCheck = func(context.Context) error { return nil }

	result, err := fixture.applier.Apply(context.Background(), []byte("mode: new\n"))
	if err != nil {
		t.Fatal(err)
	}
	if activationCalls != 1 {
		t.Fatalf("activation calls = %d", activationCalls)
	}
	assertFileContents(t, fixture.configPath, "mode: new\n")
	assertMode(t, fixture.configPath, 0o640)
	assertFileContents(t, result.BackupPath, "mode: old\n")
	assertMode(t, result.BackupPath, 0o600)
	assertValidationCall(t, fixture.runner.calls, fixture.binary, fixture.configDir)
}

func TestConfigApplierRollsBackActivationFailure(t *testing.T) {
	fixture := newApplierFixture(t, []byte("mode: old\n"))
	activationCalls := 0
	fixture.applier.Activate = func(context.Context) error {
		activationCalls++
		if activationCalls == 1 {
			assertFileContents(t, fixture.configPath, "mode: broken\n")
			return errors.New("reload failed")
		}
		assertFileContents(t, fixture.configPath, "mode: old\n")
		return nil
	}
	fixture.applier.HealthCheck = func(context.Context) error { return nil }

	_, err := fixture.applier.Apply(context.Background(), []byte("mode: broken\n"))
	var applyErr *ApplyError
	if !errors.As(err, &applyErr) || applyErr.Stage != "activation" || applyErr.RollbackErr != nil {
		t.Fatalf("unexpected error: %#v", err)
	}
	if activationCalls != 2 {
		t.Fatalf("activation calls = %d", activationCalls)
	}
	assertFileContents(t, fixture.configPath, "mode: old\n")
	assertMode(t, fixture.configPath, 0o644)
}

func TestConfigApplierRollsBackHealthFailure(t *testing.T) {
	fixture := newApplierFixture(t, []byte("mode: old\n"))
	fixture.applier.Activate = func(context.Context) error { return nil }
	healthCalls := 0
	fixture.applier.HealthCheck = func(context.Context) error {
		healthCalls++
		if healthCalls == 1 {
			return errors.New("controller unavailable")
		}
		assertFileContents(t, fixture.configPath, "mode: old\n")
		return nil
	}

	_, err := fixture.applier.Apply(context.Background(), []byte("mode: unhealthy\n"))
	var applyErr *ApplyError
	if !errors.As(err, &applyErr) || applyErr.Stage != "health check" || applyErr.RollbackErr != nil {
		t.Fatalf("unexpected error: %#v", err)
	}
	if healthCalls != 2 {
		t.Fatalf("health calls = %d", healthCalls)
	}
	assertFileContents(t, fixture.configPath, "mode: old\n")
	assertMode(t, fixture.configPath, 0o644)
}

func TestConfigApplierPreservesRequiredGroupRead(t *testing.T) {
	fixture := newApplierFixture(t, []byte("mode: old\n"))
	if err := os.Chmod(fixture.configPath, 0o640); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(fixture.configPath)
	if err != nil {
		t.Fatal(err)
	}
	fixture.applier.Activate = func(context.Context) error { return nil }
	fixture.applier.HealthCheck = func(context.Context) error { return nil }
	if _, err := fixture.applier.Apply(context.Background(), []byte("mode: new\n")); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(fixture.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if before.Sys().(*syscall.Stat_t).Uid != after.Sys().(*syscall.Stat_t).Uid || before.Sys().(*syscall.Stat_t).Gid != after.Sys().(*syscall.Stat_t).Gid {
		t.Fatal("config ownership changed")
	}
	assertMode(t, fixture.configPath, 0o640)
}

func TestConfigApplierRollbackSurvivesCanceledContext(t *testing.T) {
	fixture := newApplierFixture(t, []byte("mode: old\n"))
	ctx, cancel := context.WithCancel(context.Background())
	activationCalls := 0
	fixture.applier.Activate = func(callCtx context.Context) error {
		activationCalls++
		if activationCalls == 1 {
			cancel()
			return context.Canceled
		}
		if err := callCtx.Err(); err != nil {
			t.Fatalf("rollback inherited canceled context: %v", err)
		}
		return nil
	}
	fixture.applier.HealthCheck = func(callCtx context.Context) error {
		if err := callCtx.Err(); err != nil {
			t.Fatalf("rollback health check inherited canceled context: %v", err)
		}
		return nil
	}

	_, err := fixture.applier.Apply(ctx, []byte("mode: broken\n"))
	var applyErr *ApplyError
	if !errors.As(err, &applyErr) || applyErr.RollbackErr != nil {
		t.Fatalf("unexpected error: %#v", err)
	}
	assertFileContents(t, fixture.configPath, "mode: old\n")
}

func TestConfigApplierDoesNotOverwriteSameTimestampBackup(t *testing.T) {
	fixture := newApplierFixture(t, []byte("mode: zero\n"))
	fixture.applier.Activate = func(context.Context) error { return nil }
	fixture.applier.HealthCheck = func(context.Context) error { return nil }
	first, err := fixture.applier.Apply(context.Background(), []byte("mode: one\n"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := fixture.applier.Apply(context.Background(), []byte("mode: two\n"))
	if err != nil {
		t.Fatal(err)
	}
	if first.BackupPath == second.BackupPath {
		t.Fatal("second apply overwrote the first backup")
	}
	assertFileContents(t, first.BackupPath, "mode: zero\n")
	assertFileContents(t, second.BackupPath, "mode: one\n")
}

func TestConfigApplierValidationFailureLeavesOriginalUntouched(t *testing.T) {
	fixture := newApplierFixture(t, []byte("mode: old\n"))
	fixture.runner.run = func(context.Context, string, ...string) (CommandResult, error) {
		return CommandResult{Stderr: []byte("contains secret material")}, errors.New("invalid config")
	}
	fixture.applier.Activate = func(context.Context) error { t.Fatal("activate called"); return nil }
	fixture.applier.HealthCheck = func(context.Context) error { t.Fatal("health check called"); return nil }

	_, err := fixture.applier.Apply(context.Background(), []byte("mode: invalid\n"))
	var applyErr *ApplyError
	if !errors.As(err, &applyErr) || applyErr.Stage != "validation" {
		t.Fatalf("unexpected error: %#v", err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("error leaked process output: %v", err)
	}
	assertFileContents(t, fixture.configPath, "mode: old\n")
	entries, readErr := os.ReadDir(fixture.backupDir)
	if readErr == nil && len(entries) != 0 {
		t.Fatalf("validation failure created backups: %#v", entries)
	}
}

type applierFixture struct {
	applier    *ConfigApplier
	runner     *recordingRunner
	binary     string
	configDir  string
	configPath string
	backupDir  string
}

func newApplierFixture(t *testing.T, original []byte) applierFixture {
	t.Helper()
	root := t.TempDir()
	configDir := filepath.Join(root, "etc", "mihomo")
	configPath := filepath.Join(configDir, "config.yaml")
	backupDir := filepath.Join(root, "var", "backups")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, original, 0o644); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "usr", "bin", "mihomo")
	runner := &recordingRunner{run: func(_ context.Context, name string, args ...string) (CommandResult, error) {
		if name != binary {
			t.Fatalf("validation binary = %q", name)
		}
		stage := args[len(args)-1]
		assertMode(t, stage, 0o600)
		return CommandResult{}, nil
	}}
	applier := &ConfigApplier{
		Runner: runner,
		Paths: ApplyPaths{
			MihomoBinary: binary,
			ConfigPath:   configPath,
			ConfigDir:    configDir,
			BackupDir:    backupDir,
		},
		Now: func() time.Time { return time.Date(2026, 8, 23, 12, 30, 0, 0, time.UTC) },
	}
	return applierFixture{
		applier: applier, runner: runner, binary: binary,
		configDir: configDir, configPath: configPath, backupDir: backupDir,
	}
}

func assertValidationCall(t *testing.T, calls []recordedCommand, binary, configDir string) {
	t.Helper()
	if len(calls) != 1 {
		t.Fatalf("validation calls = %#v", calls)
	}
	call := calls[0]
	if call.name != binary || len(call.args) != 5 || call.args[0] != "-t" || call.args[1] != "-d" || call.args[2] != configDir || call.args[3] != "-f" {
		t.Fatalf("validation call = %#v", call)
	}
	if filepath.Dir(call.args[4]) != configDir {
		t.Fatalf("staging file %q is not in config directory", call.args[4])
	}
}

func assertFileContents(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
}
