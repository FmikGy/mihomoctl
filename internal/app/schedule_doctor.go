package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mihomoctl/internal/domain"
	"mihomoctl/internal/platform"
)

func (a *App) SetSchedule(ctx context.Context, enabled bool) error {
	if !a.isRoot() {
		if err := a.requireManagedInstallation(); err != nil {
			return err
		}
		action := "disable"
		if enabled {
			action = "enable"
		}
		return a.runElevated(ctx, []string{"schedule", action, "--output", "json"}, "")
	}
	return a.withMutation(ctx, func() error {
		state, store, err := a.rootStateSnapshot()
		if err != nil {
			return err
		}
		if enabled && os.Geteuid() == 0 && a.scheduleCompatibility != nil {
			updatable, err := store.Updatable()
			if err != nil {
				return err
			}
			if err := a.scheduleCompatibility(a.paths, state, updatable); err != nil {
				return &InvalidInputError{Cause: err}
			}
		}
		if _, err := a.runner.Run(ctx, "systemctl", "daemon-reload"); err != nil {
			return fmt.Errorf("重新载入 systemd 失败: %w", err)
		}
		timer, err := platform.NewSystemd(a.runner, normalizeUnit(a.paths.TimerUnit, ".timer"))
		if err != nil {
			return err
		}
		if enabled {
			return timer.EnableNow(ctx)
		}
		return timer.DisableNow(ctx)
	})
}

func validateScheduleCompatibility(paths Paths, state persistedState, profiles []domain.Profile) error {
	defaults := defaultElevatedPaths()
	managedPaths := []struct {
		name string
		got  string
		want string
	}{
		{"状态文件", paths.ConfigFile, defaults.ConfigFile},
		{"数据目录", paths.DataDir, defaults.DataDir},
		{"配置存储", paths.ProfileRoot, defaults.ProfileRoot},
		{"备份目录", paths.BackupDir, defaults.BackupDir},
		{"操作锁", paths.OperationLock, defaults.OperationLock},
		{"公开状态", paths.PublicFile, defaults.PublicFile},
		{"定时器", paths.TimerUnit, defaults.TimerUnit},
	}
	for _, item := range managedPaths {
		if item.got != item.want {
			return fmt.Errorf("当前%s使用自定义位置 %q，系统定时任务只支持默认受管位置 %q；请保持定时更新关闭并手动更新", item.name, item.got, item.want)
		}
	}
	for _, item := range []struct {
		name string
		path string
	}{
		{"Mihomo 配置文件", state.Installation.ConfigPath},
		{"Mihomo 配置目录", state.Installation.ConfigDir},
	} {
		if timerWriteRestricted(item.path) {
			return fmt.Errorf("%s %q 位于 systemd 定时任务的只读或隔离目录中；请保持定时更新关闭并手动更新", item.name, item.path)
		}
	}
	for _, item := range profiles {
		if item.Kind != domain.ProfileLocal || item.UpdateInterval <= 0 {
			continue
		}
		resolved, err := filepath.EvalSymlinks(item.Source)
		if err != nil {
			return fmt.Errorf("本地配置 %q 无法用于定时更新: %w", item.Name, err)
		}
		if timerPrivatePath(resolved) {
			return fmt.Errorf("本地配置 %q 位于定时任务不可见的临时目录 %q；请移动配置或保持定时更新关闭", item.Name, resolved)
		}
	}
	return nil
}

func timerWriteRestricted(path string) bool {
	for _, root := range []string{"/home", "/root", "/run/user", "/tmp", "/var/tmp", "/usr", "/boot", "/efi"} {
		if pathWithin(path, root) {
			return true
		}
	}
	return false
}

func timerPrivatePath(path string) bool {
	return pathWithin(path, "/tmp") || pathWithin(path, "/var/tmp")
}

func pathWithin(path, root string) bool {
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}

func (a *App) ScheduleStatus(ctx context.Context) (domain.ScheduleStatus, error) {
	timer, err := platform.NewSystemd(a.runner, normalizeUnit(a.paths.TimerUnit, ".timer"))
	if err != nil {
		return domain.ScheduleStatus{}, err
	}
	status, err := timer.Status(ctx)
	if err != nil {
		return domain.ScheduleStatus{}, err
	}
	return domain.ScheduleStatus{Enabled: status.Enabled, State: status.State}, nil
}

func (a *App) Doctor(ctx context.Context, fix bool) ([]domain.DoctorCheck, error) {
	if fix && !a.isRoot() {
		if err := a.runElevated(ctx, []string{"doctor", "--fix", "--output", "json"}, ""); err != nil {
			return nil, err
		}
		a.mu.Lock()
		a.reloadDiskStateLocked()
		a.mu.Unlock()
		fix = false
	}
	checks := make([]domain.DoctorCheck, 0, 6)
	installationOK := false
	binary := "mihomo"
	a.mu.RLock()
	root := a.isRoot()
	if root && a.state != nil {
		binary = a.state.Installation.BinaryPath
	}
	initialized := a.client != nil
	loadErr := a.clientLoadErr
	if root {
		initialized = a.state != nil
		loadErr = a.stateLoadErr
	}
	a.mu.RUnlock()
	if result, err := a.runner.Run(ctx, binary, "-v"); err == nil {
		installationOK = true
		checks = append(checks, domain.DoctorCheck{Name: "Mihomo 内核", OK: true, Message: strings.TrimSpace(string(result.Stdout))})
	} else {
		checks = append(checks, domain.DoctorCheck{Name: "Mihomo 内核", OK: false, Message: err.Error()})
	}
	systemd, systemdErr := platform.NewSystemd(a.runner, a.serviceName())
	if systemdErr == nil {
		status, statusErr := systemd.Status(ctx)
		if statusErr == nil {
			checks = append(checks, domain.DoctorCheck{Name: "systemd 服务", OK: true, Message: status.State})
		} else {
			checks = append(checks, domain.DoctorCheck{Name: "systemd 服务", OK: false, Message: statusErr.Error()})
		}
	} else {
		checks = append(checks, domain.DoctorCheck{Name: "systemd 服务", OK: false, Message: systemdErr.Error()})
	}
	stateMessage := boolMessage(initialized, "状态文件可用", "尚未初始化")
	stateOK := initialized
	if loadErr != nil {
		stateOK = false
		stateMessage = loadErr.Error()
	}
	checks = append(checks, domain.DoctorCheck{Name: "mihomoctl 初始化", OK: stateOK, Message: stateMessage})
	profiles, profileErr := a.Profiles(ctx)
	checks = append(checks, domain.DoctorCheck{Name: "活动配置", OK: profileErr == nil && hasActive(profiles), Message: profileMessage(profiles, profileErr)})
	apiOK := false
	if client, err := a.checkAPI(); err == nil {
		if version, versionErr := client.Version(ctx); versionErr == nil {
			apiOK = true
			checks = append(checks, domain.DoctorCheck{Name: "控制器 API", OK: true, Message: version.Version})
		} else {
			checks = append(checks, domain.DoctorCheck{Name: "控制器 API", OK: false, Message: versionErr.Error()})
		}
	} else {
		checks = append(checks, domain.DoctorCheck{Name: "控制器 API", OK: false, Message: err.Error()})
	}

	if fix && a.isRoot() && installationOK {
		if err := a.withMutation(ctx, func() error {
			state, store, err := a.rootStateSnapshot()
			if err != nil {
				return err
			}
			rollback, err := a.fileRollback(a.paths.PublicFile, a.paths.ClientFile)
			if err != nil {
				return err
			}
			config, err := store.ActiveConfig()
			var applied *appliedProfileConfig
			if err == nil {
				applied, err = a.applyProfileConfig(ctx, state, config)
			}
			if err != nil {
				return err
			}
			rollback.add("Mihomo 配置", applied.rollback)
			profiles, err := store.List()
			if err != nil {
				return a.failAndReload(ctx, rollback, err)
			}
			if err := a.publishProfiles(profiles); err != nil {
				return a.failAndReload(ctx, rollback, err)
			}
			active := ""
			for _, item := range profiles {
				if item.Active {
					active = item.Name
				}
			}
			a.mu.Lock()
			err = a.saveClient(state, active)
			a.mu.Unlock()
			if err != nil {
				return a.failAndReload(ctx, rollback, err)
			}
			return nil
		}); err != nil {
			return checks, err
		}
		for index := range checks {
			if checks[index].Name == "mihomoctl 初始化" || checks[index].Name == "活动配置" {
				checks[index].Fixed = true
			}
		}
	}
	if !apiOK && initialized {
		return checks, nil
	}
	return checks, nil
}

func hasActive(profiles []domain.Profile) bool {
	for _, item := range profiles {
		if item.Active {
			return true
		}
	}
	return false
}

func profileMessage(profiles []domain.Profile, err error) string {
	if err != nil {
		return err.Error()
	}
	for _, item := range profiles {
		if item.Active {
			return item.Name
		}
	}
	return "没有活动配置"
}

func boolMessage(value bool, yes, no string) string {
	if value {
		return yes
	}
	return no
}
