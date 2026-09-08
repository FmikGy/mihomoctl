package platform

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
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

func TestSystemdNowActionsAreSingleCommands(t *testing.T) {
	runner := &recordingRunner{}
	manager, err := NewSystemd(runner, "mihomoctl-update.timer")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.EnableNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.DisableNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []recordedCommand{
		{name: "systemctl", args: []string{"enable", "--now", "--", "mihomoctl-update.timer"}},
		{name: "systemctl", args: []string{"disable", "--now", "--", "mihomoctl-update.timer"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("commands = %#v, want %#v", runner.calls, want)
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
		if !slicesContain(args, "--property=WorkingDirectory") {
			t.Fatalf("systemctl show did not request WorkingDirectory: %#v", args)
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

func TestDiscovererResolvesRelativeConfigFromWorkingDirectory(t *testing.T) {
	temp := t.TempDir()
	binary := filepath.Join(temp, "mihomo")
	if err := os.WriteFile(binary, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	workingDirectory := filepath.Join(temp, "runtime")
	runner := &recordingRunner{run: func(_ context.Context, name string, _ ...string) (CommandResult, error) {
		if name != "systemctl" {
			t.Fatalf("unexpected command %q", name)
		}
		return CommandResult{Stdout: []byte(
			"LoadState=loaded\n" +
				"FragmentPath=/usr/lib/systemd/system/mihomo.service\n" +
				"WorkingDirectory=!" + workingDirectory + "\n" +
				"ExecStart={ path=" + binary + " ; argv[]=" + binary + " -f active.yaml ; ignore_errors=no ; }\n",
		)}, nil
	}}
	discoverer := NewDiscoverer(runner)
	discoverer.LookPath = func(string) (string, error) { return "", errors.New("not found") }
	got, err := discoverer.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantConfig := filepath.Join(workingDirectory, "active.yaml")
	if got.ConfigDir != "" || got.ConfigPath != wantConfig {
		t.Fatalf("config paths = %q, %q, want empty dir and %q", got.ConfigDir, got.ConfigPath, wantConfig)
	}
}

func slicesContain(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
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
	assertMode(t, fixture.configPath, 0o644)
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

func TestConfigApplierPreservesRequiredReadAccess(t *testing.T) {
	for _, mode := range []os.FileMode{0o640, 0o644} {
		t.Run(fmt.Sprintf("%o", mode), func(t *testing.T) {
			fixture := newApplierFixture(t, []byte("mode: old\n"))
			if err := os.Chmod(fixture.configPath, mode); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(fixture.configPath)
			if err != nil {
				t.Fatal(err)
			}
			fixture.applier.Activate = func(context.Context) error {
				assertMode(t, fixture.configPath, mode)
				return nil
			}
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
			assertMode(t, fixture.configPath, mode)
		})
	}
}

func TestManagedConfigMetadataPreservesOnlyRequiredAccess(t *testing.T) {
	metadata := managedConfigMetadata(fileMetadata{mode: 0o677, uid: os.Geteuid(), gid: os.Getegid()})
	if metadata.mode.Perm() != 0o644 {
		t.Fatalf("managed mode for current owner = %o, want 644", metadata.mode.Perm())
	}
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

func TestConfigApplierInternalRollbackGetsFreshWindowAfterApplyDeadline(t *testing.T) {
	fixture := newApplierFixture(t, []byte("mode: old\n"))
	fixture.applier.RollbackTimeout = time.Second
	activationCalls := 0
	fixture.applier.Activate = func(callCtx context.Context) error {
		activationCalls++
		if activationCalls == 1 {
			<-callCtx.Done()
			return callCtx.Err()
		}
		if err := callCtx.Err(); err != nil {
			t.Fatalf("internal rollback reused expired apply deadline: %v", err)
		}
		return nil
	}
	fixture.applier.HealthCheck = func(callCtx context.Context) error {
		if err := callCtx.Err(); err != nil {
			t.Fatalf("rollback health check reused expired apply deadline: %v", err)
		}
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	_, err := fixture.applier.Apply(ctx, []byte("mode: broken\n"))
	var applyErr *ApplyError
	if !errors.As(err, &applyErr) || !errors.Is(applyErr.Cause, context.DeadlineExceeded) || applyErr.RollbackErr != nil {
		t.Fatalf("unexpected apply error: %#v", err)
	}
	if activationCalls != 2 {
		t.Fatalf("activation calls = %d, want 2", activationCalls)
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

func TestConfigApplierCanRollbackSuccessfulApply(t *testing.T) {
	fixture := newApplierFixture(t, []byte("mode: old\n"))
	ctx, cancel := context.WithCancel(context.Background())
	activationCalls := 0
	fixture.applier.Activate = func(callCtx context.Context) error {
		activationCalls++
		if activationCalls == 2 && callCtx.Err() != nil {
			t.Fatalf("explicit rollback inherited canceled context: %v", callCtx.Err())
		}
		return nil
	}
	fixture.applier.HealthCheck = func(callCtx context.Context) error {
		if callCtx.Err() != nil {
			t.Fatalf("health check received canceled context: %v", callCtx.Err())
		}
		return nil
	}

	result, err := fixture.applier.Apply(ctx, []byte("mode: new\n"))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := fixture.applier.Rollback(ctx, result); err != nil {
		t.Fatal(err)
	}
	if activationCalls != 2 {
		t.Fatalf("activation calls = %d", activationCalls)
	}
	assertFileContents(t, fixture.configPath, "mode: old\n")
	assertMode(t, fixture.configPath, 0o644)
}

func TestConfigApplierRollbackKeepsLiveCallerDeadline(t *testing.T) {
	fixture := newApplierFixture(t, []byte("mode: old\n"))
	fixture.applier.Activate = func(context.Context) error { return nil }
	fixture.applier.HealthCheck = func(context.Context) error { return nil }
	result, err := fixture.applier.Apply(context.Background(), []byte("mode: new\n"))
	if err != nil {
		t.Fatal(err)
	}

	fixture.applier.RollbackTimeout = time.Second
	fixture.applier.Activate = func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	started := time.Now()
	err = fixture.applier.Rollback(ctx, result)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("rollback error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 300*time.Millisecond {
		t.Fatalf("rollback reset the caller deadline: %s", elapsed)
	}
}

func TestConfigApplierRollbackDoesNotRenewExpiredDeadline(t *testing.T) {
	fixture := newApplierFixture(t, []byte("mode: old\n"))
	fixture.applier.Activate = func(context.Context) error { return nil }
	fixture.applier.HealthCheck = func(context.Context) error { return nil }
	result, err := fixture.applier.Apply(context.Background(), []byte("mode: new\n"))
	if err != nil {
		t.Fatal(err)
	}

	fixture.applier.RollbackTimeout = time.Second
	fixture.applier.Activate = func(ctx context.Context) error {
		return ctx.Err()
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Millisecond))
	defer cancel()
	started := time.Now()
	err = fixture.applier.Rollback(ctx, result)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("rollback error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("rollback renewed an expired deadline: %s", elapsed)
	}
}

func TestConfigApplierRejectsStaleRollback(t *testing.T) {
	fixture := newApplierFixture(t, []byte("mode: old\n"))
	fixture.applier.Activate = func(context.Context) error { return nil }
	fixture.applier.HealthCheck = func(context.Context) error { return nil }
	result, err := fixture.applier.Apply(context.Background(), []byte("mode: applied\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.configPath, []byte("mode: newer\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := fixture.applier.Rollback(context.Background(), result); err == nil || !strings.Contains(err.Error(), "changed after apply") {
		t.Fatalf("stale rollback error = %v", err)
	}
	assertFileContents(t, fixture.configPath, "mode: newer\n")
}

func TestConfigApplierRejectsSymlinkBackupDirectory(t *testing.T) {
	fixture := newApplierFixture(t, []byte("mode: old\n"))
	target := filepath.Join(t.TempDir(), "target")
	if err := os.Mkdir(target, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(fixture.backupDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, fixture.backupDir); err != nil {
		t.Fatal(err)
	}
	fixture.applier.Activate = func(context.Context) error { t.Fatal("activate called"); return nil }
	fixture.applier.HealthCheck = func(context.Context) error { t.Fatal("health check called"); return nil }
	_, err := fixture.applier.Apply(context.Background(), []byte("mode: new\n"))
	var applyErr *ApplyError
	if !errors.As(err, &applyErr) || applyErr.Stage != "backup" {
		t.Fatalf("symlink backup error = %v", err)
	}
	if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0o750 {
		t.Fatalf("symlink target mode changed: info=%v err=%v", info, err)
	}
	assertFileContents(t, fixture.configPath, "mode: old\n")
}

func TestSecureBackupDirectoryRejectsFilesystemRoot(t *testing.T) {
	info, err := os.Stat(string(os.PathSeparator))
	if err != nil {
		t.Fatal(err)
	}
	before := info.Mode().Perm()
	if err := secureBackupDirectory(string(os.PathSeparator)); err == nil {
		t.Fatal("filesystem root was accepted as a backup directory")
	}
	info, err = os.Stat(string(os.PathSeparator))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != before {
		t.Fatalf("filesystem root mode changed from %o to %o", before, info.Mode().Perm())
	}
}

func TestManagedBackupSequenceOrderingAndCanonicalNames(t *testing.T) {
	fixture := newApplierFixture(t, []byte("mode: old\n"))
	if err := os.MkdirAll(fixture.backupDir, 0o700); err != nil {
		t.Fatal(err)
	}
	base := "config.yaml.20260823T123000.000000000Z"
	for _, suffix := range []string{".bak", ".2.bak", ".10.bak", ".01.bak", ".+1.bak"} {
		if err := os.WriteFile(filepath.Join(fixture.backupDir, base+suffix), []byte(suffix), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := fixture.applier.pruneBackups(2, ""); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{".2.bak", ".10.bak", ".01.bak", ".+1.bak"} {
		if _, err := os.Stat(filepath.Join(fixture.backupDir, base+suffix)); err != nil {
			t.Fatalf("backup %s was unexpectedly removed: %v", suffix, err)
		}
	}
	if managedBackupName("config.yaml", base+".01.bak") || managedBackupName("config.yaml", base+".+1.bak") {
		t.Fatal("non-canonical sequence was accepted")
	}
	if _, err := os.Stat(filepath.Join(fixture.backupDir, base+".bak")); !os.IsNotExist(err) {
		t.Fatalf("oldest canonical backup remains: %v", err)
	}
}

func TestCreateBackupExclusiveHandlesConcurrentSameTimestamp(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "config.yaml")
	backups := filepath.Join(directory, "backups")
	if err := os.WriteFile(source, []byte("mode: source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(backups, 0o700); err != nil {
		t.Fatal(err)
	}
	const workers = 16
	paths := make(chan string, workers)
	errs := make(chan error, workers)
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			path, err := createBackupExclusive(source, backups, "config.yaml.20260823T123000.000000000Z")
			paths <- path
			errs <- err
		}()
	}
	wait.Wait()
	close(paths)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	unique := make(map[string]struct{}, workers)
	for path := range paths {
		unique[path] = struct{}{}
		assertFileContents(t, path, "mode: source\n")
	}
	if len(unique) != workers {
		t.Fatalf("unique backup paths = %d, want %d", len(unique), workers)
	}
}

func TestCreateBackupExclusiveRejectsSymlinkSource(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target.yaml")
	source := filepath.Join(directory, "config.yaml")
	backups := filepath.Join(directory, "backups")
	if err := os.WriteFile(target, []byte("mode: target\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, source); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(backups, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := createBackupExclusive(source, backups, "config.yaml.20260823T123000.000000000Z"); err == nil {
		t.Fatal("symlink source was accepted")
	}
	entries, err := os.ReadDir(backups)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("backup was created from symlink source: %#v", entries)
	}
}

func TestConfigApplierRetainsOnlyLatestManagedBackups(t *testing.T) {
	fixture := newApplierFixture(t, []byte("mode: old\n"))
	if err := os.MkdirAll(fixture.backupDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 25; index++ {
		name := fmt.Sprintf("config.yaml.20260823T1229%02d.000000000Z.bak", index)
		path := filepath.Join(fixture.backupDir, name)
		if err := os.WriteFile(path, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
		stamp := time.Unix(int64(index+1), 0)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	unrelated := filepath.Join(fixture.backupDir, "config.yaml.not-managed.bak")
	if err := os.WriteFile(unrelated, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(fixture.backupDir, "symlink-target")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(fixture.backupDir, "config.yaml.20260823T122959.000000000Z.bak")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	fixture.applier.Activate = func(context.Context) error { return nil }
	fixture.applier.HealthCheck = func(context.Context) error { return nil }

	if _, err := fixture.applier.Apply(context.Background(), []byte("mode: new\n")); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(fixture.backupDir)
	if err != nil {
		t.Fatal(err)
	}
	managed := 0
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink == 0 && managedBackupName("config.yaml", entry.Name()) {
			managed++
		}
	}
	if managed != DefaultBackupRetention {
		t.Fatalf("managed backup count = %d, want %d", managed, DefaultBackupRetention)
	}
	if _, err := os.Lstat(unrelated); err != nil {
		t.Fatalf("unrelated file was removed: %v", err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("managed-looking symlink was changed: info=%v err=%v", info, err)
	}
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

func TestConfigApplierDoesNotFollowReplacedStagingPath(t *testing.T) {
	fixture := newApplierFixture(t, []byte("mode: old\n"))
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("do not touch"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixture.runner.run = func(_ context.Context, _ string, args ...string) (CommandResult, error) {
		stage := args[len(args)-1]
		moved := stage + ".moved"
		if err := os.Rename(stage, moved); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Remove(moved) })
		if err := os.Symlink(target, stage); err != nil {
			t.Fatal(err)
		}
		return CommandResult{}, nil
	}
	fixture.applier.Activate = func(context.Context) error { t.Fatal("activate called"); return nil }
	fixture.applier.HealthCheck = func(context.Context) error { t.Fatal("health check called"); return nil }
	_, err := fixture.applier.Apply(context.Background(), []byte("mode: new\n"))
	var applyErr *ApplyError
	if !errors.As(err, &applyErr) || applyErr.Stage != "permissions" {
		t.Fatalf("replaced staging path error = %v", err)
	}
	assertFileContents(t, fixture.configPath, "mode: old\n")
	assertFileContents(t, target, "do not touch")
	assertMode(t, target, 0o644)
}

func TestConfigApplierRejectsNonCanonicalPaths(t *testing.T) {
	fixture := newApplierFixture(t, []byte("mode: old\n"))
	paths := []struct {
		name   string
		mutate func(*ApplyPaths)
	}{
		{name: "binary", mutate: func(paths *ApplyPaths) {
			paths.MihomoBinary = filepath.Dir(paths.MihomoBinary) + "/sub/../" + filepath.Base(paths.MihomoBinary)
		}},
		{name: "config", mutate: func(paths *ApplyPaths) {
			paths.ConfigPath = filepath.Dir(paths.ConfigPath) + "/sub/../" + filepath.Base(paths.ConfigPath)
		}},
		{name: "config directory", mutate: func(paths *ApplyPaths) { paths.ConfigDir += string(os.PathSeparator) }},
		{name: "backup directory", mutate: func(paths *ApplyPaths) { paths.BackupDir += string(os.PathSeparator) }},
	}
	for _, test := range paths {
		t.Run(test.name, func(t *testing.T) {
			applier := *fixture.applier
			test.mutate(&applier.Paths)
			if _, err := applier.Apply(context.Background(), []byte("mode: new\n")); err == nil || !strings.Contains(err.Error(), "canonical") {
				t.Fatalf("non-canonical path error = %v", err)
			}
		})
	}
	if len(fixture.runner.calls) != 0 {
		t.Fatalf("validation runner was called: %#v", fixture.runner.calls)
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
	if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
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
