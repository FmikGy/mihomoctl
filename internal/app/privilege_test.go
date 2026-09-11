package app

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"mihomoctl/internal/domain"
	"mihomoctl/internal/i18n"
	"mihomoctl/internal/platform"
	"mihomoctl/internal/profile"
)

type recordingPrivilegeExecutor struct {
	calls     int
	command   PrivilegeCommand
	exitCode  int
	err       error
	stdout    string
	stderr    string
	readStdin bool
	stdin     string
}

func (e *recordingPrivilegeExecutor) Run(_ context.Context, command PrivilegeCommand) (int, error) {
	e.calls++
	command.Args = append([]string(nil), command.Args...)
	command.Env = append([]string(nil), command.Env...)
	e.command = command
	if e.readStdin && command.Stdin != nil {
		content, _ := io.ReadAll(command.Stdin)
		e.stdin = string(content)
	}
	if e.stderr != "" && command.Stderr != nil {
		_, _ = io.WriteString(command.Stderr, e.stderr)
	}
	if e.stdout != "" && command.Stdout != nil {
		_, _ = io.WriteString(command.Stdout, e.stdout)
	}
	return e.exitCode, e.err
}

func privilegeTestApp(executor PrivilegeExecutor, euid int) *App {
	paths := defaultElevatedPaths()
	return &App{
		paths:               paths,
		privilegeExecutor:   executor,
		euid:                func() int { return euid },
		executable:          func() (string, error) { return "/usr/bin/mihomoctl", nil },
		executableValidator: func(path string) (string, error) { return path, nil },
		sudoPath:            func() (string, error) { return "/usr/bin/sudo", nil },
	}
}

func expectedPrivilegeArgs(nonInteractive bool, preserve []string, args ...string) []string {
	result := make([]string, 0, len(args)+6)
	if nonInteractive {
		result = append(result, "-n")
	}
	if len(preserve) > 0 {
		result = append(result, "--preserve-env="+strings.Join(preserve, ","))
	}
	result = append(result, "--", "/usr/bin/mihomoctl", "--lang", "zh")
	return append(result, args...)
}

func TestRunElevatedUsesNonInteractiveSudoForTUIAttempt(t *testing.T) {
	executor := &recordingPrivilegeExecutor{}
	application := privilegeTestApp(executor, 1000)
	ctx := platform.WithNonInteractiveElevation(context.Background())

	if err := application.runElevated(ctx, []string{"service", "start", "--output", "json"}, ""); err != nil {
		t.Fatal(err)
	}
	_, preserve, err := elevatedEnvironment(application.paths)
	if err != nil {
		t.Fatal(err)
	}
	want := expectedPrivilegeArgs(true, preserve, "service", "start", "--output", "json")
	if executor.command.Name != "/usr/bin/sudo" || !reflect.DeepEqual(executor.command.Args, want) {
		t.Fatalf("command = %q %#v, want /usr/bin/sudo %#v", executor.command.Name, executor.command.Args, want)
	}
	if executor.command.Stdin != nil {
		t.Fatal("non-interactive sudo inherited terminal input")
	}
	wantEnv, _, err := elevatedEnvironment(application.paths)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(executor.command.Env, wantEnv) {
		t.Fatalf("command environment = %#v, want %#v", executor.command.Env, wantEnv)
	}
}

func TestRunElevatedPropagatesInvocationLanguage(t *testing.T) {
	executor := &recordingPrivilegeExecutor{}
	application := privilegeTestApp(executor, 1000)
	ctx := platform.WithNonInteractiveElevation(i18n.WithLanguage(context.Background(), i18n.English))
	if err := application.runElevated(ctx, []string{"service", "start"}, ""); err != nil {
		t.Fatal(err)
	}
	if got := executor.command.Args; len(got) < 6 || got[3] != "--lang" || got[4] != "en" {
		t.Fatalf("elevated arguments did not propagate English: %#v", got)
	}
}

func TestRunElevatedInteractiveRetryUsesAttachedTerminalInput(t *testing.T) {
	executor := &recordingPrivilegeExecutor{readStdin: true}
	application := privilegeTestApp(executor, 1000)
	ctx := platform.WithInteractiveElevation(context.Background())
	ctx = platform.WithElevationInput(ctx, strings.NewReader("terminal-input"))

	if err := application.runElevated(ctx, []string{"tun", "on"}, ""); err != nil {
		t.Fatal(err)
	}
	if executor.stdin != "terminal-input" {
		t.Fatalf("stdin = %q, want attached terminal input", executor.stdin)
	}
	if len(executor.command.Args) == 0 || executor.command.Args[0] == "-n" {
		t.Fatalf("interactive command unexpectedly used -n: %#v", executor.command.Args)
	}
}

func TestRunElevatedKeepsSensitiveProfileSourceOnStdin(t *testing.T) {
	const source = "https://example.test/subscribe?token=must-not-leak"
	executor := &recordingPrivilegeExecutor{readStdin: true}
	application := privilegeTestApp(executor, 1000)
	ctx := platform.WithNonInteractiveElevation(context.Background())

	if err := application.runElevated(ctx, []string{"profile", "add", "-"}, source); err != nil {
		t.Fatal(err)
	}
	if executor.stdin != source {
		t.Fatalf("stdin was not the profile source: %q", executor.stdin)
	}
	if strings.Contains(strings.Join(executor.command.Args, " "), source) || strings.Contains(strings.Join(executor.command.Args, " "), "must-not-leak") {
		t.Fatalf("command arguments exposed profile source: %#v", executor.command.Args)
	}
}

func TestRunElevatedRedactsProfileSourceFromChildError(t *testing.T) {
	const source = "https://user:password@example.test/subscribe?token=must-not-leak"
	executor := &recordingPrivilegeExecutor{
		exitCode: 1,
		err:      errors.New("child failed"),
		stderr:   "mihomoctl: request token must-not-leak failed for " + source + "\n",
	}
	application := privilegeTestApp(executor, 1000)
	err := application.runElevated(context.Background(), []string{"profile", "add", "-"}, source)
	if err == nil {
		t.Fatal("expected child error")
	}
	for _, secret := range []string{source, "must-not-leak", "password"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("child error exposed %q: %v", secret, err)
		}
	}
}

func TestProtectedApplicationMethodsUseExpectedElevatedArguments(t *testing.T) {
	tests := []struct {
		name string
		run  func(*App, context.Context) error
		want []string
	}{
		{name: "service", run: func(a *App, ctx context.Context) error { return a.Service(ctx, "restart") }, want: []string{"service", "restart", "--output", "json"}},
		{name: "service enable now", run: func(a *App, ctx context.Context) error { return a.Service(ctx, "enable-now") }, want: []string{"service", "enable", "--now", "--output", "json"}},
		{name: "mode", run: func(a *App, ctx context.Context) error { return a.SetMode(ctx, domain.ModeGlobal) }, want: []string{"mode", "global", "--output", "json"}},
		{name: "tun", run: func(a *App, ctx context.Context) error { return a.SetTUN(ctx, true) }, want: []string{"tun", "on", "--output", "json"}},
		{name: "config", run: func(a *App, ctx context.Context) error { return a.SetConfig(ctx, "allow-lan", "on") }, want: []string{"--output", "json", "config", "set", "--", "allow-lan", "on"}},
		{name: "schedule", run: func(a *App, ctx context.Context) error { return a.SetSchedule(ctx, false) }, want: []string{"schedule", "disable", "--output", "json"}},
		{name: "profile update", run: func(a *App, ctx context.Context) error { return a.UpdateProfile(ctx, "daily") }, want: []string{"--output", "json", "profile", "update", "--", "daily"}},
		{name: "profile update all", run: func(a *App, ctx context.Context) error {
			return a.UpdateProfiles(ctx, domain.ProfileUpdateOptions{All: true})
		}, want: []string{"--output", "json", "profile", "update", "--all"}},
		{name: "profile update due", run: func(a *App, ctx context.Context) error {
			return a.UpdateProfiles(ctx, domain.ProfileUpdateOptions{Due: true})
		}, want: []string{"--output", "json", "profile", "update", "--due"}},
		{name: "profile use", run: func(a *App, ctx context.Context) error { return a.UseProfile(ctx, "daily") }, want: []string{"--output", "json", "profile", "use", "--", "daily"}},
		{name: "profile remove", run: func(a *App, ctx context.Context) error { return a.RemoveProfile(ctx, "old") }, want: []string{"--output", "json", "profile", "remove", "--", "old"}},
		{name: "profile add", run: func(a *App, ctx context.Context) error {
			_, err := a.AddProfile(ctx, "daily", "https://example.test/sub", time.Hour)
			return err
		}, want: []string{"profile", "add", "-", "--interval", "1h0m0s", "--output", "json", "--name", "daily"}},
		{name: "initialize", run: func(a *App, ctx context.Context) error {
			return a.Initialize(ctx, domain.InitOptions{
				Controller: "127.0.0.1:9090", Force: true,
			})
		}, want: []string{"init", "--output", "json", "--controller", "127.0.0.1:9090", "--force"}},
		{name: "public state sync", run: func(a *App, ctx context.Context) error {
			return a.SyncPublicState(ctx)
		}, want: []string{"--output", "json", "config", "sync-public-state"}},
		{name: "client state sync", run: func(a *App, ctx context.Context) error {
			return a.SyncClientState(ctx)
		}, want: []string{"--output", "json", "config", "sync-client"}},
		{name: "doctor fix", run: func(a *App, ctx context.Context) error {
			_, err := a.Doctor(ctx, true)
			return err
		}, want: []string{"doctor", "--fix", "--output", "json"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executor := &recordingPrivilegeExecutor{}
			if test.name == "profile add" {
				executor.stdout = `{"schema_version":1,"data":{"id":"profile-id","name":"daily","kind":"remote","active":false,"update_interval":"1h0m0s"}}`
			}
			application := privilegeTestApp(executor, 1000)
			application.client = &clientState{}
			application.runner = &fakeRunner{}
			ctx := platform.WithNonInteractiveElevation(context.Background())
			if err := test.run(application, ctx); err != nil {
				t.Fatal(err)
			}
			_, preserve, err := elevatedEnvironment(application.paths)
			if err != nil {
				t.Fatal(err)
			}
			want := expectedPrivilegeArgs(true, preserve, test.want...)
			if executor.calls != 1 || !reflect.DeepEqual(executor.command.Args, want) {
				t.Fatalf("calls=%d args=%#v, want %#v", executor.calls, executor.command.Args, want)
			}
		})
	}
}

func TestServiceRequiresManagedInstallationBeforeElevation(t *testing.T) {
	executor := &recordingPrivilegeExecutor{}
	application := privilegeTestApp(executor, 1000)
	application.paths.PublicFile = filepath.Join(t.TempDir(), "missing-public.json")

	err := application.Service(context.Background(), "restart")
	if !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("uninitialized service error = %T %v, want ErrNotInitialized", err, err)
	}
	if executor.calls != 0 {
		t.Fatalf("uninitialized service invoked sudo %d times", executor.calls)
	}
}

func TestProtectedOperationIgnoresPrivateRootStateReadError(t *testing.T) {
	executor := &recordingPrivilegeExecutor{}
	application := privilegeTestApp(executor, 1000)
	application.runner = &fakeRunner{}
	application.client = &clientState{Version: stateVersion, Controller: "127.0.0.1:9090"}
	application.stateLoadErr = &os.PathError{Op: "open", Path: "/etc/mihomoctl/config.yaml", Err: os.ErrPermission}

	if err := application.Service(context.Background(), "restart"); err != nil {
		t.Fatalf("ordinary-user protected operation rejected private root state: %v", err)
	}
	if executor.calls != 1 {
		t.Fatalf("ordinary-user protected operation invoked sudo %d times, want 1", executor.calls)
	}
	checks, err := application.Doctor(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range checks {
		if check.Name == "mihomoctl 初始化" {
			if !check.OK {
				t.Fatalf("doctor treated private root state as ordinary-user failure: %#v", check)
			}
			return
		}
	}
	t.Fatal("doctor omitted mihomoctl initialization check")
}

func TestInitializeRejectsPathOverridesAcrossAutomaticElevation(t *testing.T) {
	tests := []domain.InitOptions{
		{Service: "ssh.service"},
		{ConfigPath: "/etc/shadow"},
	}
	for _, options := range tests {
		executor := &recordingPrivilegeExecutor{}
		application := privilegeTestApp(executor, 1000)
		err := application.Initialize(context.Background(), options)
		var invalid *InvalidInputError
		if !errors.As(err, &invalid) {
			t.Fatalf("Initialize(%#v) error = %T %v, want InvalidInputError", options, err, err)
		}
		if executor.calls != 0 {
			t.Fatalf("Initialize(%#v) invoked sudo %d times", options, executor.calls)
		}
	}
	for _, options := range tests {
		if err := validateInitOverrides(true, true, options); err == nil {
			t.Fatalf("sudo child accepted init override %#v", options)
		}
		if err := validateInitOverrides(true, false, options); err != nil {
			t.Fatalf("direct root rejected init override %#v: %v", options, err)
		}
	}
}

func TestAddLocalProfileSnapshotsBeforeElevation(t *testing.T) {
	const config = "mixed-port: 7890\nproxies: []\nproxy-groups: []\nrules:\n  - MATCH,DIRECT\n"
	path := filepath.Join(t.TempDir(), "local-profile.yaml")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	executor := &recordingPrivilegeExecutor{
		readStdin: true,
		stdout:    `{"schema_version":1,"data":{"id":"profile-id","name":"local-profile","kind":"local","active":false,"update_interval":"0s"}}`,
	}
	application := privilegeTestApp(executor, 1000)
	application.client = &clientState{}

	created, err := application.AddProfile(context.Background(), "", path, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if created.Name != "local-profile" || created.Kind != domain.ProfileLocal || created.UpdateInterval != 0 {
		t.Fatalf("elevated profile result = %#v", created)
	}
	if executor.calls != 1 || !strings.HasPrefix(executor.stdin, profile.InlineSnapshotPrefix) {
		t.Fatalf("local profile was not sent as a snapshot: calls=%d stdin prefix=%t", executor.calls, strings.HasPrefix(executor.stdin, profile.InlineSnapshotPrefix))
	}
	if strings.Contains(executor.stdin, path) || !strings.Contains(executor.stdin, config) {
		t.Fatalf("snapshot stdin contains path or lost content: %q", executor.stdin)
	}
	joinedArgs := strings.Join(executor.command.Args, "\n")
	if strings.Contains(joinedArgs, path) || !strings.Contains(joinedArgs, "local-profile") {
		t.Fatalf("elevated arguments expose path or lack derived name: %#v", executor.command.Args)
	}
	if err := validateLocalProfilePrivilege(true, domain.ProfileLocal); err == nil {
		t.Fatal("sudo child accepted a direct local profile path")
	}
	if err := validateLocalProfilePrivilege(true, domain.ProfileRemote); err != nil {
		t.Fatalf("sudo child rejected a remote profile: %v", err)
	}
}

func TestRunElevatedClassifiesSudoFailures(t *testing.T) {
	tests := []struct {
		name              string
		stderr            string
		nonInteractive    bool
		want              string
		wantAuthorization bool
	}{
		{name: "authorization required", stderr: "sudo: a password is required\n", nonInteractive: true, want: "sudo 需要身份验证", wantAuthorization: true},
		{name: "interactive password missing", stderr: "sudo: a password is required\n", want: "sudo 身份验证未完成，操作未执行"},
		{name: "authentication failed", stderr: "sudo: 3 incorrect password attempts\n", want: "sudo 身份验证未完成，操作未执行"},
		{name: "policy denied", stderr: "sudo: user is not in the sudoers file\n", want: "当前用户没有执行此操作的 sudo 权限"},
		{name: "policy denied without prefix", stderr: "Sorry, user fm is not allowed to execute this command.\n", want: "当前用户没有执行此操作的 sudo 权限"},
		{name: "environment preservation denied", stderr: "sudo: sorry, you are not allowed to preserve the environment\n", want: "当前用户没有执行此操作的 sudo 权限"},
		{name: "no terminal", stderr: "sudo: a terminal is required to read the password\n", want: "sudo 需要可交互终端"},
		{name: "multi-line no terminal", stderr: "sudo: a terminal is required to read the password\nsudo: a password is required\n", want: "sudo 需要可交互终端"},
		{name: "unknown", stderr: "sudo: policy plugin failed\n", want: "sudo 提权失败：policy plugin failed"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executor := &recordingPrivilegeExecutor{exitCode: 1, err: errors.New("sudo failed"), stderr: test.stderr}
			application := privilegeTestApp(executor, 1000)
			ctx := context.Background()
			if test.nonInteractive {
				ctx = platform.WithNonInteractiveElevation(ctx)
			}
			err := application.runElevated(ctx, []string{"service", "start"}, "")
			var privilegeErr *PrivilegeError
			if !errors.As(err, &privilegeErr) {
				t.Fatalf("error = %T %v, want PrivilegeError", err, err)
			}
			if err.Error() != test.want || privilegeErr.ExitCode() != 3 || privilegeErr.AuthorizationRequired() != test.wantAuthorization {
				t.Fatalf("error = %q, code=%d, authorization=%v", err, privilegeErr.ExitCode(), privilegeErr.AuthorizationRequired())
			}
		})
	}
}

func TestRunElevatedReportsMissingSudo(t *testing.T) {
	executor := &recordingPrivilegeExecutor{exitCode: -1, err: exec.ErrNotFound}
	application := privilegeTestApp(executor, 1000)
	err := application.runElevated(context.Background(), []string{"service", "start"}, "")
	var privilegeErr *PrivilegeError
	if !errors.As(err, &privilegeErr) || err.Error() != "未找到可信的 sudo，请安装系统 sudo 或以 root 运行" || privilegeErr.ExitCode() != 3 {
		t.Fatalf("missing sudo error = %T %v", err, err)
	}
}

func TestRunElevatedDoesNotRunWhenSudoPathIsUntrusted(t *testing.T) {
	executor := &recordingPrivilegeExecutor{}
	application := privilegeTestApp(executor, 1000)
	application.sudoPath = func() (string, error) {
		return "", errors.New("sudo path is writable")
	}

	err := application.runElevated(context.Background(), []string{"service", "start"}, "")
	var privilegeErr *PrivilegeError
	if !errors.As(err, &privilegeErr) || err.Error() != "未找到可信的 sudo，请安装系统 sudo 或以 root 运行" {
		t.Fatalf("untrusted sudo error = %T %v", err, err)
	}
	if executor.calls != 0 {
		t.Fatalf("executor calls = %d, want 0", executor.calls)
	}
}

func TestValidateTrustedExecutableRejectsUserOwnedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sudo")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := validateTrustedExecutable(path); err == nil {
		t.Fatal("user-owned sudo was accepted as trusted")
	}
}

func TestRunElevatedRejectsUserWritableCurrentExecutable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mihomoctl")
	if err := os.WriteFile(path, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	executor := &recordingPrivilegeExecutor{}
	application := privilegeTestApp(executor, 1000)
	application.executable = func() (string, error) { return path, nil }
	application.executableValidator = validateTrustedExecutable

	err := application.runElevated(context.Background(), []string{"service", "start"}, "")
	var privilegeErr *PrivilegeError
	if !errors.As(err, &privilegeErr) || err.Error() != "当前 mihomoctl 不是可信的系统安装版本，请先安装后再执行管理员操作" {
		t.Fatalf("untrusted executable error = %T %v", err, err)
	}
	if executor.calls != 0 {
		t.Fatalf("executor calls = %d, want 0", executor.calls)
	}
}

func TestRunElevatedPreservesElevatedChildExitCode(t *testing.T) {
	executor := &recordingPrivilegeExecutor{exitCode: 2, err: errors.New("child failed"), stderr: "mihomoctl: 配置值无效\n"}
	application := privilegeTestApp(executor, 1000)
	err := application.runElevated(context.Background(), []string{"config", "set"}, "")
	var coded interface{ ExitCode() int }
	var privilegeErr *PrivilegeError
	if err == nil || !errors.As(err, &coded) || coded.ExitCode() != 2 || errors.As(err, &privilegeErr) || err.Error() != "配置值无效" {
		t.Fatalf("child error = %T %v", err, err)
	}
}

func TestRunElevatedReturnsSuccessfulChildWarning(t *testing.T) {
	executor := &recordingPrivilegeExecutor{stdout: `{"schema_version":1,"data":{"action":"use","warning":"部分节点未恢复"}}`}
	application := privilegeTestApp(executor, 1000)
	err := application.runElevated(context.Background(), []string{"profile", "use"}, "")
	var warning interface{ Warning() bool }
	if !errors.As(err, &warning) || !warning.Warning() || err.Error() != "部分节点未恢复" {
		t.Fatalf("elevated warning = %T %v", err, err)
	}
}

func TestSilentElevatedChildFailureDoesNotTriggerAuthorizationRetry(t *testing.T) {
	executor := &recordingPrivilegeExecutor{exitCode: 1, err: errors.New("child failed")}
	application := privilegeTestApp(executor, 1000)
	ctx := platform.WithNonInteractiveElevation(context.Background())
	err := application.runElevated(ctx, []string{"service", "start"}, "")
	var coded interface{ ExitCode() int }
	var privilegeErr *PrivilegeError
	if err == nil || !errors.As(err, &coded) || coded.ExitCode() != 1 || errors.As(err, &privilegeErr) {
		t.Fatalf("silent child error = %T %v", err, err)
	}
}

func TestRunElevatedNormalizesUnexpectedChildExitCode(t *testing.T) {
	executor := &recordingPrivilegeExecutor{exitCode: 7, err: errors.New("child failed"), stderr: "mihomoctl: 子进程异常退出\n"}
	application := privilegeTestApp(executor, 1000)
	err := application.runElevated(context.Background(), []string{"service", "start"}, "")
	var coded interface{ ExitCode() int }
	var privilegeErr *PrivilegeError
	if !errors.As(err, &coded) || coded.ExitCode() != 1 || errors.As(err, &privilegeErr) || err.Error() != "子进程异常退出" {
		t.Fatalf("unexpected child exit = %T %v", err, err)
	}
}

func TestRunElevatedDoesNotInvokeExecutorAsRoot(t *testing.T) {
	executor := &recordingPrivilegeExecutor{}
	application := privilegeTestApp(executor, 0)
	if err := application.runElevated(context.Background(), []string{"service", "start"}, ""); err == nil {
		t.Fatal("root runElevated unexpectedly succeeded")
	}
	if executor.calls != 0 {
		t.Fatalf("executor calls = %d, want 0", executor.calls)
	}
}

func TestRunElevatedPreservesContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	executor := &recordingPrivilegeExecutor{exitCode: -1, err: context.Canceled}
	application := privilegeTestApp(executor, 1000)
	err := application.runElevated(ctx, []string{"service", "start"}, "")
	var privilegeErr *PrivilegeError
	if !errors.Is(err, context.Canceled) || errors.As(err, &privilegeErr) {
		t.Fatalf("cancellation became %T %v", err, err)
	}
}

func TestSanitizeElevatedErrorIsControlAndRuneSafe(t *testing.T) {
	got := sanitizeElevatedError("ignored\n" + strings.Repeat("界", 300) + "\x00")
	if !utf8.ValidString(got) || utf8.RuneCountInString(got) != 240 || !strings.HasSuffix(got, "…") {
		t.Fatalf("sanitized value is not a valid 240-rune summary: %q", got)
	}
	if strings.ContainsRune(got, '\x00') {
		t.Fatalf("sanitized value contains a control character: %q", got)
	}
}

func TestElevationEnvironmentDoesNotInheritUserControlledValues(t *testing.T) {
	got := elevationEnvironment([]string{
		"PATH=/tmp/fake-bin", "MIHOMOCTL_CONFIG=/tmp/config.yaml", "LD_PRELOAD=/tmp/inject.so",
		"SUDO_ASKPASS=/tmp/prompt", "LC_ALL=zh_CN.UTF-8",
	})
	want := []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("environment = %#v, want %#v", got, want)
	}
}

func TestRunElevatedPassesOnlyCallerOwnedClientOverride(t *testing.T) {
	executor := &recordingPrivilegeExecutor{}
	application := privilegeTestApp(executor, 1000)
	if application.paths.ClientFile == "" {
		t.Skip("current user account is unavailable")
	}
	application.paths.ClientFile = application.paths.ClientFile + ".alternate"
	if err := application.runElevated(context.Background(), []string{"service", "restart"}, ""); err != nil {
		t.Fatal(err)
	}
	wantEnvironment := append(elevationEnvironment(nil), clientConfigEnvironment+"="+application.paths.ClientFile)
	if !reflect.DeepEqual(executor.command.Env, wantEnvironment) {
		t.Fatalf("command environment = %#v, want %#v", executor.command.Env, wantEnvironment)
	}
	wantOption := "--preserve-env=" + clientConfigEnvironment
	if len(executor.command.Args) == 0 || executor.command.Args[0] != wantOption {
		t.Fatalf("sudo arguments = %#v, want first argument %q", executor.command.Args, wantOption)
	}
}

func TestRunElevatedDefaultPathsDoNotRequireEnvironmentPermission(t *testing.T) {
	executor := &recordingPrivilegeExecutor{}
	application := privilegeTestApp(executor, 1000)
	application.paths = defaultElevatedPaths()
	if application.paths.ClientFile == "" {
		t.Skip("current user account is unavailable")
	}
	if err := application.runElevated(context.Background(), []string{"service", "restart"}, ""); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(executor.command.Env, elevationEnvironment(nil)) {
		t.Fatalf("default elevation environment = %#v", executor.command.Env)
	}
	if len(executor.command.Args) == 0 || strings.HasPrefix(executor.command.Args[0], "--preserve-env=") {
		t.Fatalf("default paths requested environment preservation: %#v", executor.command.Args)
	}
}

func TestDefaultPathsSupportsOverridesButElevationRejectsThem(t *testing.T) {
	want := Paths{
		ConfigFile:     "/opt/mihomoctl/config/state.yaml",
		DataDir:        "/opt/mihomoctl/data",
		ProfileRoot:    "/opt/mihomoctl/profiles",
		BackupDir:      "/opt/mihomoctl/backups",
		OperationLock:  "/opt/mihomoctl/run/operation.lock",
		PublicFile:     "/opt/mihomoctl/public/state.json",
		ClientFile:     "/home/operator/.config/mihomoctl/client.yaml",
		DefaultService: "alternate.service",
		TimerUnit:      "alternate.timer",
	}
	values := map[string]string{
		dataDirEnvironment:      want.DataDir,
		configEnvironment:       want.ConfigFile,
		profileDirEnvironment:   want.ProfileRoot,
		backupDirEnvironment:    want.BackupDir,
		lockFileEnvironment:     want.OperationLock,
		publicStateEnvironment:  want.PublicFile,
		clientConfigEnvironment: want.ClientFile,
		serviceEnvironment:      want.DefaultService,
		timerEnvironment:        want.TimerUnit,
	}
	for key, value := range values {
		t.Setenv(key, value)
	}
	if got := DefaultPaths(); !reflect.DeepEqual(got, want) {
		t.Fatalf("DefaultPaths() = %#v, want %#v", got, want)
	}
	if _, _, err := elevatedEnvironment(want); err == nil || !strings.Contains(err.Error(), "不支持覆盖") {
		t.Fatalf("custom privileged layout error = %v", err)
	}
}

func TestRunElevatedRejectsUnsafeSupportedEnvironmentValues(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Paths)
	}{
		{name: "relative data directory", mutate: func(paths *Paths) { paths.DataDir = "relative/data" }},
		{name: "root profile directory", mutate: func(paths *Paths) { paths.ProfileRoot = "/" }},
		{name: "valid but privileged data override", mutate: func(paths *Paths) { paths.DataDir = "/etc" }},
		{name: "valid but privileged public override", mutate: func(paths *Paths) { paths.PublicFile = "/etc/mihomoctl-overwrite.json" }},
		{name: "valid but privileged service override", mutate: func(paths *Paths) { paths.DefaultService = "ssh.service" }},
		{name: "valid but privileged timer override", mutate: func(paths *Paths) { paths.TimerUnit = "apt-daily.timer" }},
		{name: "client path outside caller home", mutate: func(paths *Paths) { paths.ClientFile = "/etc/client.yaml" }},
		{name: "NUL in client path", mutate: func(paths *Paths) { paths.ClientFile = "/home/test/client\x00.yaml" }},
		{name: "control character in config path", mutate: func(paths *Paths) { paths.ConfigFile = "/etc/mihomoctl/config\n.yaml" }},
		{name: "invalid service unit", mutate: func(paths *Paths) { paths.DefaultService = "mihomo.service;reboot" }},
		{name: "invalid timer unit", mutate: func(paths *Paths) { paths.TimerUnit = "--system" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executor := &recordingPrivilegeExecutor{}
			application := privilegeTestApp(executor, 1000)
			test.mutate(&application.paths)
			err := application.runElevated(context.Background(), []string{"service", "restart"}, "")
			var invalid *InvalidInputError
			if !errors.As(err, &invalid) {
				t.Fatalf("unsafe override error = %T %v, want InvalidInputError", err, err)
			}
			if executor.calls != 0 {
				t.Fatalf("unsafe override invoked sudo %d times", executor.calls)
			}
		})
	}
}

func TestOSPrivilegeExecutorReportsChildExitCode(t *testing.T) {
	t.Setenv("MIHOMOCTL_PRIVILEGE_HELPER", "exit")
	code, err := (OSPrivilegeExecutor{}).Run(context.Background(), PrivilegeCommand{
		Name: os.Args[0], Args: []string{"-test.run=^TestPrivilegeExecutorHelperProcess$"},
		Stdout: io.Discard, Stderr: io.Discard,
		Env: append(elevationEnvironment(nil), "MIHOMOCTL_PRIVILEGE_HELPER=exit"),
	})
	if err == nil || code != 7 {
		t.Fatalf("exit code=%d error=%v, want code 7", code, err)
	}
}

func TestOSPrivilegeExecutorCancelsRunningProcess(t *testing.T) {
	t.Setenv("MIHOMOCTL_PRIVILEGE_HELPER", "wait")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := (OSPrivilegeExecutor{}).Run(ctx, PrivilegeCommand{
		Name: os.Args[0], Args: []string{"-test.run=^TestPrivilegeExecutorHelperProcess$"},
		Stdout: io.Discard, Stderr: io.Discard,
		Env: append(elevationEnvironment(nil), "MIHOMOCTL_PRIVILEGE_HELPER=wait"),
	})
	if err == nil {
		t.Fatal("cancelled executor unexpectedly succeeded")
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("cancelled executor took %s, want at most 3s", elapsed)
	}
}

func TestPrivilegeExecutorHelperProcess(t *testing.T) {
	switch os.Getenv("MIHOMOCTL_PRIVILEGE_HELPER") {
	case "exit":
		os.Exit(7)
	case "wait":
		time.Sleep(30 * time.Second)
	}
}
