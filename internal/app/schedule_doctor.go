package app

import (
	"context"
	"errors"
	"fmt"
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
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, _, err := a.requireRootState(); err != nil {
		return err
	}
	if _, err := a.runner.Run(ctx, "systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("重新载入 systemd 失败: %w", err)
	}
	timer, err := platform.NewSystemd(a.runner, normalizeUnit(a.paths.TimerUnit, ".timer"))
	if err != nil {
		return err
	}
	if enabled {
		if err := timer.Enable(ctx); err != nil {
			return err
		}
		return timer.Start(ctx)
	}
	stopErr := timer.Stop(ctx)
	disableErr := timer.Disable(ctx)
	return errors.Join(stopErr, disableErr)
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
		fix = false
	}
	checks := make([]domain.DoctorCheck, 0, 6)
	installationOK := false
	binary := "mihomo"
	a.mu.RLock()
	if a.state != nil {
		binary = a.state.Installation.BinaryPath
	}
	initialized := a.state != nil || a.client != nil
	loadErr := errors.Join(a.stateLoadErr, a.clientLoadErr)
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
		a.mu.Lock()
		defer a.mu.Unlock()
		state, store, err := a.requireRootState()
		if err != nil {
			return checks, err
		}
		config, err := store.ActiveConfig()
		if err == nil {
			err = a.applyProfileConfig(ctx, state, config)
		}
		if err != nil {
			return checks, err
		}
		profiles, _ := store.List()
		_ = a.writePublicProfiles(profiles)
		active := ""
		for _, item := range profiles {
			if item.Active {
				active = item.Name
			}
		}
		_ = a.saveClient(state, active)
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
