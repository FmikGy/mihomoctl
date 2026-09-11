package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"mihomoctl/internal/domain"
	"mihomoctl/internal/i18n"
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
			return i18n.Errorf("重新载入 systemd 失败: %w", err)
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
		name i18n.Message
		got  string
		want string
	}{
		{i18n.M("状态文件"), paths.ConfigFile, defaults.ConfigFile},
		{i18n.M("数据目录"), paths.DataDir, defaults.DataDir},
		{i18n.M("配置存储"), paths.ProfileRoot, defaults.ProfileRoot},
		{i18n.M("备份目录"), paths.BackupDir, defaults.BackupDir},
		{i18n.M("操作锁"), paths.OperationLock, defaults.OperationLock},
		{i18n.M("公开状态"), paths.PublicFile, defaults.PublicFile},
		{i18n.M("定时器"), paths.TimerUnit, defaults.TimerUnit},
	}
	for _, item := range managedPaths {
		if item.got != item.want {
			return i18n.Errorf("当前%s使用自定义位置 %q，系统定时任务只支持默认受管位置 %q；请保持定时更新关闭并手动更新", item.name, item.got, item.want)
		}
	}
	for _, item := range []struct {
		name i18n.Message
		path string
	}{
		{i18n.M("Mihomo 配置文件"), state.Installation.ConfigPath},
		{i18n.M("Mihomo 配置目录"), state.Installation.ConfigDir},
	} {
		if timerWriteRestricted(item.path) {
			return i18n.Errorf("%s %q 位于 systemd 定时任务的只读或隔离目录中；请保持定时更新关闭并手动更新", item.name, item.path)
		}
	}
	for _, item := range profiles {
		if item.Kind != domain.ProfileLocal || item.UpdateInterval <= 0 {
			continue
		}
		resolved, err := filepath.EvalSymlinks(item.Source)
		if err != nil {
			return i18n.Errorf("本地配置 %q 无法用于定时更新: %w", item.Name, err)
		}
		if timerPrivatePath(resolved) {
			return i18n.Errorf("本地配置 %q 位于定时任务不可见的临时目录 %q；请移动配置或保持定时更新关闭", item.Name, resolved)
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
	checks := make([]domain.DoctorCheck, 0, 7)
	installationOK := false
	binary := "mihomo"
	a.mu.RLock()
	root := a.isRoot()
	var rootState *persistedState
	if root && a.state != nil {
		binary = a.state.Installation.BinaryPath
		copy := *a.state
		rootState = &copy
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
		checks = append(checks, domain.DoctorCheck{ID: "core", Name: "Mihomo 内核", OK: true, Message: strings.TrimSpace(string(result.Stdout))})
	} else {
		check := domain.DoctorCheck{ID: "core", Name: "Mihomo 内核"}
		setDoctorError(&check, err)
		checks = append(checks, check)
	}
	systemd, systemdErr := platform.NewSystemd(a.runner, a.serviceName())
	if systemdErr == nil {
		status, statusErr := systemd.Status(ctx)
		if statusErr == nil {
			checks = append(checks, domain.DoctorCheck{ID: "service", Name: "systemd 服务", OK: true, Message: status.State})
		} else {
			check := domain.DoctorCheck{ID: "service", Name: "systemd 服务"}
			setDoctorError(&check, statusErr)
			checks = append(checks, check)
		}
	} else {
		check := domain.DoctorCheck{ID: "service", Name: "systemd 服务"}
		setDoctorError(&check, systemdErr)
		checks = append(checks, check)
	}
	if rootState != nil && systemdErr == nil {
		checks = append(checks, checkManagedConfigPermissions(ctx, systemd, rootState.Installation.ConfigPath))
	}
	stateCheck := domain.DoctorCheck{ID: "initialized", Name: "mihomoctl 初始化", OK: initialized}
	if loadErr != nil {
		stateCheck.OK = false
		setDoctorError(&stateCheck, loadErr)
	} else if initialized {
		setDoctorMessage(&stateCheck, "状态文件可用")
	} else {
		setDoctorMessage(&stateCheck, "尚未初始化")
	}
	checks = append(checks, stateCheck)
	profiles, profileErr := a.Profiles(ctx)
	profileCheck := domain.DoctorCheck{ID: "active-profile", Name: "活动配置", OK: profileErr == nil && hasActive(profiles)}
	switch {
	case profileErr != nil:
		setDoctorError(&profileCheck, profileErr)
	case profileCheck.OK:
		for _, item := range profiles {
			if item.Active {
				profileCheck.Message = item.Name
				break
			}
		}
	default:
		setDoctorMessage(&profileCheck, "没有活动配置")
	}
	checks = append(checks, profileCheck)
	apiOK := false
	if client, err := a.checkAPI(); err == nil {
		if version, versionErr := client.Version(ctx); versionErr == nil {
			apiOK = true
			checks = append(checks, domain.DoctorCheck{ID: "controller", Name: "控制器 API", OK: true, Message: version.Version})
		} else {
			check := domain.DoctorCheck{ID: "controller", Name: "控制器 API"}
			setDoctorError(&check, versionErr)
			checks = append(checks, check)
		}
	} else {
		check := domain.DoctorCheck{ID: "controller", Name: "控制器 API"}
		setDoctorError(&check, err)
		checks = append(checks, check)
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
			if checks[index].ID == "initialized" || checks[index].ID == "active-profile" || checks[index].ID == "config-permissions" {
				checks[index].OK = true
				checks[index].Fixed = true
				if checks[index].ID == "config-permissions" {
					setDoctorMessage(&checks[index], "已按 Mihomo 服务用户收紧")
				}
			}
		}
	}
	if !apiOK && initialized {
		return checks, nil
	}
	return checks, nil
}

func checkManagedConfigPermissions(ctx context.Context, systemd *platform.Systemd, path string) domain.DoctorCheck {
	check := domain.DoctorCheck{ID: "config-permissions", Name: "Mihomo 配置权限"}
	access, err := serviceConfigAccess(ctx, systemd)
	if err != nil {
		setDoctorError(&check, err)
		return check
	}
	info, err := os.Lstat(path)
	if err != nil {
		setDoctorMessage(&check, "检查 %s 失败: %v", path, err)
		return check
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	wantMode := os.FileMode(0o600)
	if access.GroupReadable {
		wantMode = 0o640
	}
	if !ok || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		setDoctorMessage(&check, "配置路径不是普通文件")
		return check
	}
	if int(stat.Uid) != access.UID || int(stat.Gid) != access.GID || info.Mode().Perm() != wantMode {
		setDoctorMessage(&check, "当前 %d:%d %04o，期望 %d:%d %04o", stat.Uid, stat.Gid, info.Mode().Perm(), access.UID, access.GID, wantMode)
		return check
	}
	check.OK = true
	check.Message = fmt.Sprintf("%d:%d %04o", stat.Uid, stat.Gid, info.Mode().Perm())
	return check
}

func hasActive(profiles []domain.Profile) bool {
	for _, item := range profiles {
		if item.Active {
			return true
		}
	}
	return false
}

func setDoctorMessage(check *domain.DoctorCheck, key string, args ...any) {
	message := i18n.M(key, args...)
	check.Message = message.String()
	check.MessageKey = key
	check.MessageArgs = append([]any(nil), args...)
	check.MessageError = nil
}

func setDoctorError(check *domain.DoctorCheck, err error) {
	check.Message = err.Error()
	check.MessageKey = ""
	check.MessageArgs = nil
	check.MessageError = err
}
