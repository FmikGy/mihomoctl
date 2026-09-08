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
	"mihomoctl/internal/profile"
	"mihomoctl/internal/tui"
)

const defaultDelayTestURL = "https://www.gstatic.com/generate_204"
const coreVersionCacheTTL = 5 * time.Minute

func (a *App) Status(ctx context.Context) (domain.RuntimeStatus, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	statusCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	var (
		telemetry tui.OverviewTelemetry
		traffic   domain.Traffic
		status    domain.RuntimeStatus
		statusErr error
	)
	client, _ := a.checkAPI()
	coreDone := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		status, statusErr = a.CoreStatus(statusCtx)
		close(coreDone)
	}()
	go func() { defer wait.Done(); telemetry, _ = a.OverviewTelemetry(statusCtx) }()
	if client != nil {
		wait.Add(1)
		go func() { defer wait.Done(); traffic, _ = client.Traffic(statusCtx) }()
	}
	<-coreDone
	if statusErr != nil || !status.Service.Active {
		cancel()
		wait.Wait()
		return status, statusErr
	}
	wait.Wait()
	status.Traffic = traffic
	if telemetry.TrafficTotalsValid {
		status.Traffic.UpTotal = max(status.Traffic.UpTotal, telemetry.Traffic.UpTotal)
		status.Traffic.DownTotal = max(status.Traffic.DownTotal, telemetry.Traffic.DownTotal)
	}
	if telemetry.MemoryValid {
		status.Memory = telemetry.Memory
	}
	if telemetry.ConnectionCountValid {
		status.ConnectionCount = telemetry.ConnectionCount
	}
	return status, nil
}

// CoreStatus returns stable service, profile and configuration state without
// fetching traffic, memory or the potentially large connection snapshot.
func (a *App) CoreStatus(ctx context.Context) (domain.RuntimeStatus, error) {
	systemd, err := platform.NewSystemd(a.runner, a.serviceName())
	if err != nil {
		return domain.RuntimeStatus{}, err
	}
	service, err := systemd.Status(ctx)
	if err != nil {
		return domain.RuntimeStatus{}, err
	}
	status := domain.RuntimeStatus{Service: service}
	publicActiveProfile := ""
	if public, publicErr := a.readPublicState(); publicErr == nil {
		if public.Settings != nil {
			applyEffectiveConfig(&status, *public.Settings)
		}
		for _, item := range public.Profiles {
			if item.Active {
				publicActiveProfile = item.Name
				break
			}
		}
	}
	a.mu.RLock()
	store := a.store
	var privateState *persistedState
	if a.state != nil {
		copy := *a.state
		privateState = &copy
	}
	clientActiveProfile := ""
	if a.client != nil {
		clientActiveProfile = a.client.ActiveProfile
	}
	a.mu.RUnlock()
	status.ActiveProfile = publicActiveProfile
	if a.isRoot() && store != nil {
		// The private store is authoritative for root. public.json is published
		// afterward and can legitimately lag if a process exits between writes.
		active, activeErr := store.Active()
		switch {
		case activeErr == nil:
			status.ActiveProfile = active.Name
		case errors.Is(activeErr, profile.ErrNoActive):
			status.ActiveProfile = ""
		default:
			return status, classifyProfileStoreError(activeErr)
		}
		if privateState != nil {
			if !status.ConfigAvailable || status.ActiveProfile != publicActiveProfile {
				effective, effectiveErr := effectiveStoredConfig(*privateState, store)
				if effectiveErr != nil {
					return status, effectiveErr
				}
				applyEffectiveConfig(&status, effective)
			} else {
				applyManagedSettings(&status, privateState.Settings)
			}
		}
	} else if status.ActiveProfile == "" {
		status.ActiveProfile = clientActiveProfile
	}
	a.mu.RLock()
	api := a.api
	cachedVersion := a.coreVersion
	versionAt := a.coreVersionAt
	a.mu.RUnlock()
	if !service.Active || api == nil {
		return status, nil
	}

	requestCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	var (
		version    mihomo.VersionInfo
		config     mihomo.Config
		versionErr error
		configErr  error
	)
	var wait sync.WaitGroup
	versionFresh := cachedVersion != "" && time.Since(versionAt) < coreVersionCacheTTL
	if !versionFresh {
		wait.Add(1)
		go func() { defer wait.Done(); version, versionErr = api.Version(requestCtx) }()
	}
	wait.Add(1)
	go func() { defer wait.Done(); config, configErr = api.Configs(requestCtx) }()
	wait.Wait()

	if configErr == nil {
		applyEffectiveConfig(&status, domain.EffectiveConfig{
			Mode: config.Mode, TUN: config.TUN.Enable, MixedPort: config.MixedPort,
			AllowLAN: config.AllowLAN, IPv6: config.IPv6, LogLevel: config.LogLevel,
		})
	}
	if versionFresh {
		status.CoreVersion = cachedVersion
	} else if versionErr == nil {
		status.CoreVersion = version.Version
		a.mu.Lock()
		if a.api == api {
			a.coreVersion = version.Version
			a.coreVersionAt = time.Now()
		}
		a.mu.Unlock()
	}
	if versionErr != nil || configErr != nil {
		status.CoreVersion = "控制器不可用"
		return status, &UnavailableError{Message: "Mihomo 控制器不可用", Cause: errors.Join(versionErr, configErr)}
	}
	return status, nil
}

func effectiveStoredConfig(state persistedState, store *profile.Store) (domain.EffectiveConfig, error) {
	raw, err := store.ActiveConfig()
	if err != nil {
		return domain.EffectiveConfig{}, classifyProfileStoreError(err)
	}
	managed, err := profile.MergeManagedConfig(raw, profile.OverlayOptions{
		ExternalController: state.Controller,
		Secret:             state.Secret,
		Settings:           state.Settings,
	})
	if err != nil {
		return domain.EffectiveConfig{}, fmt.Errorf("生成活动配置状态失败: %w", err)
	}
	effective, err := effectiveConfigFromYAML(managed)
	if err != nil {
		return domain.EffectiveConfig{}, fmt.Errorf("读取活动配置状态失败: %w", err)
	}
	return effective, nil
}

func applyManagedSettings(status *domain.RuntimeStatus, settings domain.ManagedSettings) {
	if settings.Mode != "" {
		status.Mode = settings.Mode
	}
	status.TUN = settings.TUN
	if settings.MixedPort != nil {
		status.MixedPort = *settings.MixedPort
	}
	if settings.AllowLAN != nil {
		status.AllowLAN = *settings.AllowLAN
	}
	if settings.IPv6 != nil {
		status.IPv6 = *settings.IPv6
	}
	if settings.LogLevel != "" {
		status.LogLevel = settings.LogLevel
	}
}

// OverviewTelemetry returns only volatile values needed by the overview page.
func (a *App) OverviewTelemetry(ctx context.Context) (tui.OverviewTelemetry, error) {
	client, err := a.checkAPI()
	if err != nil {
		return tui.OverviewTelemetry{}, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	var (
		memory         mihomo.Memory
		connections    mihomo.ConnectionSnapshot
		memoryErr      error
		connectionsErr error
	)
	var wait sync.WaitGroup
	wait.Add(2)
	go func() { defer wait.Done(); memory, memoryErr = client.Memory(requestCtx) }()
	go func() { defer wait.Done(); connections, connectionsErr = client.Connections(requestCtx) }()
	wait.Wait()
	telemetry := tui.OverviewTelemetry{
		Memory:               memory.InUse,
		ConnectionCount:      len(connections.Connections),
		MemoryValid:          memoryErr == nil || connectionsErr == nil,
		ConnectionCountValid: connectionsErr == nil,
		TrafficTotalsValid:   connectionsErr == nil,
	}
	if connectionsErr == nil && connections.Memory > telemetry.Memory {
		telemetry.Memory = connections.Memory
	}
	if connections.DownloadTotal > telemetry.Traffic.DownTotal {
		telemetry.Traffic.DownTotal = connections.DownloadTotal
	}
	if connections.UploadTotal > telemetry.Traffic.UpTotal {
		telemetry.Traffic.UpTotal = connections.UploadTotal
	}
	var telemetryErrors []error
	if memoryErr != nil {
		telemetryErrors = append(telemetryErrors, controllerError(fmt.Errorf("读取内存失败: %w", memoryErr)))
	}
	if connectionsErr != nil {
		telemetryErrors = append(telemetryErrors, controllerError(fmt.Errorf("读取连接失败: %w", connectionsErr)))
	}
	if err := errors.Join(telemetryErrors...); err != nil {
		return telemetry, err
	}
	return telemetry, nil
}

func applyEffectiveConfig(status *domain.RuntimeStatus, config domain.EffectiveConfig) {
	status.ConfigAvailable = true
	status.Mode = config.Mode
	status.TUN = config.TUN
	status.MixedPort = config.MixedPort
	status.AllowLAN = config.AllowLAN
	status.IPv6 = config.IPv6
	status.LogLevel = config.LogLevel
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
	groups, err := client.Groups(ctx)
	if err != nil {
		return nil, controllerError(err)
	}
	testURL := defaultDelayTestURL
	for key, candidate := range groups {
		if key != group && candidate.Name != group {
			continue
		}
		if candidate.TestURL != "" {
			testURL = candidate.TestURL
		}
		break
	}
	delays, err := client.TestGroup(ctx, group, testURL, 5*time.Second)
	return delays, controllerError(err)
}

func (a *App) TestGroupAtURL(ctx context.Context, group, testURL string) (map[string]uint16, error) {
	client, err := a.checkAPI()
	if err != nil {
		return nil, err
	}
	if testURL == "" {
		testURL = defaultDelayTestURL
	}
	delays, err := client.TestGroup(ctx, group, testURL, 5*time.Second)
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

func (a *App) WatchTraffic(ctx context.Context) (<-chan domain.Traffic, <-chan error) {
	traffic := make(chan domain.Traffic, 16)
	errorsCh := make(chan error, 1)
	client, err := a.checkAPI()
	if err != nil {
		close(traffic)
		errorsCh <- err
		close(errorsCh)
		return traffic, errorsCh
	}
	go func() {
		defer close(traffic)
		defer close(errorsCh)
		err := client.StreamTraffic(ctx, func(sample domain.Traffic) error {
			select {
			case traffic <- sample:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		if err != nil && ctx.Err() == nil {
			select {
			case errorsCh <- controllerError(fmt.Errorf("流量流已断开: %w", err)):
			default:
			}
		}
	}()
	return traffic, errorsCh
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
