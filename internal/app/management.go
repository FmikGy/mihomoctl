package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"mihomoctl/internal/domain"
	"mihomoctl/internal/mihomo"
	"mihomoctl/internal/platform"
	"mihomoctl/internal/profile"
)

func (a *App) Initialize(ctx context.Context, options domain.InitOptions) error {
	options.Controller = strings.TrimSpace(options.Controller)
	if options.Controller != "" {
		if err := validateLocalController(options.Controller); err != nil {
			return &InvalidInputError{Cause: err}
		}
	}
	if !a.isRoot() {
		args := []string{"init", "--output", "json"}
		if options.Service != "" {
			args = append(args, "--service", options.Service)
		}
		if options.ConfigPath != "" {
			args = append(args, "--config", options.ConfigPath)
		}
		if options.Controller != "" {
			args = append(args, "--controller", options.Controller)
		}
		if options.Force {
			args = append(args, "--force")
		}
		return a.runElevated(ctx, args, "")
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.stateLoadErr != nil && !options.Force {
		return a.stateLoadErr
	}
	if a.state != nil && !options.Force {
		return errors.New("mihomoctl 已初始化；如需修复请使用 doctor --fix")
	}
	discoverer := platform.NewDiscoverer(a.runner)
	if options.Service != "" {
		discoverer.UnitCandidates = []string{normalizeUnit(options.Service, ".service")}
	}
	installation, err := discoverer.Discover(ctx)
	if err != nil {
		return &UnavailableError{Message: "检测 Mihomo 安装失败", Cause: err}
	}
	if options.ConfigPath != "" {
		configPath, pathErr := filepath.Abs(options.ConfigPath)
		if pathErr != nil {
			return pathErr
		}
		installation.ConfigPath = configPath
		installation.ConfigDir = filepath.Dir(configPath)
	}
	if installation.ConfigPath == "" {
		if installation.ConfigDir == "" {
			installation.ConfigDir = "/etc/mihomo"
		}
		installation.ConfigPath = filepath.Join(installation.ConfigDir, "config.yaml")
	}
	if installation.ConfigDir == "" {
		installation.ConfigDir = filepath.Dir(installation.ConfigPath)
	}
	controller := options.Controller
	if controller == "" {
		controller = "127.0.0.1:9090"
	}
	raw, err := os.ReadFile(installation.ConfigPath)
	if err != nil {
		return fmt.Errorf("读取现有 Mihomo 配置失败: %w", err)
	}
	settings := inferSettings(raw)
	secret, err := randomSecret()
	if err != nil {
		return err
	}
	if a.state != nil && options.Force {
		settings = a.state.Settings
		secret = a.state.Secret
		if options.Controller == "" {
			controller = a.state.Controller
		}
	}
	state := persistedState{
		Version:      stateVersion,
		Installation: installation,
		Controller:   controller,
		Secret:       secret,
		Settings:     settings,
	}

	store, err := profile.NewStore(a.paths.ProfileRoot)
	if err != nil {
		return err
	}
	profiles, err := store.List()
	if err != nil {
		return err
	}
	if len(profiles) == 0 {
		if _, err := store.AddSnapshot("系统原配置", raw); err != nil {
			return fmt.Errorf("导入现有配置失败: %w", err)
		}
	}
	config, err := store.ActiveConfig()
	if err != nil {
		return err
	}
	if err := a.applyProfileConfig(ctx, state, config); err != nil {
		return err
	}
	if err := a.saveState(state); err != nil {
		return err
	}
	a.store = store
	a.rebuildAPI()
	profiles, err = store.List()
	if err != nil {
		return err
	}
	if err := a.writePublicProfiles(profiles); err != nil {
		return err
	}
	activeName := ""
	for _, item := range profiles {
		if item.Active {
			activeName = item.Name
		}
	}
	return a.saveClient(state, activeName)
}

func (a *App) applyProfileConfig(ctx context.Context, state persistedState, raw []byte) error {
	managed, err := profile.MergeManagedConfig(raw, profile.OverlayOptions{
		ExternalController: state.Controller,
		Secret:             state.Secret,
		Settings:           state.Settings,
	})
	if err != nil {
		return fmt.Errorf("生成活动配置失败: %w", err)
	}
	systemd, err := platform.NewSystemd(a.runner, state.Installation.Unit)
	if err != nil {
		return err
	}
	service, err := systemd.Status(ctx)
	if err != nil {
		return err
	}
	client, err := mihomo.New(state.Controller, state.Secret, mihomo.WithRequestTimeout(time.Second))
	if err != nil {
		return err
	}
	healthCalls := 0
	applier := platform.ConfigApplier{
		Runner: a.runner,
		Paths: platform.ApplyPaths{
			MihomoBinary: state.Installation.BinaryPath,
			ConfigPath:   state.Installation.ConfigPath,
			ConfigDir:    state.Installation.ConfigDir,
			BackupDir:    a.paths.BackupDir,
		},
		Activate: func(activateCtx context.Context) error {
			if !service.Active {
				return nil
			}
			return systemd.Restart(activateCtx)
		},
		HealthCheck: func(healthCtx context.Context) error {
			healthCalls++
			if !service.Active {
				return nil
			}
			if healthCalls > 1 {
				status, statusErr := systemd.Status(healthCtx)
				if statusErr == nil && status.Active {
					return nil
				}
				return errors.Join(statusErr, errors.New("恢复后的 Mihomo 服务未运行"))
			}
			return waitForAPI(healthCtx, client, 8*time.Second)
		},
	}
	if _, err := applier.Apply(ctx, managed); err != nil {
		return fmt.Errorf("应用 Mihomo 配置失败: %w", err)
	}
	return nil
}

func waitForAPI(ctx context.Context, client *mihomo.Client, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		attemptCtx, cancel := context.WithTimeout(ctx, time.Second)
		_, lastErr = client.Version(attemptCtx)
		cancel()
		if lastErr == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("Mihomo 控制器健康检查超时: %w", lastErr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (a *App) Service(ctx context.Context, action string) error {
	action = strings.ToLower(strings.TrimSpace(action))
	switch action {
	case "start", "stop", "restart", "enable", "disable":
	default:
		return fmt.Errorf("不支持的服务操作 %q", action)
	}
	if !a.isRoot() {
		return a.runElevated(ctx, []string{"service", action, "--output", "json"}, "")
	}
	systemd, err := platform.NewSystemd(a.runner, a.serviceName())
	if err != nil {
		return err
	}
	switch action {
	case "start":
		return systemd.Start(ctx)
	case "stop":
		return systemd.Stop(ctx)
	case "restart":
		return systemd.Restart(ctx)
	case "enable":
		return systemd.Enable(ctx)
	case "disable":
		return systemd.Disable(ctx)
	}
	return nil
}

func (a *App) SetMode(ctx context.Context, mode domain.Mode) error {
	if mode != domain.ModeRule && mode != domain.ModeGlobal && mode != domain.ModeDirect {
		return fmt.Errorf("无效运行模式 %q", mode)
	}
	if !a.isRoot() {
		if err := a.requireManagedInstallation(); err != nil {
			return err
		}
		return a.runElevated(ctx, []string{"mode", string(mode), "--output", "json"}, "")
	}
	return a.changeSettings(ctx, func(settings *domain.ManagedSettings) { settings.Mode = mode })
}

func (a *App) SetTUN(ctx context.Context, enabled bool) error {
	if !a.isRoot() {
		if err := a.requireManagedInstallation(); err != nil {
			return err
		}
		value := "off"
		if enabled {
			value = "on"
		}
		return a.runElevated(ctx, []string{"tun", value, "--output", "json"}, "")
	}
	return a.changeSettings(ctx, func(settings *domain.ManagedSettings) { settings.TUN = enabled })
}

func (a *App) SetConfig(ctx context.Context, key, value string) error {
	key, value = strings.ToLower(strings.TrimSpace(key)), strings.TrimSpace(value)
	if err := a.ValidateConfigValue(key, value); err != nil {
		return err
	}
	if key == "log-level" {
		value = strings.ToLower(value)
	}
	if !a.isRoot() {
		if err := a.requireManagedInstallation(); err != nil {
			return err
		}
		return a.runElevated(ctx, []string{"--output", "json", "config", "set", "--", key, value}, "")
	}
	return a.changeSettings(ctx, func(settings *domain.ManagedSettings) {
		switch key {
		case "mixed-port":
			port, _ := strconv.Atoi(value)
			settings.MixedPort = &port
		case "allow-lan":
			enabled, _ := parseToggle(value)
			settings.AllowLAN = &enabled
		case "ipv6":
			enabled, _ := parseToggle(value)
			settings.IPv6 = &enabled
		case "log-level":
			settings.LogLevel = value
		}
	})
}

func (a *App) ValidateConfigValue(key, value string) error {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "mixed-port":
		port, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || port < 1 || port > 65535 {
			return errors.New("mixed-port 必须是 1 到 65535")
		}
	case "allow-lan", "ipv6":
		if _, err := parseToggle(value); err != nil {
			return err
		}
	case "log-level":
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "debug", "info", "warning", "error", "silent":
		default:
			return errors.New("log-level 必须是 debug、info、warning、error 或 silent")
		}
	default:
		return fmt.Errorf("不支持设置 %q", key)
	}
	return nil
}

func (a *App) changeSettings(ctx context.Context, mutate func(*domain.ManagedSettings)) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	state, store, err := a.requireRootState()
	if err != nil {
		return err
	}
	mutate(&state.Settings)
	if state.Settings.MixedPort != nil && (*state.Settings.MixedPort < 1 || *state.Settings.MixedPort > 65535) {
		return errors.New("mixed-port 必须是 1 到 65535")
	}
	config, err := store.ActiveConfig()
	if err != nil {
		return err
	}
	if err := a.applyProfileConfig(ctx, state, config); err != nil {
		return err
	}
	if err := a.saveState(state); err != nil {
		return err
	}
	a.rebuildAPI()
	active, _ := store.Active()
	return a.saveClient(state, active.Name)
}

func inferSettings(raw []byte) domain.ManagedSettings {
	var source struct {
		Mode      domain.Mode `yaml:"mode"`
		MixedPort *int        `yaml:"mixed-port"`
		AllowLAN  *bool       `yaml:"allow-lan"`
		IPv6      *bool       `yaml:"ipv6"`
		LogLevel  string      `yaml:"log-level"`
		TUN       struct {
			Enable bool `yaml:"enable"`
		} `yaml:"tun"`
	}
	_ = yaml.Unmarshal(raw, &source)
	return domain.ManagedSettings{
		Mode: source.Mode, TUN: source.TUN.Enable, MixedPort: source.MixedPort,
		AllowLAN: source.AllowLAN, IPv6: source.IPv6, LogLevel: source.LogLevel,
	}
}

func randomSecret() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("生成控制器密钥失败: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func validateLocalController(value string) error {
	if strings.Contains(value, "://") {
		return errors.New("控制器地址应使用 host:port 格式")
	}
	host, portText, err := net.SplitHostPort(value)
	if err != nil {
		return fmt.Errorf("控制器地址无效: %w", err)
	}
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return errors.New("控制器只能监听回环地址")
		}
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("控制器端口必须是 1 到 65535")
	}
	return nil
}

func parseToggle(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "on", "true", "1", "yes", "开启":
		return true, nil
	case "off", "false", "0", "no", "关闭":
		return false, nil
	default:
		return false, fmt.Errorf("%q 不是有效开关值", value)
	}
}

func normalizeUnit(value, suffix string) string {
	value = strings.TrimSpace(value)
	if !strings.HasSuffix(value, suffix) {
		value += suffix
	}
	return value
}
