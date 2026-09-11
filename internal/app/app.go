package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mihomoctl/internal/domain"
	"mihomoctl/internal/i18n"
	"mihomoctl/internal/mihomo"
	"mihomoctl/internal/platform"
	"mihomoctl/internal/profile"
	"mihomoctl/internal/tui"
)

// UnavailableError marks a missing local runtime dependency. The CLI maps it
// to its stable "controller unavailable" exit code without importing app.
type UnavailableError struct {
	Message string
	Cause   error
}

func (e *UnavailableError) Error() string {
	if e.Message != "" {
		if e.Cause != nil {
			return e.Message + ": " + e.Cause.Error()
		}
		return e.Message
	}
	return "Mihomo 控制器不可用"
}

func (e *UnavailableError) Unwrap() error { return e.Cause }
func (e *UnavailableError) ExitCode() int { return 4 }
func (e *UnavailableError) Localized(language i18n.Language) string {
	message := "Mihomo 控制器不可用"
	if e != nil && e.Message != "" {
		message = e.Message
	}
	message = i18n.T(language, message)
	if e != nil && e.Cause != nil {
		message += ": " + i18n.Error(language, e.Cause)
	}
	return message
}

var ErrNotInitialized = &UnavailableError{Message: "mihomoctl 尚未初始化，请先运行 mihomoctl init"}

type InvalidStateError struct {
	Cause error
}

func (e *InvalidStateError) Error() string {
	if e.Cause == nil {
		return "mihomoctl 状态文件无效"
	}
	return "mihomoctl 状态文件无效: " + e.Cause.Error()
}
func (e *InvalidStateError) Unwrap() error { return e.Cause }
func (e *InvalidStateError) ExitCode() int { return 2 }
func (e *InvalidStateError) Localized(language i18n.Language) string {
	message := i18n.T(language, "mihomoctl 状态文件无效")
	if e != nil && e.Cause != nil {
		message += ": " + i18n.Error(language, e.Cause)
	}
	return message
}

type InvalidInputError struct {
	Cause error
}

func (e *InvalidInputError) Error() string {
	if e.Cause == nil {
		return "输入无效"
	}
	return e.Cause.Error()
}
func (e *InvalidInputError) Unwrap() error { return e.Cause }
func (e *InvalidInputError) ExitCode() int { return 2 }
func (e *InvalidInputError) Localized(language i18n.Language) string {
	if e == nil || e.Cause == nil {
		return i18n.T(language, "输入无效")
	}
	return i18n.Error(language, e.Cause)
}

// OperationWarning reports a completed operation with non-fatal follow-up
// failures. Presentation layers should show it as a warning, not a hard error.
type OperationWarning struct {
	Message string
	message *i18n.Message
}

func operationWarningf(key string, args ...any) *OperationWarning {
	message := i18n.M(key, args...)
	return &OperationWarning{Message: message.String(), message: &message}
}

type localizedErrorList []error

func (items localizedErrorList) Error() string {
	return items.Localized(i18n.Chinese)
}

func (items localizedErrorList) Localized(language i18n.Language) string {
	separator := "；"
	if language == i18n.English {
		separator = "; "
	}
	messages := make([]string, 0, len(items))
	for _, err := range items {
		if err != nil {
			messages = append(messages, i18n.Error(language, err))
		}
	}
	return strings.Join(messages, separator)
}

func (e *OperationWarning) Error() string {
	if e == nil || strings.TrimSpace(e.Message) == "" {
		return "操作已完成，但存在警告"
	}
	return e.Message
}

func (e *OperationWarning) Warning() bool { return true }
func (e *OperationWarning) Localized(language i18n.Language) string {
	if e == nil || strings.TrimSpace(e.Message) == "" {
		return i18n.T(language, "操作已完成，但存在警告")
	}
	if e.message != nil {
		return e.message.Text(language)
	}
	return i18n.T(language, e.Message)
}

type App struct {
	paths               Paths
	runner              platform.CommandRunner
	privilegeExecutor   PrivilegeExecutor
	euid                func() int
	executable          func() (string, error)
	executableValidator func(string) (string, error)
	sudoPath            func() (string, error)

	mu     sync.RWMutex
	state  *persistedState
	client *clientState
	store  *profile.Store
	api    *mihomo.Client

	coreVersion   string
	coreVersionAt time.Time

	scheduleCompatibility func(Paths, persistedState, []domain.Profile) error

	stateLoadErr  error
	clientLoadErr error
}

type Option func(*App)

func WithPaths(paths Paths) Option {
	return func(a *App) { a.paths = paths }
}

func WithRunner(runner platform.CommandRunner) Option {
	return func(a *App) { a.runner = runner }
}

func WithPrivilegeExecutor(executor PrivilegeExecutor) Option {
	return func(a *App) { a.privilegeExecutor = executor }
}

func WithEUID(euid func() int) Option {
	return func(a *App) { a.euid = euid }
}

func WithExecutable(executable func() (string, error)) Option {
	return func(a *App) { a.executable = executable }
}

func WithExecutableValidator(validator func(string) (string, error)) Option {
	return func(a *App) { a.executableValidator = validator }
}

func WithSudoPathResolver(resolver func() (string, error)) Option {
	return func(a *App) { a.sudoPath = resolver }
}

func New(options ...Option) (*App, error) {
	a := &App{
		paths:                 DefaultPaths(),
		runner:                platform.ExecRunner{},
		privilegeExecutor:     OSPrivilegeExecutor{},
		euid:                  os.Geteuid,
		executable:            os.Executable,
		executableValidator:   validateTrustedExecutable,
		sudoPath:              trustedSudoPath,
		scheduleCompatibility: validateScheduleCompatibility,
	}
	for _, option := range options {
		option(a)
	}
	if a.paths.OperationLock == "" && a.paths.DataDir != "" {
		a.paths.OperationLock = filepath.Join(a.paths.DataDir, ".operation.lock")
	}
	if a.runner == nil || a.privilegeExecutor == nil || a.euid == nil || a.executable == nil || a.executableValidator == nil || a.sudoPath == nil {
		return nil, i18n.Errorf("mihomoctl 运行依赖不能为空")
	}
	if a.paths.ProfileRoot == "" || a.paths.ConfigFile == "" || a.paths.ClientFile == "" || a.paths.DataDir == "" || a.paths.BackupDir == "" || a.paths.OperationLock == "" || a.paths.PublicFile == "" || a.paths.DefaultService == "" || a.paths.TimerUnit == "" {
		return nil, i18n.Errorf("mihomoctl 路径配置不完整")
	}
	if runningUnderSudo() {
		if _, _, err := elevatedEnvironment(a.paths); err != nil {
			return nil, &InvalidInputError{Cause: i18n.Errorf("sudo 子进程拒绝自定义受管路径: %w", err)}
		}
	}
	// Use the real process identity here. WithEUID controls application-level
	// privilege behavior in tests, but must never authorize root filesystem
	// operations in a non-root process.
	if os.Geteuid() == 0 {
		if err := validateExistingRootApplicationDirectories(a.paths); err != nil {
			return nil, &InvalidInputError{Cause: i18n.Errorf("验证 root 受管目录失败: %w", err)}
		}
	}
	stateErr := a.loadState()
	if stateErr == nil {
		if os.Geteuid() == 0 {
			if err := ensureRootApplicationDirectories(a.paths); err != nil {
				return nil, &InvalidInputError{Cause: i18n.Errorf("验证 root 受管目录失败: %w", err)}
			}
		}
		store, err := profile.NewStore(a.paths.ProfileRoot)
		if err != nil {
			a.stateLoadErr = classifyProfileStoreError(err)
		} else {
			a.store = store
		}
	} else if !isMissing(stateErr) {
		a.stateLoadErr = stateErr
	}
	clientErr := a.loadClient()
	if clientErr != nil && !errors.Is(clientErr, os.ErrNotExist) {
		a.clientLoadErr = clientErr
	}
	a.rebuildAPI()
	return a, nil
}

func (a *App) rebuildAPI() {
	a.api = nil
	a.coreVersion = ""
	a.coreVersionAt = time.Time{}
	controller, secret := "", ""
	if a.state != nil {
		controller, secret = a.state.Controller, a.state.Secret
	} else if a.client != nil {
		controller, secret = a.client.Controller, a.client.Secret
	}
	if controller == "" {
		return
	}
	client, err := mihomo.New(controller, secret)
	if err == nil {
		a.api = client
	}
}

func (a *App) isRoot() bool { return a.euid() == 0 }

// NeedsElevation reports whether protected operations need sudo for this
// process. TUI clients use it to select their interactive execution path.
func (a *App) NeedsElevation() bool { return !a.isRoot() }

func (a *App) serviceName() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.state != nil && a.state.Installation.Unit != "" {
		return a.state.Installation.Unit
	}
	if a.client != nil && a.client.Service != "" {
		return a.client.Service
	}
	return a.paths.DefaultService
}

func (a *App) requireRootState() (persistedState, *profile.Store, error) {
	if !a.isRoot() {
		return persistedState{}, nil, i18n.Errorf("此操作需要 root 权限")
	}
	if a.state == nil {
		if a.stateLoadErr != nil {
			return persistedState{}, nil, a.stateLoadErr
		}
		if err := a.loadState(); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return persistedState{}, nil, ErrNotInitialized
			}
			return persistedState{}, nil, err
		}
	}
	if a.store == nil {
		store, err := profile.NewStore(a.paths.ProfileRoot)
		if err != nil {
			return persistedState{}, nil, classifyProfileStoreError(err)
		}
		a.store = store
	}
	return *a.state, a.store, nil
}

func (a *App) rootStateSnapshot() (persistedState, *profile.Store, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.requireRootState()
}

func (a *App) publishProfiles(profiles []domain.Profile) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.writePublicProfiles(profiles)
}

func (a *App) RunTUI() error {
	return a.RunTUIContext(context.Background())
}

func (a *App) RunTUIContext(ctx context.Context) error {
	return tui.Run(ctx, a)
}

func executablePath(getter func() (string, error)) (string, error) {
	path, err := getter()
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(path) {
		path, err = exec.LookPath(path)
		if err != nil {
			return "", err
		}
	}
	return filepath.Clean(path), nil
}

func (a *App) checkAPI() (*mihomo.Client, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.isRoot() && a.stateLoadErr != nil {
		return nil, a.stateLoadErr
	}
	if !a.isRoot() && a.clientLoadErr != nil {
		return nil, a.clientLoadErr
	}
	if a.api == nil {
		return nil, ErrNotInitialized
	}
	return a.api, nil
}

func (a *App) requireManagedInstallation() error {
	a.mu.RLock()
	// The root-owned state is deliberately private. An ordinary process may
	// receive EACCES while probing it and must rely on its client state or the
	// public snapshot before handing protected work to sudo.
	if a.isRoot() && a.stateLoadErr != nil {
		err := a.stateLoadErr
		a.mu.RUnlock()
		return err
	}
	if a.clientLoadErr != nil {
		err := a.clientLoadErr
		a.mu.RUnlock()
		return err
	}
	if (a.isRoot() && a.state != nil) || a.client != nil {
		a.mu.RUnlock()
		return nil
	}
	a.mu.RUnlock()
	if _, err := a.readPublicProfiles(); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return ErrNotInitialized
}

func controllerError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) {
		return err
	}
	var apiErr *mihomo.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode >= 400 && apiErr.StatusCode < 500 && apiErr.StatusCode != 401 && apiErr.StatusCode != 403 {
		return err
	}
	return &UnavailableError{Message: "Mihomo 控制器不可用", Cause: err}
}

func classifyProfileStoreError(err error) error {
	if err == nil {
		return nil
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return err
	}
	return &InvalidStateError{Cause: err}
}

func joinErrors(prefix string, errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	return i18n.Errorf("%s: %w", i18n.M(prefix), errors.Join(errs...))
}
