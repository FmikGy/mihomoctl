package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

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

type App struct {
	paths      Paths
	runner     platform.CommandRunner
	euid       func() int
	executable func() (string, error)

	mu     sync.RWMutex
	state  *persistedState
	client *clientState
	store  *profile.Store
	api    *mihomo.Client

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

func WithEUID(euid func() int) Option {
	return func(a *App) { a.euid = euid }
}

func WithExecutable(executable func() (string, error)) Option {
	return func(a *App) { a.executable = executable }
}

func New(options ...Option) (*App, error) {
	a := &App{
		paths:      DefaultPaths(),
		runner:     platform.ExecRunner{},
		euid:       os.Geteuid,
		executable: os.Executable,
	}
	for _, option := range options {
		option(a)
	}
	if a.runner == nil || a.euid == nil || a.executable == nil {
		return nil, errors.New("mihomoctl 运行依赖不能为空")
	}
	if a.paths.ProfileRoot == "" || a.paths.ConfigFile == "" || a.paths.ClientFile == "" || a.paths.DataDir == "" || a.paths.PublicFile == "" {
		return nil, errors.New("mihomoctl 路径配置不完整")
	}
	stateErr := a.loadState()
	if stateErr == nil {
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
	controller, secret := "", ""
	if a.state != nil {
		controller, secret = a.state.Controller, a.state.Secret
	} else if a.client != nil {
		controller, secret = a.client.Controller, a.client.Secret
	}
	if controller == "" {
		a.api = nil
		return
	}
	client, err := mihomo.New(controller, secret)
	if err == nil {
		a.api = client
	}
}

func (a *App) isRoot() bool { return a.euid() == 0 }

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
		return persistedState{}, nil, errors.New("此操作需要 root 权限")
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
	if a.stateLoadErr != nil {
		err := a.stateLoadErr
		a.mu.RUnlock()
		return err
	}
	if a.clientLoadErr != nil {
		err := a.clientLoadErr
		a.mu.RUnlock()
		return err
	}
	if a.state != nil || a.client != nil {
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
	return fmt.Errorf("%s: %w", prefix, errors.Join(errs...))
}
