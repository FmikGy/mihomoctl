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

type Paths struct {
	ConfigFile     string
	DataDir        string
	ProfileRoot    string
	BackupDir      string
	PublicFile     string
	ClientFile     string
	DefaultService string
	TimerUnit      string
}

func DefaultPaths() Paths {
	dataDir := envOr("MIHOMOCTL_DATA_DIR", "/var/lib/mihomoctl")
	return Paths{
		ConfigFile:     envOr("MIHOMOCTL_CONFIG", "/etc/mihomoctl/config.yaml"),
		DataDir:        dataDir,
		ProfileRoot:    envOr("MIHOMOCTL_PROFILE_DIR", filepath.Join(dataDir, "store")),
		BackupDir:      envOr("MIHOMOCTL_BACKUP_DIR", filepath.Join(dataDir, "backups")),
		PublicFile:     envOr("MIHOMOCTL_PUBLIC_STATE", filepath.Join(dataDir, "public.json")),
		ClientFile:     clientConfigPath(),
		DefaultService: envOr("MIHOMOCTL_SERVICE", "mihomo.service"),
		TimerUnit:      envOr("MIHOMOCTL_TIMER", "mihomoctl-update.timer"),
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
	uid         int
	gid         int
	directories []string
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func clientConfigPath() string {
	if value := strings.TrimSpace(os.Getenv("MIHOMOCTL_CLIENT_CONFIG")); value != "" {
		return value
	}
	if uid := strings.TrimSpace(os.Getenv("SUDO_UID")); uid != "" {
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
	content, err := os.ReadFile(path)
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
	if err := os.Chmod(path, mode); err != nil {
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
	state, err := loadYAML[persistedState](a.paths.ConfigFile)
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
	client, err := loadYAML[clientState](a.paths.ClientFile)
	if err != nil {
		return err
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
	if err := writeYAML(a.paths.ClientFile, client, 0o600); err != nil {
		return err
	}
	if owner != nil {
		for _, directory := range owner.directories {
			if err := os.Chown(directory, owner.uid, owner.gid); err != nil {
				return fmt.Errorf("设置客户端目录所有者失败: %w", err)
			}
			if err := os.Chmod(directory, 0o700); err != nil {
				return fmt.Errorf("设置客户端目录权限失败: %w", err)
			}
		}
		if err := os.Chown(a.paths.ClientFile, owner.uid, owner.gid); err != nil {
			return fmt.Errorf("设置客户端状态所有者失败: %w", err)
		}
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
	target, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	relative, err := filepath.Rel(home, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, errors.New("客户端状态文件必须位于 sudo 用户主目录中")
	}

	directory := filepath.Dir(target)
	relativeDirectory, err := filepath.Rel(home, directory)
	if err != nil {
		return nil, err
	}
	plan := &clientOwnerPlan{uid: uid, gid: gid}
	current := home
	if relativeDirectory != "." {
		for _, component := range strings.Split(relativeDirectory, string(filepath.Separator)) {
			current = filepath.Join(current, component)
			info, statErr := os.Lstat(current)
			switch {
			case errors.Is(statErr, os.ErrNotExist):
				plan.directories = append(plan.directories, current)
			case statErr != nil:
				return nil, statErr
			case info.Mode()&os.ModeSymlink != 0:
				return nil, fmt.Errorf("客户端状态目录不能是符号链接: %s", current)
			case !info.IsDir():
				return nil, fmt.Errorf("客户端状态路径不是目录: %s", current)
			}
		}
	}
	if directory != home && (len(plan.directories) == 0 || plan.directories[len(plan.directories)-1] != directory) {
		plan.directories = append(plan.directories, directory)
	}
	return plan, nil
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
		content, err := os.ReadFile(a.state.Installation.ConfigPath)
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
	content, err := os.ReadFile(a.paths.PublicFile)
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
