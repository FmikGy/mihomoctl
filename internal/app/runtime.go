package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"mihomoctl/internal/domain"
	"mihomoctl/internal/mihomo"
	"mihomoctl/internal/platform"
)

func (a *App) Status(ctx context.Context) (domain.RuntimeStatus, error) {
	systemd, err := platform.NewSystemd(a.runner, a.serviceName())
	if err != nil {
		return domain.RuntimeStatus{}, err
	}
	service, err := systemd.Status(ctx)
	if err != nil {
		return domain.RuntimeStatus{}, err
	}
	status := domain.RuntimeStatus{Service: service}
	if profiles, profileErr := a.Profiles(ctx); profileErr == nil {
		for _, item := range profiles {
			if item.Active {
				status.ActiveProfile = item.Name
				break
			}
		}
	} else {
		a.mu.RLock()
		if a.client != nil {
			status.ActiveProfile = a.client.ActiveProfile
		}
		a.mu.RUnlock()
	}
	a.mu.RLock()
	api := a.api
	a.mu.RUnlock()
	if !service.Active || api == nil {
		return status, nil
	}

	requestCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	var (
		version     mihomo.VersionInfo
		config      mihomo.Config
		traffic     domain.Traffic
		memory      mihomo.Memory
		connections mihomo.ConnectionSnapshot
		versionErr  error
		configErr   error
	)
	var wait sync.WaitGroup
	wait.Add(5)
	go func() { defer wait.Done(); version, versionErr = api.Version(requestCtx) }()
	go func() { defer wait.Done(); config, configErr = api.Configs(requestCtx) }()
	go func() { defer wait.Done(); traffic, _ = api.Traffic(requestCtx) }()
	go func() { defer wait.Done(); memory, _ = api.Memory(requestCtx) }()
	go func() { defer wait.Done(); connections, _ = api.Connections(requestCtx) }()
	wait.Wait()

	if versionErr != nil || configErr != nil {
		status.CoreVersion = "控制器不可用"
		return status, &UnavailableError{Message: "Mihomo 控制器不可用", Cause: errors.Join(versionErr, configErr)}
	}
	status.CoreVersion = version.Version
	status.Mode = config.Mode
	status.TUN = config.TUN.Enable
	status.MixedPort = config.MixedPort
	status.AllowLAN = config.AllowLAN
	status.IPv6 = config.IPv6
	status.LogLevel = config.LogLevel
	status.Traffic = traffic
	status.Memory = memory.InUse
	if connections.Memory > status.Memory {
		status.Memory = connections.Memory
	}
	status.ConnectionCount = len(connections.Connections)
	if connections.DownloadTotal > status.Traffic.DownTotal {
		status.Traffic.DownTotal = connections.DownloadTotal
	}
	if connections.UploadTotal > status.Traffic.UpTotal {
		status.Traffic.UpTotal = connections.UploadTotal
	}
	return status, nil
}

func (a *App) Groups(ctx context.Context) ([]domain.ProxyGroup, error) {
	client, err := a.checkAPI()
	if err != nil {
		return nil, err
	}
	groups, err := client.Groups(ctx)
	if err != nil {
		return nil, controllerError(err)
	}
	result := make([]domain.ProxyGroup, 0, len(groups))
	for _, group := range groups {
		if !group.Hidden {
			result = append(result, group)
		}
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func (a *App) Connections(ctx context.Context) ([]domain.Connection, error) {
	client, err := a.checkAPI()
	if err != nil {
		return nil, err
	}
	connections, err := client.ListConnections(ctx)
	return connections, controllerError(err)
}

func (a *App) SelectProxy(ctx context.Context, group, proxyName string) error {
	client, err := a.checkAPI()
	if err != nil {
		return err
	}
	return controllerError(client.SelectProxy(ctx, group, proxyName))
}

func (a *App) TestGroup(ctx context.Context, group string) (map[string]uint16, error) {
	client, err := a.checkAPI()
	if err != nil {
		return nil, err
	}
	delays, err := client.TestGroup(ctx, group, "https://www.gstatic.com/generate_204", 5*time.Second)
	return delays, controllerError(err)
}

func (a *App) CloseConnection(ctx context.Context, id string) error {
	client, err := a.checkAPI()
	if err != nil {
		return err
	}
	return controllerError(client.CloseConnection(ctx, id))
}

func (a *App) CloseAllConnections(ctx context.Context) error {
	client, err := a.checkAPI()
	if err != nil {
		return err
	}
	return controllerError(client.CloseAllConnections(ctx))
}

func (a *App) WatchLogs(ctx context.Context, level string) (<-chan domain.LogEntry, <-chan error) {
	entries := make(chan domain.LogEntry, 128)
	errorsCh := make(chan error, 1)
	client, err := a.checkAPI()
	if err != nil {
		close(entries)
		errorsCh <- err
		close(errorsCh)
		return entries, errorsCh
	}
	go func() {
		defer close(entries)
		defer close(errorsCh)
		err := client.StreamLogs(ctx, level, func(entry domain.LogEntry) error {
			select {
			case entries <- entry:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		if err != nil && ctx.Err() == nil {
			select {
			case errorsCh <- controllerError(fmt.Errorf("日志流已断开: %w", err)):
			default:
			}
		}
	}()
	return entries, errorsCh
}
