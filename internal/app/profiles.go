package app

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"mihomoctl/internal/domain"
	"mihomoctl/internal/platform"
	"mihomoctl/internal/profile"
)

const profileUpdateBatchSize = 4
const profileSelectionRestoreTimeout = 6 * time.Second
const profileSelectionRestoreWorkers = 4

type preparedProfileUpdate struct {
	target   domain.Profile
	prepared profile.PreparedUpdate
	err      error
}

func (a *App) Profiles(context.Context) ([]domain.Profile, error) {
	if a.isRoot() {
		a.mu.RLock()
		if a.stateLoadErr != nil {
			err := a.stateLoadErr
			a.mu.RUnlock()
			return nil, err
		}
		store := a.store
		a.mu.RUnlock()
		if store != nil {
			return store.List()
		}
	}
	profiles, err := a.readPublicProfiles()
	if err == nil {
		return profiles, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotInitialized
	}
	return nil, err
}

func (a *App) AddProfile(ctx context.Context, name, source string, interval time.Duration) error {
	source = strings.TrimSpace(source)
	name = strings.TrimSpace(name)
	if source == "" {
		return errors.New("配置来源不能为空")
	}
	if interval <= 0 {
		interval = profile.DefaultUpdateInterval
	}
	if !a.isRoot() {
		if err := a.requireManagedInstallation(); err != nil {
			return err
		}
		if detectProfileKind(source) == domain.ProfileLocal {
			if name == "" {
				name = defaultProfileName(source)
			}
			content, err := profile.ReadLocalSource(source)
			if err != nil {
				return err
			}
			source = profile.InlineSnapshotPrefix + string(content)
		}
		args := []string{"profile", "add", "-", "--interval", interval.String(), "--output", "json"}
		if name != "" {
			args = append(args, "--name", name)
		}
		return a.runElevated(ctx, args, source)
	}
	return a.withMutation(ctx, func() error {
		return a.addProfileRoot(ctx, name, source, interval)
	})
}

func (a *App) addProfileRoot(ctx context.Context, name, source string, interval time.Duration) error {
	a.mu.Lock()
	state, store, err := a.requireRootState()
	a.mu.Unlock()
	if err != nil {
		return err
	}
	if name == "" {
		name = defaultProfileName(source)
	}
	profiles, err := store.List()
	if err != nil {
		return err
	}
	for _, item := range profiles {
		if item.Name == name {
			return fmt.Errorf("配置名称 %q 已存在", name)
		}
	}
	storeCheckpoint, err := store.Checkpoint()
	if err != nil {
		return err
	}
	rollback, err := a.fileRollback(a.paths.PublicFile, a.paths.ClientFile)
	if err != nil {
		return err
	}
	rollback.add("配置存储", func(context.Context) error { return store.RestoreCheckpoint(storeCheckpoint) })

	var created domain.Profile
	if strings.HasPrefix(source, profile.InlineSnapshotPrefix) {
		created, err = store.AddSnapshot(name, []byte(strings.TrimPrefix(source, profile.InlineSnapshotPrefix)))
	} else {
		kind := detectProfileKind(source)
		if err := validateLocalProfilePrivilege(runningUnderSudo(), kind); err != nil {
			return err
		}
		request := profile.AddRequest{Name: name, Kind: kind, Source: source, UpdateInterval: interval}
		created, err = store.Add(ctx, request)
	}
	if err != nil {
		return a.failAndReload(ctx, rollback, err)
	}
	createdConfig, err := store.Config(created.ID)
	if err == nil {
		err = profile.ValidateManagedConfigSize(createdConfig, profile.OverlayOptions{
			ExternalController: state.Controller,
			Secret:             state.Secret,
			Settings:           state.Settings,
		}, platform.MaxManagedConfigBytes)
	}
	if err != nil {
		return a.failAndReload(ctx, rollback, fmt.Errorf("配置在应用管理设置后无效: %w", err))
	}
	if created.Active {
		config, configErr := store.Config(created.ID)
		var applied *appliedProfileConfig
		if configErr == nil {
			applied, configErr = a.applyProfileConfig(ctx, state, config)
		}
		if configErr != nil {
			return a.failAndReload(ctx, rollback, configErr)
		}
		rollback.add("Mihomo 配置", applied.rollback)
		a.mu.Lock()
		err = a.saveClient(state, created.Name)
		a.mu.Unlock()
		if err != nil {
			return a.failAndReload(ctx, rollback, err)
		}
	}
	profiles, err = store.List()
	if err != nil {
		return a.failAndReload(ctx, rollback, err)
	}
	a.mu.Lock()
	err = a.writePublicProfiles(profiles)
	a.mu.Unlock()
	if err != nil {
		return a.failAndReload(ctx, rollback, err)
	}
	return nil
}

func validateLocalProfilePrivilege(sudoChild bool, kind domain.ProfileKind) error {
	if sudoChild && kind == domain.ProfileLocal {
		return &InvalidInputError{Cause: errors.New("sudo 子进程拒绝直接读取本地配置路径；请以普通用户运行 mihomoctl，由程序在提权前创建安全快照")}
	}
	return nil
}

func (a *App) UpdateProfile(ctx context.Context, name string) error {
	return a.UpdateProfiles(ctx, domain.ProfileUpdateOptions{Name: name})
}

func (a *App) UpdateProfiles(ctx context.Context, options domain.ProfileUpdateOptions) error {
	if !a.isRoot() {
		if err := a.requireManagedInstallation(); err != nil {
			return err
		}
		args := []string{"--output", "json", "profile", "update"}
		if options.All {
			args = append(args, "--all")
		}
		if options.Due {
			args = append(args, "--due")
		}
		if options.Name != "" {
			args = append(args, "--", options.Name)
		}
		return a.runElevated(ctx, args, "")
	}
	return a.withMutation(ctx, func() error {
		return a.updateProfilesRoot(ctx, options)
	})
}

func (a *App) updateProfilesRoot(ctx context.Context, options domain.ProfileUpdateOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	state, store, err := a.requireRootState()
	a.mu.Unlock()
	if err != nil {
		return err
	}
	var targets []domain.Profile
	switch {
	case options.Due:
		targets, err = store.Due(time.Now())
	case options.All:
		targets, err = store.Updatable()
	case strings.TrimSpace(options.Name) != "":
		var target domain.Profile
		target, err = resolveProfile(store, options.Name)
		targets = []domain.Profile{target}
	default:
		return errors.New("请指定配置名称、--all 或 --due")
	}
	if err != nil {
		return err
	}

	// Keep the active profile in its own first batch. Other profiles are
	// prepared concurrently in bounded batches so source, normalized config,
	// and rollback data cannot grow with the total subscription count.
	sort.SliceStable(targets, func(i, j int) bool { return targets[i].Active && !targets[j].Active })
	var updateErrors []error
	for start := 0; start < len(targets); {
		end := min(start+profileUpdateBatchSize, len(targets))
		if targets[start].Active {
			end = start + 1
		}
		prepared := prepareProfileUpdateBatch(ctx, store, targets[start:end])
		if err := ctx.Err(); err != nil {
			return errors.Join(joinErrors("部分配置更新失败", updateErrors), err)
		}
		batchErrors, fatalErr := a.commitProfileUpdateBatch(ctx, state, store, prepared)
		updateErrors = append(updateErrors, batchErrors...)
		if fatalErr != nil {
			return errors.Join(joinErrors("部分配置更新失败", updateErrors), fatalErr)
		}
		start = end
	}
	return joinErrors("部分配置更新失败", updateErrors)
}

func prepareProfileUpdateBatch(ctx context.Context, store *profile.Store, targets []domain.Profile) []preparedProfileUpdate {
	prepared := make([]preparedProfileUpdate, len(targets))
	var wait sync.WaitGroup
	for index, target := range targets {
		index, target := index, target
		prepared[index].target = target
		wait.Add(1)
		go func() {
			defer wait.Done()
			prepared[index].prepared, prepared[index].err = store.PrepareUpdate(ctx, target.ID)
		}()
	}
	wait.Wait()
	return prepared
}

func (a *App) commitProfileUpdateBatch(
	ctx context.Context,
	state persistedState,
	store *profile.Store,
	prepared []preparedProfileUpdate,
) ([]error, error) {
	ids := make([]string, 0, len(prepared))
	ready := make(map[string]bool, len(prepared))
	var updateErrors []error
	for _, item := range prepared {
		if item.err != nil {
			updateErrors = append(updateErrors, fmt.Errorf("%s: %w", item.target.Name, item.err))
			continue
		}
		if validateErr := item.prepared.ValidateManagedConfig(profile.OverlayOptions{
			ExternalController: state.Controller,
			Secret:             state.Secret,
			Settings:           state.Settings,
		}, platform.MaxManagedConfigBytes); validateErr != nil {
			updateErrors = append(updateErrors, fmt.Errorf("%s: 配置在应用管理设置后无效: %w", item.target.Name, validateErr))
			continue
		}
		ids = append(ids, item.target.ID)
		ready[item.target.ID] = true
	}
	if len(ids) == 0 {
		return updateErrors, nil
	}
	if err := ctx.Err(); err != nil {
		return updateErrors, err
	}
	storeCheckpoint, err := store.Checkpoint(ids...)
	if err != nil {
		return updateErrors, err
	}
	rollback, err := a.fileRollback(a.paths.PublicFile)
	if err != nil {
		return updateErrors, err
	}
	rollback.add("配置存储", func(context.Context) error { return store.RestoreCheckpoint(storeCheckpoint) })

	committed := false
	for _, item := range prepared {
		if item.err != nil || !ready[item.target.ID] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return updateErrors, a.failAndReload(ctx, rollback, err)
		}
		result, updateErr := store.CommitUpdate(item.prepared)
		if updateErr != nil {
			updateErrors = append(updateErrors, fmt.Errorf("%s: %w", item.target.Name, updateErr))
			if errors.Is(updateErr, profile.ErrStaleUpdate) {
				continue
			}
			return updateErrors, a.failAndReload(ctx, rollback, updateErr)
		}
		committed = true
		if item.target.Active && result.Changed {
			config, configErr := store.Config(item.target.ID)
			var applied *appliedProfileConfig
			if configErr == nil {
				applied, configErr = a.applyProfileConfig(ctx, state, config)
			}
			if configErr != nil {
				var applyErr *platform.ApplyError
				if errors.As(configErr, &applyErr) && applyErr.RollbackErr != nil {
					return updateErrors, a.failAndReload(ctx, rollback, fmt.Errorf("%s 激活失败且 Mihomo 配置回滚失败: %w", item.target.Name, configErr))
				}
				restoreErr := store.RestoreCheckpoint(storeCheckpoint)
				if restoreErr != nil {
					return updateErrors, a.failAndReload(ctx, rollback, fmt.Errorf("%s 激活失败且配置存储回滚失败: %w", item.target.Name, errors.Join(configErr, restoreErr)))
				}
				updateErrors = append(updateErrors, fmt.Errorf("%s 激活失败: %w", item.target.Name, configErr))
				return updateErrors, nil
			}
			rollback.add("Mihomo 配置", applied.rollback)
		}
	}
	if !committed {
		return updateErrors, nil
	}
	profiles, err := store.List()
	if err == nil {
		err = a.publishProfiles(profiles)
	}
	if err != nil {
		return updateErrors, a.failAndReload(ctx, rollback, err)
	}
	return updateErrors, nil
}

func (a *App) UseProfile(ctx context.Context, name string) error {
	if !a.isRoot() {
		if err := a.requireManagedInstallation(); err != nil {
			return err
		}
		return a.runElevated(ctx, []string{"--output", "json", "profile", "use", "--", name}, "")
	}
	return a.withMutation(ctx, func() error {
		return a.useProfileRoot(ctx, name)
	})
}

func (a *App) useProfileRoot(ctx context.Context, name string) error {
	state, store, err := a.rootStateSnapshot()
	if err != nil {
		return err
	}
	current, err := store.Active()
	if err != nil {
		return err
	}
	target, err := resolveProfile(store, name)
	if err != nil {
		return err
	}
	if target.ID == current.ID {
		return nil
	}
	config, err := store.Config(target.ID)
	if err != nil {
		return err
	}
	storeCheckpoint, err := store.Checkpoint()
	if err != nil {
		return err
	}
	rollback, err := a.fileRollback(a.paths.PublicFile, a.paths.ClientFile)
	if err != nil {
		return err
	}
	rollback.add("配置存储", func(context.Context) error { return store.RestoreCheckpoint(storeCheckpoint) })
	if err := a.rememberProfileSelections(ctx, store, current.ID); err != nil {
		return a.failAndReload(ctx, rollback, err)
	}
	targetSelections, err := store.Selections(target.ID)
	if err != nil {
		return a.failAndReload(ctx, rollback, err)
	}
	applied, err := a.applyProfileConfig(ctx, state, config)
	if err != nil {
		return a.failAndReload(ctx, rollback, err)
	}
	rollback.add("Mihomo 配置", applied.rollback)
	if _, err := store.Use(target.ID); err != nil {
		return a.failAndReload(ctx, rollback, err)
	}
	profiles, err := store.List()
	if err != nil {
		return a.failAndReload(ctx, rollback, err)
	}
	if err := a.publishProfiles(profiles); err != nil {
		return a.failAndReload(ctx, rollback, err)
	}
	a.mu.Lock()
	err = a.saveClient(state, target.Name)
	a.mu.Unlock()
	if err != nil {
		return a.failAndReload(ctx, rollback, err)
	}
	return a.restoreProfileSelections(ctx, targetSelections)
}

func (a *App) rememberProfileSelections(ctx context.Context, store *profile.Store, id string) error {
	requestCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	groups, err := a.Groups(requestCtx)
	if err != nil {
		// A stopped or temporarily unavailable controller must not prevent a
		// configuration switch. Keep the last successful snapshot instead.
		return nil
	}
	selections := make(map[string]string)
	for _, group := range groups {
		if !strings.EqualFold(group.Type, "selector") || group.Now == "" || !containsName(group.All, group.Now) {
			continue
		}
		selections[group.Name] = group.Now
	}
	return store.SaveSelections(id, selections)
}

func (a *App) restoreProfileSelections(ctx context.Context, selections map[string]string) error {
	if len(selections) == 0 {
		return nil
	}
	requestCtx, cancel := context.WithTimeout(ctx, profileSelectionRestoreTimeout)
	defer cancel()
	var (
		groups  []domain.ProxyGroup
		lastErr error
	)
	for {
		attemptCtx, attemptCancel := context.WithTimeout(requestCtx, 1500*time.Millisecond)
		groups, lastErr = a.Groups(attemptCtx)
		attemptCancel()
		if lastErr == nil && len(groups) > 0 {
			break
		}
		select {
		case <-requestCtx.Done():
			if lastErr == nil {
				lastErr = errors.New("代理组尚未就绪")
			}
			return &OperationWarning{Message: fmt.Sprintf("配置已激活，但无法恢复节点选择: %v", lastErr)}
		case <-time.After(250 * time.Millisecond):
		}
	}
	available := make(map[string]domain.ProxyGroup, len(groups))
	for _, group := range groups {
		available[group.Name] = group
	}
	names := make([]string, 0, len(selections))
	for groupName := range selections {
		names = append(names, groupName)
	}
	sort.Strings(names)
	type selectionJob struct {
		group string
		proxy string
	}
	jobs := make(chan selectionJob)
	failures := make(chan string, len(names))
	var wait sync.WaitGroup
	workers := min(profileSelectionRestoreWorkers, len(names))
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for job := range jobs {
				if err := a.SelectProxy(requestCtx, job.group, job.proxy); err != nil {
					failures <- fmt.Sprintf("%s: %v", job.group, err)
				}
			}
		}()
	}
	for _, groupName := range names {
		group, exists := available[groupName]
		proxyName := selections[groupName]
		if !exists {
			failures <- groupName + ": 策略组不存在"
			continue
		}
		if !strings.EqualFold(group.Type, "selector") {
			failures <- groupName + ": 已不再是手动选择组"
			continue
		}
		if !containsName(group.All, proxyName) {
			failures <- groupName + ": 之前选择的节点已不存在"
			continue
		}
		select {
		case jobs <- selectionJob{group: groupName, proxy: proxyName}:
		case <-requestCtx.Done():
			failures <- groupName + ": 恢复超时"
		}
	}
	close(jobs)
	wait.Wait()
	close(failures)
	items := make([]string, 0, len(names))
	for failure := range failures {
		items = append(items, failure)
	}
	if len(items) == 0 {
		return nil
	}
	sort.Strings(items)
	const detailLimit = 6
	detail := items
	if len(detail) > detailLimit {
		detail = detail[:detailLimit]
	}
	message := "配置已激活，但部分节点选择未恢复: " + strings.Join(detail, "；")
	if len(items) > len(detail) {
		message += fmt.Sprintf("；另有 %d 项", len(items)-len(detail))
	}
	return &OperationWarning{Message: message}
}

func containsName(names []string, wanted string) bool {
	for _, name := range names {
		if name == wanted {
			return true
		}
	}
	return false
}

func (a *App) RemoveProfile(ctx context.Context, name string) error {
	if !a.isRoot() {
		if err := a.requireManagedInstallation(); err != nil {
			return err
		}
		return a.runElevated(ctx, []string{"--output", "json", "profile", "remove", "--", name}, "")
	}
	return a.withMutation(ctx, func() error {
		return a.removeProfileRoot(ctx, name)
	})
}

func (a *App) removeProfileRoot(ctx context.Context, name string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, store, err := a.requireRootState()
	if err != nil {
		return err
	}
	target, err := resolveProfile(store, name)
	if err != nil {
		return err
	}
	if target.Active {
		return errors.New("不能删除当前活动配置，请先切换到其他配置")
	}
	storeCheckpoint, err := store.Checkpoint(target.ID)
	if err != nil {
		return err
	}
	rollback, err := a.fileRollback(a.paths.PublicFile)
	if err != nil {
		return err
	}
	rollback.add("配置存储", func(context.Context) error { return store.RestoreCheckpoint(storeCheckpoint) })
	if err := store.Remove(target.ID); err != nil {
		return rollback.fail(ctx, err)
	}
	profiles, err := store.List()
	if err != nil {
		return rollback.fail(ctx, err)
	}
	if err := a.writePublicProfiles(profiles); err != nil {
		return rollback.fail(ctx, err)
	}
	return nil
}

func resolveProfile(store *profile.Store, value string) (domain.Profile, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return domain.Profile{}, errors.New("配置名称不能为空")
	}
	if byID, err := store.Get(value); err == nil {
		return byID, nil
	}
	profiles, err := store.List()
	if err != nil {
		return domain.Profile{}, err
	}
	matches := make([]domain.Profile, 0, 1)
	for _, item := range profiles {
		if item.Name == value {
			matches = append(matches, item)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return domain.Profile{}, fmt.Errorf("存在多个名为 %q 的配置，请使用 ID", value)
	}
	return domain.Profile{}, fmt.Errorf("找不到配置 %q", value)
}

func detectProfileKind(source string) domain.ProfileKind {
	parsed, err := url.Parse(source)
	if err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" {
		return domain.ProfileRemote
	}
	if info, err := os.Stat(source); err == nil && !info.IsDir() {
		return domain.ProfileLocal
	}
	return domain.ProfileURI
}

func defaultProfileName(source string) string {
	if parsed, err := url.Parse(source); err == nil && parsed.Hostname() != "" && (parsed.Scheme == "http" || parsed.Scheme == "https") {
		return parsed.Hostname()
	}
	if info, err := os.Stat(source); err == nil && !info.IsDir() {
		name := strings.TrimSuffix(filepath.Base(source), filepath.Ext(source))
		if name != "" {
			return name
		}
	}
	return "节点配置-" + time.Now().Format("20060102-150405")
}

func sortedProfiles(profiles []domain.Profile) []domain.Profile {
	result := append([]domain.Profile(nil), profiles...)
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Active != result[j].Active {
			return result[i].Active
		}
		return result[i].Name < result[j].Name
	})
	return result
}
