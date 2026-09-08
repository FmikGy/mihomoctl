package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"mihomoctl/internal/domain"
	"mihomoctl/internal/platform"
)

const stateVersion = 1

const (
	dataDirEnvironment      = "MIHOMOCTL_DATA_DIR"
	configEnvironment       = "MIHOMOCTL_CONFIG"
	profileDirEnvironment   = "MIHOMOCTL_PROFILE_DIR"
	backupDirEnvironment    = "MIHOMOCTL_BACKUP_DIR"
	lockFileEnvironment     = "MIHOMOCTL_LOCK_FILE"
	publicStateEnvironment  = "MIHOMOCTL_PUBLIC_STATE"
	clientConfigEnvironment = "MIHOMOCTL_CLIENT_CONFIG"
	serviceEnvironment      = "MIHOMOCTL_SERVICE"
	timerEnvironment        = "MIHOMOCTL_TIMER"
)

type Paths struct {
	ConfigFile     string
	DataDir        string
	ProfileRoot    string
	BackupDir      string
	OperationLock  string
	PublicFile     string
	ClientFile     string
	DefaultService string
	TimerUnit      string
}

func DefaultPaths() Paths {
	dataDir := envOr(dataDirEnvironment, "/var/lib/mihomoctl")
	return Paths{
		ConfigFile:     envOr(configEnvironment, "/etc/mihomoctl/config.yaml"),
		DataDir:        dataDir,
		ProfileRoot:    envOr(profileDirEnvironment, filepath.Join(dataDir, "store")),
		BackupDir:      envOr(backupDirEnvironment, filepath.Join(dataDir, "backups")),
		OperationLock:  envOr(lockFileEnvironment, filepath.Join(dataDir, ".operation.lock")),
		PublicFile:     envOr(publicStateEnvironment, filepath.Join(dataDir, "public.json")),
		ClientFile:     clientConfigPath(),
		DefaultService: envOr(serviceEnvironment, "mihomo.service"),
		TimerUnit:      envOr(timerEnvironment, "mihomoctl-update.timer"),
	}
}

// defaultElevatedPaths describes what a clean root process launched by sudo
// resolves without MIHOMOCTL_* overrides. The client path uses the real
// caller's account instead of HOME or SUDO_* supplied by the parent process.
func defaultElevatedPaths() Paths {
	dataDir := "/var/lib/mihomoctl"
	clientFile := ""
	callerUID := strconv.Itoa(os.Getuid())
	if os.Geteuid() == 0 {
		if sudoUID := strings.TrimSpace(os.Getenv("SUDO_UID")); sudoUID != "" {
			callerUID = sudoUID
		}
	}
	if account, err := user.LookupId(callerUID); err == nil && account.HomeDir != "" {
		clientFile = filepath.Join(account.HomeDir, ".config", "mihomoctl", "client.yaml")
	}
	return Paths{
		ConfigFile:     "/etc/mihomoctl/config.yaml",
		DataDir:        dataDir,
		ProfileRoot:    filepath.Join(dataDir, "store"),
		BackupDir:      filepath.Join(dataDir, "backups"),
		OperationLock:  filepath.Join(dataDir, ".operation.lock"),
		PublicFile:     filepath.Join(dataDir, "public.json"),
		ClientFile:     clientFile,
		DefaultService: "mihomo.service",
		TimerUnit:      "mihomoctl-update.timer",
	}
}

type supportedEnvironmentOverride struct {
	name       string
	value      string
	unitSuffix string
}

// supportedEnvironmentOverrides is the complete environment contract accepted
// by DefaultPaths. Privileged re-execution uses this fixed list instead of
// forwarding the caller's environment.
func (paths Paths) supportedEnvironmentOverrides() []supportedEnvironmentOverride {
	return []supportedEnvironmentOverride{
		{name: dataDirEnvironment, value: paths.DataDir},
		{name: configEnvironment, value: paths.ConfigFile},
		{name: profileDirEnvironment, value: paths.ProfileRoot},
		{name: backupDirEnvironment, value: paths.BackupDir},
		{name: lockFileEnvironment, value: paths.OperationLock},
		{name: publicStateEnvironment, value: paths.PublicFile},
		{name: clientConfigEnvironment, value: paths.ClientFile},
		{name: serviceEnvironment, value: paths.DefaultService, unitSuffix: ".service"},
		{name: timerEnvironment, value: paths.TimerUnit, unitSuffix: ".timer"},
	}
}

type persistedState struct {
	Version      int                    `yaml:"version"`
	Installation platform.Installation  `yaml:"installation"`
	Controller   string                 `yaml:"controller"`
	Secret       string                 `yaml:"secret"`
	Settings     domain.ManagedSettings `yaml:"settings"`
}

type clientState struct {
	Version       int    `yaml:"version"`
	Controller    string `yaml:"controller"`
	Secret        string `yaml:"secret"`
	Service       string `yaml:"service"`
	ActiveProfile string `yaml:"active_profile,omitempty"`
}

type publicState struct {
	SchemaVersion int                     `json:"schema_version"`
	Profiles      []publicProfile         `json:"profiles"`
	Settings      *domain.EffectiveConfig `json:"settings,omitempty"`
}

// publicProfile deliberately omits Source, ETag and LastModified. public.json
// is readable by unprivileged users and must never contain subscription
// credentials or local source paths.
type publicProfile struct {
	ID             string                  `json:"id"`
	Name           string                  `json:"name"`
	Kind           domain.ProfileKind      `json:"kind"`
	Active         bool                    `json:"active"`
	LastUpdated    time.Time               `json:"last_updated,omitempty"`
	UpdateInterval time.Duration           `json:"update_interval,omitempty"`
	Subscription   domain.SubscriptionInfo `json:"subscription,omitempty"`
}

type clientOwnerPlan struct {
	uid                 int
	gid                 int
	home                string
	directoryComponents []string
	filename            string
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func clientConfigPath() string {
	return clientConfigPathForEUID(os.Geteuid())
}

func clientConfigPathForEUID(euid int) string {
	if value := strings.TrimSpace(os.Getenv(clientConfigEnvironment)); value != "" {
		return value
	}
	if uid := strings.TrimSpace(os.Getenv("SUDO_UID")); euid == 0 && uid != "" {
		if account, err := user.LookupId(uid); err == nil && account.HomeDir != "" {
			return filepath.Join(account.HomeDir, ".config", "mihomoctl", "client.yaml")
		}
	}
	configDir, err := os.UserConfigDir()
	if err == nil && configDir != "" {
		return filepath.Join(configDir, "mihomoctl", "client.yaml")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "mihomoctl", "client.yaml")
}

func loadYAML[T any](path string) (T, error) {
	var value T
	content, _, err := readRegularFileNoFollow(path, maxApplicationStateBytes)
	if err != nil {
		return value, err
	}
	if err := yaml.Unmarshal(content, &value); err != nil {
		return value, &InvalidStateError{Cause: fmt.Errorf("读取 %s 失败: %w", path, err)}
	}
	return value, nil
}

func writeYAML(path string, value any, mode os.FileMode) error {
	content, err := yaml.Marshal(value)
	if err != nil {
		return fmt.Errorf("编码状态失败: %w", err)
	}
	return atomicWrite(path, content, mode)
}

func atomicWrite(path string, content []byte, mode os.FileMode) error {
	if int64(len(content)) > maxApplicationStateBytes {
		return fmt.Errorf("写入 %s 失败: 状态文件超过 %d 字节上限", path, maxApplicationStateBytes)
	}
	return atomicWriteOwned(path, content, mode, -1, -1)
}

func atomicWriteOwned(path string, content []byte, mode os.FileMode, uid, gid int) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("创建目录 %s 失败: %w", directory, err)
	}
	temp, err := os.CreateTemp(directory, ".mihomoctl-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if uid >= 0 || gid >= 0 {
		if err := temp.Chown(uid, gid); err != nil {
			temp.Close()
			return err
		}
	}
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(content); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return err
	}
	syncErr := directoryHandle.Sync()
	closeErr := directoryHandle.Close()
	return errors.Join(syncErr, closeErr)
}

func (a *App) loadState() error {
	var state persistedState
	var err error
	if os.Geteuid() == 0 {
		var content []byte
		content, err = platform.ReadManagedConfig(a.paths.ConfigFile, maxApplicationStateBytes)
		if err == nil {
			err = yaml.Unmarshal(content, &state)
			if err != nil {
				err = &InvalidStateError{Cause: fmt.Errorf("读取 %s 失败: %w", a.paths.ConfigFile, err)}
			}
		}
	} else {
		state, err = loadYAML[persistedState](a.paths.ConfigFile)
	}
	if err != nil {
		return err
	}
	if state.Version != stateVersion {
		return &InvalidStateError{Cause: fmt.Errorf("不支持的 mihomoctl 状态版本 %d", state.Version)}
	}
	a.state = &state
	a.stateLoadErr = nil
	return nil
}

func (a *App) saveState(state persistedState) error {
	state.Version = stateVersion
	if err := writeYAML(a.paths.ConfigFile, state, 0o600); err != nil {
		return err
	}
	a.state = &state
	a.stateLoadErr = nil
	return nil
}

func (a *App) loadClient() error {
	owner, err := planClientOwnership(a.paths.ClientFile)
	if err != nil {
		return err
	}
	var content []byte
	if owner == nil {
		content, err = readLocalClientFile(a.paths.ClientFile)
	} else {
		content, err = readOwnedClientFile(owner)
	}
	if err != nil {
		return err
	}
	var client clientState
	if err := yaml.Unmarshal(content, &client); err != nil {
		return &InvalidStateError{Cause: fmt.Errorf("读取 %s 失败: %w", a.paths.ClientFile, err)}
	}
	if client.Version != stateVersion {
		return &InvalidStateError{Cause: fmt.Errorf("不支持的客户端状态版本 %d", client.Version)}
	}
	a.client = &client
	a.clientLoadErr = nil
	return nil
}

func (a *App) saveClient(state persistedState, activeProfile string) error {
	client := clientState{
		Version:       stateVersion,
		Controller:    state.Controller,
		Secret:        state.Secret,
		Service:       state.Installation.Unit,
		ActiveProfile: activeProfile,
	}
	owner, err := planClientOwnership(a.paths.ClientFile)
	if err != nil {
		return err
	}
	content, err := yaml.Marshal(client)
	if err != nil {
		return fmt.Errorf("编码状态失败: %w", err)
	}
	if owner == nil {
		err = atomicWrite(a.paths.ClientFile, content, 0o600)
	} else {
		err = writeOwnedClientFile(owner, content, 0o600, owner.uid, owner.gid)
	}
	if err != nil {
		return err
	}
	a.client = &client
	a.clientLoadErr = nil
	return nil
}

func planClientOwnership(path string) (*clientOwnerPlan, error) {
	uidText := strings.TrimSpace(os.Getenv("SUDO_UID"))
	if uidText == "" {
		return nil, nil
	}
	uid, uidErr := strconv.Atoi(uidText)
	gid, gidErr := strconv.Atoi(strings.TrimSpace(os.Getenv("SUDO_GID")))
	if uidErr != nil || gidErr != nil || uid < 0 || gid < 0 {
		return nil, errors.New("sudo 用户身份无效")
	}
	account, err := user.LookupId(uidText)
	if err != nil {
		return nil, fmt.Errorf("查找 sudo 用户失败: %w", err)
	}
	home, err := filepath.Abs(account.HomeDir)
	if err != nil {
		return nil, err
	}
	resolvedHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		return nil, fmt.Errorf("解析 sudo 用户主目录失败: %w", err)
	}
	target, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	relative, err := filepath.Rel(home, target)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, errors.New("客户端状态文件必须位于 sudo 用户主目录中")
	}
	directory := filepath.Dir(relative)
	components := []string(nil)
	if directory != "." {
		components = strings.Split(directory, string(filepath.Separator))
		for _, component := range components {
			if component == "" || component == "." || component == ".." {
				return nil, errors.New("客户端状态路径无效")
			}
		}
	}
	filename := filepath.Base(relative)
	if filename == "" || filename == "." || filename == ".." {
		return nil, errors.New("客户端状态文件名无效")
	}
	return &clientOwnerPlan{
		uid: uid, gid: gid, home: resolvedHome,
		directoryComponents: components, filename: filename,
	}, nil
}

func (a *App) writePublicProfiles(profiles []domain.Profile) error {
	if err := os.MkdirAll(a.paths.DataDir, 0o755); err != nil {
		return fmt.Errorf("创建公开状态目录失败: %w", err)
	}
	if err := os.Chmod(a.paths.DataDir, 0o755); err != nil {
		return fmt.Errorf("设置公开状态目录权限失败: %w", err)
	}
	public := make([]publicProfile, 0, len(profiles))
	for _, item := range profiles {
		public = append(public, publicProfile{
			ID: item.ID, Name: item.Name, Kind: item.Kind, Active: item.Active,
			LastUpdated: item.LastUpdated, UpdateInterval: item.UpdateInterval,
			Subscription: item.Subscription,
		})
	}
	state := publicState{SchemaVersion: stateVersion, Profiles: public}
	if a.state != nil && a.state.Installation.ConfigPath != "" {
		content, err := platform.ReadManagedConfig(a.state.Installation.ConfigPath, platform.MaxManagedConfigBytes)
		if err != nil {
			return fmt.Errorf("读取活动 Mihomo 配置失败: %w", err)
		}
		settings, err := effectiveConfigFromYAML(content)
		if err != nil {
			return fmt.Errorf("读取活动 Mihomo 设置失败: %w", err)
		}
		state.Settings = &settings
	}
	content, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(a.paths.PublicFile, append(content, '\n'), 0o644)
}

func (a *App) readPublicProfiles() ([]domain.Profile, error) {
	state, err := a.readPublicState()
	if err != nil {
		return nil, err
	}
	profiles := make([]domain.Profile, 0, len(state.Profiles))
	for _, item := range state.Profiles {
		profiles = append(profiles, domain.Profile{
			ID: item.ID, Name: item.Name, Kind: item.Kind, Active: item.Active,
			LastUpdated: item.LastUpdated, UpdateInterval: item.UpdateInterval,
			Subscription: item.Subscription,
		})
	}
	return profiles, nil
}

func (a *App) readPublicSettings() (domain.EffectiveConfig, bool, error) {
	state, err := a.readPublicState()
	if err != nil {
		return domain.EffectiveConfig{}, false, err
	}
	if state.Settings == nil {
		return domain.EffectiveConfig{}, false, nil
	}
	return *state.Settings, true, nil
}

func (a *App) readPublicState() (publicState, error) {
	content, _, err := readRegularFileNoFollow(a.paths.PublicFile, maxApplicationStateBytes)
	if err != nil {
		return publicState{}, err
	}
	var state publicState
	if err := json.Unmarshal(content, &state); err != nil {
		return publicState{}, &InvalidStateError{Cause: fmt.Errorf("读取公开状态失败: %w", err)}
	}
	if state.SchemaVersion != stateVersion {
		return publicState{}, &InvalidStateError{Cause: fmt.Errorf("不支持的公开状态版本 %d", state.SchemaVersion)}
	}
	return state, nil
}

func effectiveConfigFromYAML(content []byte) (domain.EffectiveConfig, error) {
	var source struct {
		Mode      domain.Mode `yaml:"mode"`
		MixedPort int         `yaml:"mixed-port"`
		AllowLAN  bool        `yaml:"allow-lan"`
		IPv6      bool        `yaml:"ipv6"`
		LogLevel  string      `yaml:"log-level"`
		TUN       struct {
			Enable bool `yaml:"enable"`
		} `yaml:"tun"`
	}
	if err := yaml.Unmarshal(content, &source); err != nil {
		return domain.EffectiveConfig{}, err
	}
	switch source.Mode {
	case domain.ModeRule, domain.ModeGlobal, domain.ModeDirect:
	default:
		source.Mode = domain.ModeRule
	}
	switch source.LogLevel {
	case "silent", "error", "warning", "info", "debug":
	default:
		source.LogLevel = "info"
	}
	if source.MixedPort < 1 || source.MixedPort > 65535 {
		source.MixedPort = 0
	}
	return domain.EffectiveConfig{
		Mode: source.Mode, TUN: source.TUN.Enable, MixedPort: source.MixedPort,
		AllowLAN: source.AllowLAN, IPv6: source.IPv6, LogLevel: source.LogLevel,
	}, nil
}

func isMissing(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission)
}
