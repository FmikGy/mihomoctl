package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"mihomoctl/internal/platform"
)

type privilegeFailure uint8

const (
	privilegeFailureUnknown privilegeFailure = iota
	privilegeFailureAuthorizationRequired
	privilegeFailureAuthenticationFailed
	privilegeFailureDenied
	privilegeFailureUnavailable
	privilegeFailureNoTerminal
	privilegeFailureUntrustedExecutable
)

type PrivilegeError struct {
	Cause   error
	failure privilegeFailure
	detail  string
}

func (e *PrivilegeError) Error() string {
	if e == nil {
		return "sudo 提权失败"
	}
	switch e.failure {
	case privilegeFailureAuthorizationRequired:
		return "sudo 需要身份验证"
	case privilegeFailureAuthenticationFailed:
		return "sudo 身份验证未完成，操作未执行"
	case privilegeFailureDenied:
		return "当前用户没有执行此操作的 sudo 权限"
	case privilegeFailureUnavailable:
		return "未找到可信的 sudo，请安装系统 sudo 或以 root 运行"
	case privilegeFailureNoTerminal:
		return "sudo 需要可交互终端"
	case privilegeFailureUntrustedExecutable:
		return "当前 mihomoctl 不是可信的系统安装版本，请先安装后再执行管理员操作"
	default:
		if e.detail != "" {
			return "sudo 提权失败：" + e.detail
		}
		return "sudo 提权失败"
	}
}

func (e *PrivilegeError) Unwrap() error { return e.Cause }
func (e *PrivilegeError) ExitCode() int { return 3 }

// AuthorizationRequired lets the TUI detect the single failure that is safe
// to retry after releasing its terminal. Other sudo failures are final.
func (e *PrivilegeError) AuthorizationRequired() bool {
	return e != nil && e.failure == privilegeFailureAuthorizationRequired
}

type elevatedCommandError struct {
	code    int
	message string
}

func (e *elevatedCommandError) Error() string { return e.message }
func (e *elevatedCommandError) ExitCode() int { return e.code }

// PrivilegeCommand describes one direct process execution. Arguments are kept
// separate from the executable and are never interpreted by a shell.
type PrivilegeCommand struct {
	Name   string
	Args   []string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Env    []string
}

// PrivilegeExecutor makes the production sudo path replaceable in tests.
type PrivilegeExecutor interface {
	Run(context.Context, PrivilegeCommand) (int, error)
}

type OSPrivilegeExecutor struct{}

func (OSPrivilegeExecutor) Run(ctx context.Context, command PrivilegeCommand) (int, error) {
	cmd := exec.CommandContext(ctx, command.Name, command.Args...)
	cmd.Stdin = command.Stdin
	cmd.Stdout = command.Stdout
	cmd.Stderr = command.Stderr
	if command.Env == nil {
		cmd.Env = elevationEnvironment(os.Environ())
	} else {
		cmd.Env = append([]string{}, command.Env...)
	}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		return cmd.Process.Signal(os.Interrupt)
	}
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), err
		}
		return -1, err
	}
	return 0, nil
}

// elevationEnvironment deliberately ignores the caller's environment. sudo
// adds HOME and SUDO_* from its authenticated invocation; inheriting those
// values here would let the parent process impersonate a different caller.
func elevationEnvironment(_ []string) []string {
	return []string{
		"PATH=/usr/sbin:/usr/bin:/sbin:/bin",
		"LANG=C",
		"LC_ALL=C",
	}
}

func elevatedEnvironment(paths Paths) ([]string, []string, error) {
	environment := elevationEnvironment(nil)
	overrides := paths.supportedEnvironmentOverrides()
	defaultValues := make(map[string]string, len(overrides))
	for _, item := range defaultElevatedPaths().supportedEnvironmentOverrides() {
		defaultValues[item.name] = item.value
	}
	preserve := make([]string, 0, len(overrides))
	for _, override := range overrides {
		value, err := validateElevatedEnvironmentOverride(override)
		if err != nil {
			return nil, nil, err
		}
		if value == defaultValues[override.name] {
			continue
		}
		if override.name != clientConfigEnvironment {
			return nil, nil, fmt.Errorf("普通用户提权操作不支持覆盖 %s；请使用默认受管路径，或由管理员直接以 root 运行自定义部署", override.name)
		}
		if err := validateCallerClientPath(value); err != nil {
			return nil, nil, fmt.Errorf("环境变量 %s 不安全: %w", override.name, err)
		}
		environment = append(environment, override.name+"="+value)
		preserve = append(preserve, override.name)
	}
	return environment, preserve, nil
}

func validateElevatedEnvironmentOverride(override supportedEnvironmentOverride) (string, error) {
	value := override.value
	if value == "" || value != strings.TrimSpace(value) || !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
		return "", fmt.Errorf("环境变量 %s 的值无效", override.name)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return "", fmt.Errorf("环境变量 %s 的值包含控制字符", override.name)
		}
	}
	if override.unitSuffix != "" {
		unit := value
		if override.name == timerEnvironment {
			unit = normalizeUnit(value, override.unitSuffix)
		}
		if _, err := platform.NewSystemd(platform.ExecRunner{}, unit); err != nil {
			return "", fmt.Errorf("环境变量 %s 的 systemd unit 无效", override.name)
		}
		return value, nil
	}
	clean := filepath.Clean(value)
	if !filepath.IsAbs(value) || clean != value || value == string(filepath.Separator) {
		return "", fmt.Errorf("环境变量 %s 必须是非根目录的绝对路径", override.name)
	}
	return value, nil
}

func runningUnderSudo() bool {
	return isSudoProcess(os.Geteuid(), os.Getenv("SUDO_UID"))
}

func isSudoProcess(euid int, sudoUID string) bool {
	return euid == 0 && strings.TrimSpace(sudoUID) != ""
}

func validateCallerClientPath(path string) error {
	uid := strconv.Itoa(os.Getuid())
	if runningUnderSudo() {
		uid = strings.TrimSpace(os.Getenv("SUDO_UID"))
	}
	account, err := user.LookupId(uid)
	if err != nil || account.HomeDir == "" {
		return errors.New("无法确定当前用户主目录")
	}
	home, err := filepath.Abs(account.HomeDir)
	if err != nil {
		return err
	}
	target, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(home, target)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("客户端状态文件必须位于当前用户主目录中")
	}
	return nil
}

// runElevated re-executes only argv constructed by App methods. No shell is
// involved. Sensitive profile source text is carried on stdin, never argv.
func (a *App) runElevated(ctx context.Context, args []string, input string) error {
	if a.isRoot() {
		return errors.New("内部错误：root 进程不应再次提权")
	}
	executable, err := executablePath(a.executable)
	if err != nil {
		return &PrivilegeError{Cause: err, detail: sanitizeElevatedError(err.Error())}
	}
	executable, err = a.executableValidator(executable)
	if err != nil {
		return &PrivilegeError{Cause: err, failure: privilegeFailureUntrustedExecutable}
	}
	sudoPath, err := a.sudoPath()
	if err != nil {
		return &PrivilegeError{Cause: err, failure: privilegeFailureUnavailable}
	}
	commandEnvironment, preserveEnvironment, err := elevatedEnvironment(a.paths)
	if err != nil {
		return &InvalidInputError{Cause: err}
	}
	commandArgs := make([]string, 0, len(args)+4)
	if platform.NonInteractiveElevation(ctx) {
		commandArgs = append(commandArgs, "-n")
	}
	if len(preserveEnvironment) > 0 {
		commandArgs = append(commandArgs, "--preserve-env="+strings.Join(preserveEnvironment, ","))
	}
	commandArgs = append(commandArgs, "--", executable)
	commandArgs = append(commandArgs, args...)

	var stdin io.Reader
	if input != "" {
		stdin = strings.NewReader(input)
	} else if !platform.NonInteractiveElevation(ctx) {
		stdin = os.Stdin
		if attachedStdin, ok := platform.ElevationInput(ctx); ok && attachedStdin != nil {
			stdin = attachedStdin
		}
	}

	// Keep child output private: elevated CLI errors can contain subscription
	// URLs, and successful JSON output is not useful while returning to the TUI.
	var stdout, stderr bytes.Buffer
	exitCode, runErr := a.privilegeExecutor.Run(ctx, PrivilegeCommand{
		Name: sudoPath, Args: commandArgs, Stdin: stdin,
		Stdout: &stdout, Stderr: &stderr, Env: commandEnvironment,
	})
	if runErr == nil {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(runErr, exec.ErrNotFound) || errors.Is(runErr, os.ErrNotExist) {
		return &PrivilegeError{Cause: runErr, failure: privilegeFailureUnavailable}
	}

	rawStderr := stderr.String()
	diagnosticMessage := sanitizeElevatedError(rawStderr)
	message := sanitizeElevatedError(redactElevatedInput(rawStderr, input))
	failure := classifyPrivilegeFailure(rawStderr, platform.NonInteractiveElevation(ctx))
	if !looksLikeMihomoError(diagnosticMessage) && (looksLikeSudoError(rawStderr) || failure != privilegeFailureUnknown) {
		return &PrivilegeError{Cause: runErr, failure: failure, detail: stripSudoPrefix(message)}
	}
	var exitErr *exec.ExitError
	if exitCode >= 0 || errors.As(runErr, &exitErr) {
		if message == "" {
			message = "提权后的 mihomoctl 操作失败"
		}
		message = strings.TrimPrefix(message, "mihomoctl: ")
		if exitCode < 1 || exitCode > 4 {
			exitCode = 1
		}
		return &elevatedCommandError{code: exitCode, message: message}
	}
	return &PrivilegeError{
		Cause: runErr, failure: classifyPrivilegeFailure(message, platform.NonInteractiveElevation(ctx)),
		detail: stripSudoPrefix(message),
	}
}

func trustedSudoPath() (string, error) {
	for _, candidate := range []string{"/usr/bin/sudo", "/bin/sudo"} {
		if path, err := validateTrustedExecutable(candidate); err == nil {
			return path, nil
		}
	}
	candidate, err := exec.LookPath("sudo")
	if err != nil {
		return "", err
	}
	return validateTrustedExecutable(candidate)
}

func validateTrustedExecutable(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("sudo 路径不是绝对路径")
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("%s 不是可执行的普通文件", resolved)
	}
	if err := validateRootOwnedPath(info, resolved); err != nil {
		return "", err
	}
	for directory := filepath.Dir(resolved); ; directory = filepath.Dir(directory) {
		info, err = os.Stat(directory)
		if err != nil {
			return "", err
		}
		if err := validateRootOwnedPath(info, directory); err != nil {
			return "", err
		}
		if directory == string(filepath.Separator) {
			break
		}
	}
	return resolved, nil
}

func validateRootOwnedPath(info os.FileInfo, path string) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return fmt.Errorf("%s 不属于 root", path)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s 可被非 root 用户写入", path)
	}
	return nil
}

func looksLikeSudoError(message string) bool {
	lower := strings.ToLower(strings.TrimSpace(message))
	return strings.HasPrefix(lower, "sudo:") || strings.HasPrefix(lower, "sudo：")
}

func looksLikeMihomoError(message string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(message)), "mihomoctl:")
}

func classifyPrivilegeFailure(message string, nonInteractive bool) privilegeFailure {
	lower := strings.ToLower(message)
	containsAny := func(values ...string) bool {
		for _, value := range values {
			if strings.Contains(lower, value) {
				return true
			}
		}
		return false
	}
	switch {
	case containsAny("not in the sudoers", "is not allowed to execute", "may not run sudo", "not allowed to run sudo", "not allowed to preserve the environment", "不在 sudoers", "无权运行 sudo"):
		return privilegeFailureDenied
	case containsAny("no tty present", "a terminal is required", "必须有终端", "需要可交互终端"):
		return privilegeFailureNoTerminal
	case containsAny("password is required", "authentication is required", "需要密码", "需要身份验证"):
		if nonInteractive {
			return privilegeFailureAuthorizationRequired
		}
		return privilegeFailureAuthenticationFailed
	case containsAny("incorrect password", "authentication failure", "sorry, try again", "no password was provided", "conversation error", "密码错误", "身份验证失败"):
		return privilegeFailureAuthenticationFailed
	default:
		return privilegeFailureUnknown
	}
}

func stripSudoPrefix(message string) string {
	message = strings.TrimSpace(message)
	lower := strings.ToLower(message)
	for _, prefix := range []string{"sudo:", "sudo："} {
		if strings.HasPrefix(lower, prefix) {
			return strings.TrimSpace(message[len(prefix):])
		}
	}
	return message
}

func redactElevatedInput(value, input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return value
	}
	value = strings.ReplaceAll(value, input, "[redacted input]")
	parsed, err := url.Parse(input)
	if err != nil || parsed.Scheme == "" {
		return value
	}
	candidates := make([]string, 0, len(parsed.Query())+3)
	if parsed.User != nil {
		candidates = append(candidates, parsed.User.Username())
		if password, ok := parsed.User.Password(); ok {
			candidates = append(candidates, password)
		}
	}
	for _, values := range parsed.Query() {
		candidates = append(candidates, values...)
	}
	for _, candidate := range candidates {
		if len([]rune(candidate)) >= 4 {
			value = strings.ReplaceAll(value, candidate, "[redacted]")
		}
	}
	return value
}

func sanitizeElevatedError(value string) string {
	lines := strings.Split(strings.TrimSpace(value), "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		line := strings.TrimSpace(lines[index])
		if line == "" {
			continue
		}
		line = strings.Map(func(character rune) rune {
			if unicode.IsControl(character) {
				return ' '
			}
			return character
		}, line)
		characters := []rune(line)
		if len(characters) > 240 {
			line = string(characters[:239]) + "…"
		}
		return line
	}
	return ""
}

func stdinString(reader io.Reader, limit int64) (string, error) {
	content, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return "", err
	}
	if int64(len(content)) > limit {
		return "", errors.New("输入超过大小限制")
	}
	return string(content), nil
}
