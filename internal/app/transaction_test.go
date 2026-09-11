package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"mihomoctl/internal/domain"
	"mihomoctl/internal/platform"
	"mihomoctl/internal/profile"
)

type transactionFixture struct {
	application *App
	runner      *fakeRunner
	paths       Paths
	configPath  string
}

func newTransactionFixture(t *testing.T) transactionFixture {
	t.Helper()
	root := t.TempDir()
	paths := testPaths(root)
	configDir := filepath.Join(root, "mihomo")
	configPath := filepath.Join(configDir, "config.yaml")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := []byte("mixed-port: 7890\nmode: rule\ntun:\n  enable: false\nproxies: []\nproxy-groups: []\nrules:\n  - MATCH,DIRECT\n")
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		t.Fatal(err)
	}
	binaryDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binaryDir, 0o700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(binaryDir, "mihomo")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{binary: binary, configDir: configDir}
	application, err := New(WithPaths(paths), WithRunner(runner), WithEUID(func() int { return 0 }))
	if err != nil {
		t.Fatal(err)
	}
	if err := application.Initialize(context.Background(), domain.InitOptions{}); err != nil {
		t.Fatal(err)
	}
	return transactionFixture{application: application, runner: runner, paths: paths, configPath: configPath}
}

func TestSettingsRollbackAfterClientStateFailure(t *testing.T) {
	fixture := newTransactionFixture(t)
	tracked := []string{fixture.configPath, fixture.paths.ConfigFile, fixture.paths.PublicFile, fixture.paths.ClientFile}
	before := readTrackedFiles(t, tracked)
	t.Setenv("SUDO_UID", "-1")
	t.Setenv("SUDO_GID", "-1")

	if err := fixture.application.SetConfig(context.Background(), "mixed-port", "8899"); err == nil {
		t.Fatal("SetConfig unexpectedly succeeded")
	}
	assertTrackedFiles(t, tracked, before)
	fixture.application.mu.RLock()
	port := fixture.application.state.Settings.MixedPort
	fixture.application.mu.RUnlock()
	if port == nil || *port != 7890 {
		t.Fatalf("in-memory mixed port = %v, want 7890", port)
	}
}

func TestUseProfileRollbackAfterClientStateFailure(t *testing.T) {
	fixture := newTransactionFixture(t)
	secondPath := filepath.Join(filepath.Dir(fixture.configPath), "second.yaml")
	second := []byte("mixed-port: 7891\nmode: rule\ncustom-field: second\nproxies: []\nproxy-groups: []\nrules:\n  - MATCH,DIRECT\n")
	if err := os.WriteFile(secondPath, second, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.application.AddProfile(context.Background(), "第二配置", secondPath, 0); err != nil {
		t.Fatal(err)
	}
	tracked := []string{fixture.configPath, fixture.paths.PublicFile, fixture.paths.ClientFile}
	before := readTrackedFiles(t, tracked)
	profilesBefore, err := fixture.application.Profiles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SUDO_UID", "-1")
	t.Setenv("SUDO_GID", "-1")

	if err := fixture.application.UseProfile(context.Background(), "第二配置"); err == nil {
		t.Fatal("UseProfile unexpectedly succeeded")
	}
	assertTrackedFiles(t, tracked, before)
	profilesAfter, err := fixture.application.Profiles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(profilesAfter) != len(profilesBefore) {
		t.Fatalf("profiles after rollback = %#v", profilesAfter)
	}
	for index := range profilesBefore {
		if profilesBefore[index].ID != profilesAfter[index].ID || profilesBefore[index].Active != profilesAfter[index].Active {
			t.Fatalf("profile rollback mismatch: before=%#v after=%#v", profilesBefore, profilesAfter)
		}
	}
}

func TestMutationReloadsStateAfterWaitingProcess(t *testing.T) {
	fixture := newTransactionFixture(t)
	second, err := New(WithPaths(fixture.paths), WithRunner(fixture.runner), WithEUID(func() int { return 0 }))
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.application.SetConfig(context.Background(), "mixed-port", "8899"); err != nil {
		t.Fatal(err)
	}
	if err := second.SetTUN(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	state, err := loadYAML[persistedState](fixture.paths.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if state.Settings.MixedPort == nil || *state.Settings.MixedPort != 8899 || !state.Settings.TUN {
		t.Fatalf("second process overwrote newer state: %#v", state.Settings)
	}
	config, err := os.ReadFile(fixture.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(config, []byte("mixed-port: 8899")) || !bytes.Contains(config, []byte("enable: true")) {
		t.Fatalf("merged active config lost a setting:\n%s", config)
	}
}

func TestActivationFailureRollbackChecksRestoredService(t *testing.T) {
	fixture := newTransactionFixture(t)
	before := readTrackedFiles(t, []string{fixture.configPath})
	fixture.runner.mu.Lock()
	fixture.runner.active = true
	fixture.runner.restartFailures = 1
	fixture.runner.mu.Unlock()

	started := time.Now()
	err := fixture.application.SetConfig(context.Background(), "mixed-port", "8899")
	if err == nil {
		t.Fatal("SetConfig unexpectedly succeeded")
	}
	if strings.Contains(err.Error(), "rollback also failed") {
		t.Fatalf("restored service was checked with the new controller: %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatalf("activation rollback took too long: %v", time.Since(started))
	}
	assertTrackedFiles(t, []string{fixture.configPath}, before)
}

func TestRollbackCheckpointRejectsUnsafeFilesWithoutBlocking(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		application := &App{}
		target := filepath.Join(t.TempDir(), "target")
		if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "state.yaml")
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		if _, err := application.captureFiles(path); err == nil {
			t.Fatal("symlinked rollback source was accepted")
		}
		content, err := os.ReadFile(target)
		if err != nil || string(content) != "keep" {
			t.Fatalf("symlink target changed: %q, %v", content, err)
		}
	})

	t.Run("fifo", func(t *testing.T) {
		application := &App{}
		path := filepath.Join(t.TempDir(), "state.fifo")
		if err := unix.Mkfifo(path, 0o600); err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		if _, err := application.captureFiles(path); err == nil {
			t.Fatal("FIFO rollback source was accepted")
		}
		if elapsed := time.Since(started); elapsed > time.Second {
			t.Fatalf("FIFO checkpoint blocked for %s", elapsed)
		}
	})

	t.Run("oversized", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "state.yaml")
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(maxApplicationStateBytes + 1); err != nil {
			file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		_, _, err = readRegularFileNoFollow(path, maxApplicationStateBytes)
		if err == nil || errors.Is(err, os.ErrNotExist) {
			t.Fatalf("oversized state error = %v", err)
		}
	})
}

func TestFirstAddedProfileInEmptyStoreIsAppliedAndPublished(t *testing.T) {
	fixture := newTransactionFixture(t)
	fixture.application.mu.Lock()
	store := fixture.application.store
	profiles, err := store.List()
	if err != nil {
		fixture.application.mu.Unlock()
		t.Fatal(err)
	}
	for _, item := range profiles {
		if err := store.Remove(item.ID); err != nil {
			fixture.application.mu.Unlock()
			t.Fatal(err)
		}
	}
	fixture.application.mu.Unlock()

	newPath := filepath.Join(filepath.Dir(fixture.configPath), "first.yaml")
	newConfig := []byte("mixed-port: 7999\nmode: global\ncustom-field: first-added\nproxies: []\nproxy-groups: []\nrules:\n  - MATCH,DIRECT\n")
	if err := os.WriteFile(newPath, newConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.application.AddProfile(context.Background(), "首个配置", newPath, 0); err != nil {
		t.Fatal(err)
	}
	applied, err := os.ReadFile(fixture.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(applied, []byte("custom-field: first-added")) {
		t.Fatalf("first active profile was not applied:\n%s", applied)
	}
	publicProfiles, err := fixture.application.readPublicProfiles()
	if err != nil || len(publicProfiles) != 1 || !publicProfiles[0].Active || publicProfiles[0].Name != "首个配置" {
		t.Fatalf("public profiles = %#v, %v", publicProfiles, err)
	}
	client, err := loadYAML[clientState](fixture.paths.ClientFile)
	if err != nil || client.ActiveProfile != "首个配置" {
		t.Fatalf("client state = %#v, %v", client, err)
	}
}

func TestScheduleToggleUsesSingleNowAction(t *testing.T) {
	fixture := newTransactionFixture(t)
	fixture.application.scheduleCompatibility = func(Paths, persistedState, []domain.Profile) error { return nil }
	for _, test := range []struct {
		enabled bool
		action  string
	}{
		{enabled: true, action: "enable"},
		{enabled: false, action: "disable"},
	} {
		fixture.runner.mu.Lock()
		fixture.runner.calls = nil
		fixture.runner.mu.Unlock()
		if err := fixture.application.SetSchedule(context.Background(), test.enabled); err != nil {
			t.Fatal(err)
		}
		calls := fixture.runner.snapshotCalls()
		if len(calls) != 2 || calls[0] != "systemctl daemon-reload" || calls[1] != "systemctl "+test.action+" --now -- mihomoctl-update.timer" {
			t.Fatalf("schedule enabled=%v calls = %#v", test.enabled, calls)
		}
	}
}

func TestScheduleCompatibilityRejectsSandboxAndCustomStatePaths(t *testing.T) {
	defaults := defaultElevatedPaths()
	baseState := persistedState{Installation: platform.Installation{
		ConfigPath: "/etc/mihomo/config.yaml",
		ConfigDir:  "/etc/mihomo",
	}}
	tests := []struct {
		name     string
		paths    Paths
		state    persistedState
		profiles []domain.Profile
	}{
		{name: "custom app state", paths: func() Paths {
			paths := defaults
			paths.DataDir = "/opt/mihomoctl"
			return paths
		}(), state: baseState},
		{name: "config under home", paths: defaults, state: persistedState{Installation: platform.Installation{
			ConfigPath: "/root/.config/mihomo/config.yaml", ConfigDir: "/root/.config/mihomo",
		}}},
		{name: "config under usr", paths: defaults, state: persistedState{Installation: platform.Installation{
			ConfigPath: "/usr/local/etc/mihomo/config.yaml", ConfigDir: "/usr/local/etc/mihomo",
		}}},
		{name: "local profile in private tmp", paths: defaults, state: baseState, profiles: []domain.Profile{{
			Name: "临时配置", Kind: domain.ProfileLocal, Source: filepath.Join(t.TempDir(), "profile.yaml"), UpdateInterval: time.Hour,
		}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if len(test.profiles) > 0 {
				if err := os.WriteFile(test.profiles[0].Source, []byte("proxies: []\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := validateScheduleCompatibility(test.paths, test.state, test.profiles); err == nil {
				t.Fatal("incompatible timer setup was accepted")
			}
		})
	}
	if err := validateScheduleCompatibility(defaults, baseState, nil); err != nil {
		t.Fatalf("default timer setup rejected: %v", err)
	}
}

func TestSyncClientStateRecreatesCurrentClientCredentials(t *testing.T) {
	fixture := newTransactionFixture(t)
	if err := os.Remove(fixture.paths.ClientFile); err != nil {
		t.Fatal(err)
	}
	if err := fixture.application.SyncClientState(context.Background()); err != nil {
		t.Fatal(err)
	}
	client, err := loadYAML[clientState](fixture.paths.ClientFile)
	if err != nil {
		t.Fatal(err)
	}
	if client.Controller == "" || client.Secret == "" || client.ActiveProfile != "系统原配置" || client.Service != "mihomo.service" {
		t.Fatalf("synced client state = %#v", client)
	}
}

func TestServiceEnableNowUsesSingleSystemdAction(t *testing.T) {
	fixture := newTransactionFixture(t)
	fixture.runner.mu.Lock()
	fixture.runner.calls = nil
	fixture.runner.mu.Unlock()
	if err := fixture.application.Service(context.Background(), "enable-now"); err != nil {
		t.Fatal(err)
	}
	if calls := fixture.runner.snapshotCalls(); len(calls) != 1 || calls[0] != "systemctl enable --now -- mihomo.service" {
		t.Fatalf("service enable-now calls = %#v", calls)
	}
}

func TestRootServiceUsesUnitFromManagedInstallation(t *testing.T) {
	fixture := newTransactionFixture(t)
	fixture.application.paths.DefaultService = "ssh.service"
	fixture.runner.mu.Lock()
	fixture.runner.calls = nil
	fixture.runner.mu.Unlock()

	if err := fixture.application.Service(context.Background(), "stop"); err != nil {
		t.Fatal(err)
	}
	if calls := fixture.runner.snapshotCalls(); len(calls) != 1 || calls[0] != "systemctl stop -- mihomo.service" {
		t.Fatalf("service calls = %#v, want managed mihomo unit", calls)
	}
}

func TestElevatedLocalProfileEnvelopeCreatesImmutableSnapshot(t *testing.T) {
	fixture := newTransactionFixture(t)
	content := "mixed-port: 7890\nmode: rule\ncustom-field: caller-snapshot\nproxies: []\nproxy-groups: []\nrules:\n  - MATCH,DIRECT\n"
	if _, err := fixture.application.AddProfile(context.Background(), "调用者快照", profile.InlineSnapshotPrefix+content, time.Hour); err != nil {
		t.Fatal(err)
	}
	profiles, err := fixture.application.Profiles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range profiles {
		if item.Name != "调用者快照" {
			continue
		}
		if item.Kind != domain.ProfileLocal || item.Source != "[immutable snapshot]" || item.UpdateInterval != 0 {
			t.Fatalf("elevated local snapshot metadata = %#v", item)
		}
		return
	}
	t.Fatal("elevated local snapshot was not stored")
}

func TestRootCoreStatusPrefersManagedSettingsWhenPublicStateLags(t *testing.T) {
	fixture := newTransactionFixture(t)
	secondPath := filepath.Join(filepath.Dir(fixture.configPath), "new-active.yaml")
	secondConfig := []byte("mixed-port: 7890\nallow-lan: true\nmode: rule\ncustom-field: new-active\nproxies: []\nproxy-groups: []\nrules:\n  - MATCH,DIRECT\n")
	if err := os.WriteFile(secondPath, secondConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.application.AddProfile(context.Background(), "新活动配置", secondPath, time.Hour); err != nil {
		t.Fatal(err)
	}
	fixture.application.mu.RLock()
	store := fixture.application.store
	fixture.application.mu.RUnlock()
	profiles, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range profiles {
		if item.Name == "新活动配置" {
			if _, err := store.Use(item.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	status, err := fixture.application.CoreStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.ActiveProfile != "新活动配置" {
		t.Fatalf("root CoreStatus active profile = %q, want store value", status.ActiveProfile)
	}
	if status.AllowLAN {
		t.Fatalf("root CoreStatus trusted profile allow-lan over managed settings: %#v", status)
	}
}

func TestRootCoreStatusRejectsStoreCorruption(t *testing.T) {
	fixture := newTransactionFixture(t)
	statePath := filepath.Join(fixture.paths.ProfileRoot, "state.yaml")
	if err := os.WriteFile(statePath, []byte("version: [broken\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	status, err := fixture.application.CoreStatus(context.Background())
	if err == nil {
		t.Fatal("CoreStatus accepted a corrupt private store")
	}
	if status.ActiveProfile == "" {
		t.Fatalf("CoreStatus discarded the last public profile while reporting corruption: %#v", status)
	}
}

func readTrackedFiles(t *testing.T, paths []string) map[string][]byte {
	t.Helper()
	result := make(map[string][]byte, len(paths))
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		result[path] = content
	}
	return result
}

func assertTrackedFiles(t *testing.T, paths []string, before map[string][]byte) {
	t.Helper()
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(content, before[path]) {
			t.Fatalf("%s was not rolled back\nbefore:\n%s\nafter:\n%s", path, before[path], content)
		}
	}
}
