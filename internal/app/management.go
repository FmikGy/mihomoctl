package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net"
	"os"
	osuser "os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"mihomoctl/internal/domain"
	"mihomoctl/internal/i18n"
	"mihomoctl/internal/mihomo"
	"mihomoctl/internal/platform"
	"mihomoctl/internal/profile"
)

func (a *App) Initialize(ctx context.Context, options domain.InitOptions) error {
	options.Service = strings.TrimSpace(options.Service)
	options.ConfigPath = strings.TrimSpace(options.ConfigPath)
	options.Controller = strings.TrimSpace(options.Controller)
	if options.Controller != "" {
		if err := validateLocalController(options.Controller); err != nil {
			return &InvalidInputError{Cause: err}
		}
	}
	if err := validateInitOverrides(a.isRoot(), runningUnderSudo(), options); err != nil {
		return err
	}
	if !a.isRoot() {
		args := []string{"init", "--output", "json"}
		if options.Controller != "" {
			args = append(args, "--controller", options.Controller)
		}
		if options.Force {
			args = append(args, "--force")
		}
		return a.runElevated(ctx, args, "")
	}
	return a.withMutation(ctx, func() error {
		return a.initializeRoot(ctx, options)
	})
}

func (a *App) initializeRoot(ctx context.Context, options domain.InitOptions) error {
	a.mu.RLock()
	loadErr := a.stateLoadErr
	var existing *persistedState
	if a.state != nil {
		copy := *a.state
		existing = &copy
	}
	a.mu.RUnlock()
	if loadErr != nil && !options.Force {
		return loadErr
	}
	if existing != nil && !options.Force {
		return i18n.Errorf("mihomoctl 已初始化；如需修复请使用 doctor --fix")
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
	raw, err := platform.ReadManagedConfig(installation.ConfigPath, platform.MaxManagedConfigBytes)
	if err != nil {
		return i18n.Errorf("读取现有 Mihomo 配置失败: %w", err)
	}
	settings := inferSettings(raw)
	secret, err := randomSecret()
	if err != nil {
		return err
	}
	if existing != nil && options.Force {
		settings = existing.Settings
		secret = existing.Secret
		if options.Controller == "" {
			controller = existing.Controller
		}
	}
	state := persistedState{
		Version:      stateVersion,
		Installation: installation,
		Controller:   controller,
		Secret:       secret,
		Settings:     settings,
	}
	rollback, err := a.fileRollback(a.paths.ConfigFile, a.paths.PublicFile, a.paths.ClientFile)
	if err != nil {
		return err
	}

	store, err := profile.NewStore(a.paths.ProfileRoot)
	if err != nil {
		return err
	}
	storeCheckpoint, err := store.Checkpoint()
	if err != nil {
		return err
	}
	rollback.add("配置存储", func(context.Context) error { return store.RestoreCheckpoint(storeCheckpoint) })
	profiles, err := store.List()
	if err != nil {
		return err
	}
	if len(profiles) == 0 {
		if _, err := store.AddSnapshot("系统原配置", raw); err != nil {
			return a.failAndReload(ctx, rollback, i18n.Errorf("导入现有配置失败: %w", err))
		}
	}
	config, err := store.ActiveConfig()
	if err != nil {
		return a.failAndReload(ctx, rollback, err)
	}
	applied, err := a.applyProfileConfig(ctx, state, config)
	if err != nil {
		return a.failAndReload(ctx, rollback, err)
	}
	rollback.add("Mihomo 配置", applied.rollback)
	a.mu.Lock()
	err = a.saveState(state)
	if err == nil {
		a.store = store
		a.rebuildAPI()
	}
	a.mu.Unlock()
	if err != nil {
		return a.failAndReload(ctx, rollback, err)
	}
	profiles, err = store.List()
	if err != nil {
		return a.failAndReload(ctx, rollback, err)
	}
	if err := a.publishProfiles(profiles); err != nil {
		return a.failAndReload(ctx, rollback, err)
	}
	activeName := ""
	for _, item := range profiles {
		if item.Active {
			activeName = item.Name
		}
	}
	a.mu.Lock()
	err = a.saveClient(state, activeName)
	a.mu.Unlock()
	if err != nil {
		return a.failAndReload(ctx, rollback, err)
	}
	return nil
}

func validateInitOverrides(root, sudoChild bool, options domain.InitOptions) error {
	if options.Service == "" && options.ConfigPath == "" {
		return nil
	}
	if !root {
		return &InvalidInputError{Cause: i18n.Errorf("普通用户自动提权初始化只支持自动探测；--service 和 --config 仅可由管理员在真正的 root 会话中使用")}
	}
	if sudoChild {
		return &InvalidInputError{Cause: i18n.Errorf("sudo 子进程拒绝 --service 或 --config；自定义安装位置必须由管理员在真正的 root 会话中配置")}
	}
	return nil
}

type appliedProfileConfig struct {
	applier platform.ConfigApplier
	result  platform.ApplyResult
}

func (applied *appliedProfileConfig) rollback(ctx context.Context) error {
	if applied == nil {
		return nil
	}
	return applied.applier.Rollback(ctx, applied.result)
}

func (a *App) applyProfileConfig(ctx context.Context, state persistedState, raw []byte) (*appliedProfileConfig, error) {
	managed, err := profile.MergeManagedConfig(raw, profile.OverlayOptions{
		ExternalController: state.Controller,
		Secret:             state.Secret,
		Settings:           state.Settings,
	})
	if err != nil {
		return nil, i18n.Errorf("生成活动配置失败: %w", err)
	}
	expected, err := effectiveConfigFromYAML(managed)
	if err != nil {
		return nil, i18n.Errorf("读取活动配置失败: %w", err)
	}
	systemd, err := platform.NewSystemd(a.runner, state.Installation.Unit)
	if err != nil {
		return nil, err
	}
	service, err := systemd.Status(ctx)
	if err != nil {
		return nil, err
	}
	configAccess, err := serviceConfigAccess(ctx, systemd)
	if err != nil {
		return nil, err
	}
	client, err := mihomo.New(state.Controller, state.Secret, mihomo.WithRequestTimeout(time.Second))
	if err != nil {
		return nil, err
	}
	activationCalls := 0
	applier := platform.ConfigApplier{
		Runner: a.runner,
		Paths: platform.ApplyPaths{
			MihomoBinary: state.Installation.BinaryPath,
			ConfigPath:   state.Installation.ConfigPath,
			ConfigDir:    state.Installation.ConfigDir,
			BackupDir:    a.paths.BackupDir,
		},
		ConfigAccess: configAccess,
		Activate: func(activateCtx context.Context) error {
			activationCalls++
			if !service.Active {
				return nil
			}
			return systemd.Restart(activateCtx)
		},
		HealthCheck: func(healthCtx context.Context) error {
			if !service.Active {
				return nil
			}
			if activationCalls > 1 {
				status, statusErr := systemd.Status(healthCtx)
				if statusErr == nil && status.Active {
					return nil
				}
				return errors.Join(statusErr, i18n.Errorf("恢复后的 Mihomo 服务未运行"))
			}
			return waitForAppliedConfig(healthCtx, client, expected, 8*time.Second)
		},
	}
	result, err := applier.Apply(ctx, managed)
	if err != nil {
		return nil, i18n.Errorf("应用 Mihomo 配置失败: %w", err)
	}
	return &appliedProfileConfig{applier: applier, result: result}, nil
}

func serviceConfigAccess(ctx context.Context, systemd *platform.Systemd) (*platform.ConfigAccess, error) {
	identity, err := systemd.Identity(ctx)
	if err != nil {
		return nil, i18n.Errorf("读取 Mihomo 服务用户失败: %w", err)
	}
	serviceUser := strings.TrimSpace(identity.User)
	if !identity.DynamicUser && (serviceUser == "" || serviceUser == "root" || serviceUser == "0") {
		return &platform.ConfigAccess{UID: os.Geteuid(), GID: os.Getegid()}, nil
	}

	groupName := strings.TrimSpace(identity.Group)
	if groupName == "" {
		if identity.DynamicUser {
			return nil, i18n.Errorf("Mihomo 使用 DynamicUser 但没有固定 Group；请在服务中配置可解析的静态 Group 后重试")
		}
		account, lookupErr := lookupUser(serviceUser)
		if lookupErr != nil {
			return nil, i18n.Errorf("无法解析 Mihomo 服务用户 %q；请为服务配置可解析的 User 和 Group: %w", serviceUser, lookupErr)
		}
		groupName = account.Gid
	}
	group, err := lookupGroup(groupName)
	if err != nil {
		return nil, i18n.Errorf("无法解析 Mihomo 服务组 %q；请在 systemd 服务中配置现有的 Group: %w", groupName, err)
	}
	gid, err := strconv.Atoi(group.Gid)
	if err != nil || gid < 0 {
		return nil, i18n.Errorf("Mihomo 服务组 %q 的 GID 无效", groupName)
	}
	return &platform.ConfigAccess{UID: 0, GID: gid, GroupReadable: true}, nil
}

func lookupUser(value string) (*osuser.User, error) {
	if _, err := strconv.ParseUint(value, 10, 32); err == nil {
		return osuser.LookupId(value)
	}
	return osuser.Lookup(value)
}

func lookupGroup(value string) (*osuser.Group, error) {
	if _, err := strconv.ParseUint(value, 10, 32); err == nil {
		return osuser.LookupGroupId(value)
	}
	return osuser.LookupGroup(value)
}

func waitForAppliedConfig(ctx context.Context, client *mihomo.Client, expected domain.EffectiveConfig, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		attemptCtx, cancel := context.WithTimeout(ctx, time.Second)
		_, versionErr := client.Version(attemptCtx)
		config, configErr := client.Configs(attemptCtx)
		cancel()
		lastErr = errors.Join(versionErr, configErr)
		if lastErr == nil {
			drift := effectiveConfigDrift(expected, effectiveConfigFromMihomo(config))
			if len(drift) == 0 {
				return nil
			}
			lastErr = i18n.Errorf("运行配置与期望值不一致: %s", strings.Join(drift, ", "))
		}
		if time.Now().After(deadline) {
			return i18n.Errorf("Mihomo 配置生效检查超时: %w", lastErr)
		}
		wait := min(200*time.Millisecond, time.Until(deadline))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

func (a *App) Service(ctx context.Context, action string) error {
	action = strings.ToLower(strings.TrimSpace(action))
	switch action {
	case "start", "stop", "restart", "enable", "disable", "enable-now":
	default:
		return i18n.Errorf("不支持的服务操作 %q", action)
	}
	if !a.isRoot() {
		if err := a.requireManagedInstallation(); err != nil {
			return err
		}
		args := []string{"service", action, "--output", "json"}
		if action == "enable-now" {
			base, _, _ := strings.Cut(action, "-")
			args = []string{"service", base, "--now", "--output", "json"}
		}
		return a.runElevated(ctx, args, "")
	}
	return a.withMutation(ctx, func() error {
		state, _, err := a.rootStateSnapshot()
		if err != nil {
			return err
		}
		systemd, err := platform.NewSystemd(a.runner, state.Installation.Unit)
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
		case "enable-now":
			return systemd.EnableNow(ctx)
		}
		return nil
	})
}

func (a *App) SetMode(ctx context.Context, mode domain.Mode) error {
	if mode != domain.ModeRule && mode != domain.ModeGlobal && mode != domain.ModeDirect {
		return i18n.Errorf("无效运行模式 %q", mode)
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

// SyncPublicState rebuilds the non-sensitive status snapshot from the active
// profile and the already-applied Mihomo configuration. It never applies a
// configuration or changes service state.
func (a *App) SyncPublicState(ctx context.Context) error {
	if !a.isRoot() {
		if err := a.requireManagedInstallation(); err != nil {
			return err
		}
		return a.runElevated(ctx, []string{"--output", "json", "config", "sync-public-state"}, "")
	}
	return a.withMutation(ctx, func() error { return a.syncPublicStateRoot(ctx) })
}

// SyncClientState writes the controller credentials to the current sudo
// caller's private config path. It is useful after an administrator performs
// a custom-path initialization from an independent root session.
func (a *App) SyncClientState(ctx context.Context) error {
	if !a.isRoot() {
		if err := a.requireManagedInstallation(); err != nil {
			return err
		}
		if err := a.runElevated(ctx, []string{"--output", "json", "config", "sync-client"}, ""); err != nil {
			return err
		}
		a.mu.Lock()
		a.reloadDiskStateLocked()
		a.mu.Unlock()
		return nil
	}
	return a.withMutation(ctx, func() error {
		state, store, err := a.rootStateSnapshot()
		if err != nil {
			return err
		}
		active, err := store.Active()
		if err != nil {
			return err
		}
		rollback, err := a.fileRollback(a.paths.ClientFile)
		if err != nil {
			return err
		}
		a.mu.Lock()
		err = a.saveClient(state, active.Name)
		a.mu.Unlock()
		if err != nil {
			return a.failAndReload(ctx, rollback, err)
		}
		return nil
	})
}

func (a *App) syncPublicStateRoot(ctx context.Context) error {
	_, store, err := a.rootStateSnapshot()
	if err != nil {
		return err
	}
	rollback, err := a.fileRollback(a.paths.PublicFile)
	if err != nil {
		return err
	}
	profiles, err := store.List()
	if err != nil {
		return err
	}
	if err := a.publishProfiles(profiles); err != nil {
		return rollback.fail(ctx, err)
	}
	return nil
}

func (a *App) ValidateConfigValue(key, value string) error {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "mixed-port":
		port, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || port < 1 || port > 65535 {
			return i18n.Errorf("mixed-port 必须是 1 到 65535")
		}
	case "allow-lan", "ipv6":
		if _, err := parseToggle(value); err != nil {
			return err
		}
	case "log-level":
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "debug", "info", "warning", "error", "silent":
		default:
			return i18n.Errorf("log-level 必须是 debug、info、warning、error 或 silent")
		}
	default:
		return i18n.Errorf("不支持设置 %q", key)
	}
	return nil
}

func (a *App) changeSettings(ctx context.Context, mutate func(*domain.ManagedSettings)) error {
	return a.withMutation(ctx, func() error {
		return a.changeSettingsRoot(ctx, mutate)
	})
}

func (a *App) changeSettingsRoot(ctx context.Context, mutate func(*domain.ManagedSettings)) error {
	state, store, err := a.rootStateSnapshot()
	if err != nil {
		return err
	}
	mutate(&state.Settings)
	if state.Settings.MixedPort != nil && (*state.Settings.MixedPort < 1 || *state.Settings.MixedPort > 65535) {
		return i18n.Errorf("mixed-port 必须是 1 到 65535")
	}
	config, err := store.ActiveConfig()
	if err != nil {
		return err
	}
	rollback, err := a.fileRollback(a.paths.ConfigFile, a.paths.PublicFile, a.paths.ClientFile)
	if err != nil {
		return err
	}
	applied, err := a.applyProfileConfig(ctx, state, config)
	if err != nil {
		return a.failAndReload(ctx, rollback, err)
	}
	rollback.add("Mihomo 配置", applied.rollback)
	a.mu.Lock()
	err = a.saveState(state)
	if err == nil {
		a.rebuildAPI()
	}
	a.mu.Unlock()
	if err != nil {
		return a.failAndReload(ctx, rollback, err)
	}
	active, err := store.Active()
	if err != nil {
		return a.failAndReload(ctx, rollback, err)
	}
	a.mu.Lock()
	err = a.saveClient(state, active.Name)
	a.mu.Unlock()
	if err != nil {
		return a.failAndReload(ctx, rollback, err)
	}
	profiles, err := store.List()
	if err != nil {
		return a.failAndReload(ctx, rollback, err)
	}
	if err := a.publishProfiles(profiles); err != nil {
		return a.failAndReload(ctx, rollback, err)
	}
	return nil
}

func inferSettings(raw []byte) domain.ManagedSettings {
	var source struct {
		Mode      domain.Mode `yaml:"mode"`
		MixedPort *int        `yaml:"mixed-port"`
		Port      *int        `yaml:"port"`
		AllowLAN  *bool       `yaml:"allow-lan"`
		IPv6      *bool       `yaml:"ipv6"`
		LogLevel  string      `yaml:"log-level"`
		TUN       struct {
			Enable bool `yaml:"enable"`
		} `yaml:"tun"`
	}
	_ = yaml.Unmarshal(raw, &source)
	if source.MixedPort == nil {
		source.MixedPort = source.Port
	}
	return normalizeManagedSettings(domain.ManagedSettings{
		Mode: source.Mode, TUN: source.TUN.Enable, MixedPort: source.MixedPort,
		AllowLAN: source.AllowLAN, IPv6: source.IPv6, LogLevel: source.LogLevel,
	})
}

func normalizeManagedSettings(settings domain.ManagedSettings) domain.ManagedSettings {
	if settings.MixedPort == nil {
		port := 7890
		settings.MixedPort = &port
	}
	if settings.AllowLAN == nil {
		allowLAN := false
		settings.AllowLAN = &allowLAN
	}
	return settings
}

func randomSecret() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", i18n.Errorf("生成控制器密钥失败: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func validateLocalController(value string) error {
	if strings.Contains(value, "://") {
		return i18n.Errorf("控制器地址应使用 host:port 格式")
	}
	host, portText, err := net.SplitHostPort(value)
	if err != nil {
		return i18n.Errorf("控制器地址无效: %w", err)
	}
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return i18n.Errorf("控制器只能监听回环地址")
		}
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return i18n.Errorf("控制器端口必须是 1 到 65535")
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
		return false, i18n.Errorf("%q 不是有效开关值", value)
	}
}

func normalizeUnit(value, suffix string) string {
	value = strings.TrimSpace(value)
	if !strings.HasSuffix(value, suffix) {
		value += suffix
	}
	return value
}
