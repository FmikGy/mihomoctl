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
	"time"

	"mihomoctl/internal/domain"
	"mihomoctl/internal/profile"
)

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
		args := []string{"profile", "add", "-", "--interval", interval.String(), "--output", "json"}
		if name != "" {
			args = append(args, "--name", name)
		}
		return a.runElevated(ctx, args, source)
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	_, store, err := a.requireRootState()
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

	kind := detectProfileKind(source)
	request := profile.AddRequest{Name: name, Kind: kind, Source: source, UpdateInterval: interval}
	if _, err := store.Add(ctx, request); err != nil {
		return err
	}
	profiles, err = store.List()
	if err != nil {
		return err
	}
	return a.writePublicProfiles(profiles)
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
	a.mu.Lock()
	defer a.mu.Unlock()
	state, store, err := a.requireRootState()
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

	var updateErrors []error
	for _, target := range targets {
		snapshot, snapshotErr := store.Snapshot(target.ID)
		if snapshotErr != nil {
			updateErrors = append(updateErrors, fmt.Errorf("%s: %w", target.Name, snapshotErr))
			continue
		}
		result, updateErr := store.Update(ctx, target.ID)
		if updateErr != nil {
			updateErrors = append(updateErrors, fmt.Errorf("%s: %w", target.Name, updateErr))
			continue
		}
		if target.Active && result.Changed {
			config, configErr := store.Config(target.ID)
			if configErr == nil {
				configErr = a.applyProfileConfig(ctx, state, config)
			}
			if configErr != nil {
				restoreErr := store.Restore(snapshot)
				updateErrors = append(updateErrors, fmt.Errorf("%s 激活失败: %w", target.Name, errors.Join(configErr, restoreErr)))
			}
		}
	}
	profiles, listErr := store.List()
	if listErr == nil {
		listErr = a.writePublicProfiles(profiles)
	}
	if listErr != nil {
		updateErrors = append(updateErrors, listErr)
	}
	return joinErrors("部分配置更新失败", updateErrors)
}

func (a *App) UseProfile(ctx context.Context, name string) error {
	if !a.isRoot() {
		if err := a.requireManagedInstallation(); err != nil {
			return err
		}
		return a.runElevated(ctx, []string{"--output", "json", "profile", "use", "--", name}, "")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	state, store, err := a.requireRootState()
	if err != nil {
		return err
	}
	target, err := resolveProfile(store, name)
	if err != nil {
		return err
	}
	config, err := store.Config(target.ID)
	if err != nil {
		return err
	}
	if err := a.applyProfileConfig(ctx, state, config); err != nil {
		return err
	}
	if _, err := store.Use(target.ID); err != nil {
		return err
	}
	profiles, err := store.List()
	if err != nil {
		return err
	}
	if err := a.writePublicProfiles(profiles); err != nil {
		return err
	}
	return a.saveClient(state, target.Name)
}

func (a *App) RemoveProfile(ctx context.Context, name string) error {
	if !a.isRoot() {
		if err := a.requireManagedInstallation(); err != nil {
			return err
		}
		return a.runElevated(ctx, []string{"--output", "json", "profile", "remove", "--", name}, "")
	}
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
	if err := store.Remove(target.ID); err != nil {
		return err
	}
	profiles, err := store.List()
	if err != nil {
		return err
	}
	return a.writePublicProfiles(profiles)
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
