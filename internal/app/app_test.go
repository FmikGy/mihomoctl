package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mihomoctl/internal/domain"
	"mihomoctl/internal/mihomo"
	"mihomoctl/internal/platform"
)

type fakeRunner struct {
	mu        sync.Mutex
	binary    string
	configDir string
	active    bool
	calls     []string
}

func (r *fakeRunner) Run(_ context.Context, name string, args ...string) (platform.CommandResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	call := strings.Join(append([]string{name}, args...), " ")
	r.calls = append(r.calls, call)

	if name == "systemctl" && containsArg(args, "--property=FragmentPath") {
		output := fmt.Sprintf(
			"LoadState=loaded\nFragmentPath=/etc/systemd/system/mihomo.service\nExecStart={ path=%s ; argv[]=%s -d %s ; ignore_errors=no ; }\n",
			r.binary, r.binary, r.configDir,
		)
		return platform.CommandResult{Stdout: []byte(output)}, nil
	}
	if name == "systemctl" && len(args) > 0 && args[0] == "show" {
		if r.active {
			return platform.CommandResult{Stdout: []byte("LoadState=loaded\nActiveState=active\nSubState=running\nUnitFileState=enabled\nMainPID=123\n")}, nil
		}
		return platform.CommandResult{Stdout: []byte("LoadState=loaded\nActiveState=inactive\nSubState=dead\nUnitFileState=disabled\nMainPID=0\n")}, nil
	}
	if name == r.binary {
		if containsArg(args, "-v") {
			return platform.CommandResult{Stdout: []byte("Mihomo Meta test\n")}, nil
		}
		return platform.CommandResult{}, nil
	}
	return platform.CommandResult{}, nil
}

func (r *fakeRunner) snapshotCalls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

func containsArg(args []string, wanted string) bool {
	for _, arg := range args {
		if arg == wanted {
			return true
		}
	}
	return false
}

func testPaths(root string) Paths {
	data := filepath.Join(root, "data")
	return Paths{
		ConfigFile:     filepath.Join(root, "mihomoctl.yaml"),
		DataDir:        data,
		ProfileRoot:    filepath.Join(data, "store"),
		BackupDir:      filepath.Join(data, "backups"),
		PublicFile:     filepath.Join(data, "public.json"),
		ClientFile:     filepath.Join(root, "client.yaml"),
		DefaultService: "mihomo.service",
		TimerUnit:      "mihomoctl-update.timer",
	}
}

func TestInitializeImportsAndAppliesWithoutStartingService(t *testing.T) {
	root := t.TempDir()
	paths := testPaths(root)
	configDir := filepath.Join(root, "mihomo")
	configPath := filepath.Join(configDir, "config.yaml")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte("mixed-port: 7890\nmode: rule\ncustom-field: keep-me\nproxies: []\nproxy-groups: []\nrules:\n  - MATCH,DIRECT\n")
	if err := os.WriteFile(configPath, original, 0o600); err != nil {
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
	application, err := New(
		WithPaths(paths),
		WithRunner(runner),
		WithEUID(func() int { return 0 }),
		WithExecutable(func() (string, error) { return "/usr/bin/mihomoctl", nil }),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := application.Initialize(context.Background(), domain.InitOptions{}); err != nil {
		t.Fatal(err)
	}

	applied, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"custom-field: keep-me", "external-controller: 127.0.0.1:9090", "secret:"} {
		if !strings.Contains(string(applied), marker) {
			t.Fatalf("applied config is missing %q:\n%s", marker, applied)
		}
	}
	for _, call := range runner.snapshotCalls() {
		if strings.Contains(call, "systemctl start") || strings.Contains(call, "systemctl restart") {
			t.Fatalf("initializing an inactive service changed its runtime state: %s", call)
		}
	}
	profiles, err := application.Profiles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 1 || !profiles[0].Active || profiles[0].Name != "系统原配置" {
		t.Fatalf("unexpected imported profiles: %#v", profiles)
	}
	if err := application.SetConfig(context.Background(), "mixed-port", "8899"); err != nil {
		t.Fatal(err)
	}
	settings, available, err := application.readPublicSettings()
	if err != nil || !available || settings.MixedPort != 8899 {
		t.Fatalf("updated public settings = %#v available=%v err=%v", settings, available, err)
	}
	updatedConfig, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updatedConfig), "mixed-port: 8899") || !strings.Contains(string(updatedConfig), "custom-field: keep-me") {
		t.Fatalf("setting update did not preserve and publish active config:\n%s", updatedConfig)
	}
	for _, call := range runner.snapshotCalls() {
		if strings.Contains(call, "systemctl start") || strings.Contains(call, "systemctl restart") {
			t.Fatalf("changing settings for an inactive service changed its runtime state: %s", call)
		}
	}
	for _, path := range []string{paths.ConfigFile, paths.ClientFile} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %o, want 600", path, info.Mode().Perm())
		}
	}
}

func TestSystemConfigSnapshotSurvivesRemoteSwitchAndBulkUpdates(t *testing.T) {
	root := t.TempDir()
	paths := testPaths(root)
	configDir := filepath.Join(root, "mihomo")
	configPath := filepath.Join(configDir, "config.yaml")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte("mixed-port: 7890\nmode: rule\ncustom-field: original-snapshot\nproxies: []\nproxy-groups: []\nrules:\n  - MATCH,DIRECT\n")
	if err := os.WriteFile(configPath, original, 0o600); err != nil {
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
	application, err := New(
		WithPaths(paths),
		WithRunner(runner),
		WithEUID(func() int { return 0 }),
		WithExecutable(func() (string, error) { return "/usr/bin/mihomoctl", nil }),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := application.Initialize(context.Background(), domain.InitOptions{}); err != nil {
		t.Fatal(err)
	}

	profiles, err := application.Profiles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 1 || profiles[0].Name != "系统原配置" || profiles[0].UpdateInterval != 0 {
		t.Fatalf("initialized profiles = %#v", profiles)
	}
	systemProfile := profiles[0]
	before, err := application.store.Config(systemProfile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, original) {
		t.Fatalf("initial snapshot differs from original:\n%s", before)
	}

	remoteConfig := "mixed-port: 7891\nmode: rule\ncustom-field: remote-active\nproxies: []\nproxy-groups: []\nrules:\n  - MATCH,DIRECT\n"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(remoteConfig))
	}))
	defer server.Close()
	if err := application.AddProfile(context.Background(), "远程配置", server.URL, time.Nanosecond); err != nil {
		t.Fatal(err)
	}
	if err := application.UseProfile(context.Background(), "远程配置"); err != nil {
		t.Fatal(err)
	}
	activeBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(activeBytes), "custom-field: remote-active") {
		t.Fatalf("remote profile was not applied:\n%s", activeBytes)
	}

	assertSnapshotUnchanged := func(stage string) {
		t.Helper()
		stored, readErr := application.store.Config(systemProfile.ID)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !bytes.Equal(stored, original) {
			t.Fatalf("system snapshot changed after %s:\n%s", stage, stored)
		}
	}
	if err := application.UpdateProfiles(context.Background(), domain.ProfileUpdateOptions{All: true}); err != nil {
		t.Fatal(err)
	}
	assertSnapshotUnchanged("--all")
	if err := application.UpdateProfiles(context.Background(), domain.ProfileUpdateOptions{Due: true}); err != nil {
		t.Fatal(err)
	}
	assertSnapshotUnchanged("--due")

	farFutureDue, err := application.store.Due(time.Now().Add(100 * 365 * 24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range farFutureDue {
		if item.ID == systemProfile.ID {
			t.Fatal("system snapshot became due in the far future")
		}
	}
}

func TestPublicProfilesNeverContainSourcesOrValidators(t *testing.T) {
	root := t.TempDir()
	paths := testPaths(root)
	application := &App{paths: paths}
	secret := "token-that-must-not-leak"
	profiles := []domain.Profile{{
		ID: "0123456789abcdef01234567", Name: "订阅", Kind: domain.ProfileRemote,
		Source: "https://example.test/subscribe?token=" + secret, Active: true,
		UpdateInterval: time.Hour, LastUpdated: time.Now().UTC(), ETag: secret,
		LastModified: secret,
	}}
	if err := application.writePublicProfiles(profiles); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(paths.PublicFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{secret, "example.test", `"source"`, `"etag"`, `"last_modified"`} {
		if strings.Contains(strings.ToLower(string(content)), strings.ToLower(forbidden)) {
			t.Fatalf("public state leaked %q:\n%s", forbidden, content)
		}
	}
	info, err := os.Stat(paths.PublicFile)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("public state mode = %o, want 644", info.Mode().Perm())
	}
	directoryInfo, err := os.Stat(paths.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if directoryInfo.Mode().Perm() != 0o755 {
		t.Fatalf("public state directory mode = %o, want 755", directoryInfo.Mode().Perm())
	}
	decoded, err := application.readPublicProfiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 1 || decoded[0].Source != "" || decoded[0].Name != "订阅" {
		t.Fatalf("unexpected public profile: %#v", decoded)
	}
}

func TestPublicStateUsesEffectiveAppliedConfig(t *testing.T) {
	root := t.TempDir()
	paths := testPaths(root)
	configPath := filepath.Join(root, "mihomo", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte("mode: direct\nmixed-port: 8899\nallow-lan: true\nipv6: true\nlog-level: warning\ntun:\n  enable: true\nsecret: must-not-leak\nproxy-providers:\n  private:\n    url: https://example.test/sub?token=must-not-leak\n")
	if err := os.WriteFile(configPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	managedPort := 7890
	application := &App{
		paths: paths,
		state: &persistedState{
			Installation: platform.Installation{ConfigPath: configPath},
			Settings:     domain.ManagedSettings{Mode: domain.ModeGlobal, MixedPort: &managedPort},
		},
	}
	if err := application.writePublicProfiles([]domain.Profile{{ID: "active", Name: "日常", Active: true}}); err != nil {
		t.Fatal(err)
	}
	state, err := application.readPublicState()
	if err != nil {
		t.Fatal(err)
	}
	want := domain.EffectiveConfig{
		Mode: domain.ModeDirect, TUN: true, MixedPort: 8899,
		AllowLAN: true, IPv6: true, LogLevel: "warning",
	}
	if state.SchemaVersion != 1 || state.Settings == nil || *state.Settings != want {
		t.Fatalf("public state = %#v, want settings %#v", state, want)
	}
	publicContent, err := os.ReadFile(paths.PublicFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(publicContent), "must-not-leak") || strings.Contains(string(publicContent), "example.test") {
		t.Fatalf("public settings leaked private config: %s", publicContent)
	}
}

func TestEffectiveConfigDefaultsAndLegacyPublicState(t *testing.T) {
	settings, err := effectiveConfigFromYAML([]byte("proxies: []\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := domain.EffectiveConfig{Mode: domain.ModeRule, LogLevel: "info"}
	if settings != want {
		t.Fatalf("default settings = %#v, want %#v", settings, want)
	}

	paths := testPaths(t.TempDir())
	if err := os.MkdirAll(paths.DataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{"schema_version":1,"profiles":[{"id":"old","name":"旧配置","kind":"local","active":true}]}`)
	if err := os.WriteFile(paths.PublicFile, legacy, 0o644); err != nil {
		t.Fatal(err)
	}
	application := &App{paths: paths}
	if _, available, err := application.readPublicSettings(); err != nil || available {
		t.Fatalf("legacy settings available=%v err=%v", available, err)
	}
	profiles, err := application.readPublicProfiles()
	if err != nil || len(profiles) != 1 || profiles[0].Name != "旧配置" {
		t.Fatalf("legacy profiles = %#v, err=%v", profiles, err)
	}
}

func TestStatusUsesSnapshotWhileServiceIsStopped(t *testing.T) {
	paths := testPaths(t.TempDir())
	if err := os.MkdirAll(paths.DataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settings := domain.EffectiveConfig{
		Mode: domain.ModeGlobal, TUN: true, MixedPort: 8899,
		AllowLAN: true, IPv6: true, LogLevel: "debug",
	}
	content, err := json.Marshal(publicState{SchemaVersion: 1, Profiles: []publicProfile{}, Settings: &settings})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.PublicFile, content, 0o644); err != nil {
		t.Fatal(err)
	}
	application := &App{
		paths: paths, runner: &fakeRunner{}, euid: func() int { return 1000 },
		client: &clientState{Service: "mihomo.service"},
	}
	status, err := application.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.ConfigAvailable || status.Mode != settings.Mode || status.MixedPort != settings.MixedPort || !status.TUN || !status.AllowLAN || !status.IPv6 || status.LogLevel != settings.LogLevel {
		t.Fatalf("stopped status did not retain settings: %#v", status)
	}
}

func TestStatusLiveAPIOverridesPublicSnapshot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/version":
			_, _ = writer.Write([]byte(`{"version":"1.19.0"}`))
		case "/configs":
			_, _ = writer.Write([]byte(`{"mode":"global","mixed-port":9999,"allow-lan":true,"ipv6":true,"log-level":"debug","tun":{"enable":true}}`))
		case "/traffic":
			_, _ = writer.Write([]byte(`{"up":1,"down":2}`))
		case "/memory":
			_, _ = writer.Write([]byte(`{"inuse":3}`))
		case "/connections":
			_, _ = writer.Write([]byte(`{"connections":[]}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	api, err := mihomo.New(server.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	paths := testPaths(t.TempDir())
	if err := os.MkdirAll(paths.DataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	snapshot := domain.EffectiveConfig{Mode: domain.ModeDirect, MixedPort: 7777, LogLevel: "warning"}
	publicContent, err := json.Marshal(publicState{SchemaVersion: 1, Profiles: []publicProfile{}, Settings: &snapshot})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.PublicFile, publicContent, 0o644); err != nil {
		t.Fatal(err)
	}
	application := &App{
		paths: paths, runner: &fakeRunner{active: true}, euid: func() int { return 1000 },
		client: &clientState{Service: "mihomo.service"}, api: api,
	}
	status, err := application.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.ConfigAvailable || status.Mode != domain.ModeGlobal || status.MixedPort != 9999 || !status.TUN || !status.AllowLAN || !status.IPv6 || status.LogLevel != "debug" {
		t.Fatalf("live config did not override snapshot: %#v", status)
	}
}

func TestSyncPublicStateIsIdempotentAndDoesNotTouchRuntime(t *testing.T) {
	root := t.TempDir()
	paths := testPaths(root)
	configDir := filepath.Join(root, "mihomo")
	configPath := filepath.Join(configDir, "config.yaml")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte("mixed-port: 7898\nallow-lan: true\nmode: rule\nlog-level: info\nproxies: []\nproxy-groups: []\nrules:\n  - MATCH,DIRECT\n")
	if err := os.WriteFile(configPath, original, 0o600); err != nil {
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
	legacy := []byte(`{"schema_version":1,"profiles":[]}`)
	if err := os.WriteFile(paths.PublicFile, legacy, 0o644); err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	runner.calls = nil
	runner.mu.Unlock()
	appliedBefore, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := application.SyncPublicState(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if calls := runner.snapshotCalls(); len(calls) != 0 {
		t.Fatalf("public sync touched runtime: %v", calls)
	}
	appliedAfter, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(appliedBefore, appliedAfter) {
		t.Fatal("public sync changed the active Mihomo config")
	}
	settings, available, err := application.readPublicSettings()
	if err != nil || !available || settings.MixedPort != 7898 || !settings.AllowLAN {
		t.Fatalf("synced settings = %#v available=%v err=%v", settings, available, err)
	}
}

func TestSetConfigRejectsInvalidValueBeforeMutation(t *testing.T) {
	runner := &fakeRunner{}
	application, err := New(
		WithPaths(testPaths(t.TempDir())),
		WithRunner(runner),
		WithEUID(func() int { return 0 }),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := application.SetConfig(context.Background(), "mixed-port", "70000"); err == nil {
		t.Fatal("SetConfig accepted an invalid port")
	}
	if calls := runner.snapshotCalls(); len(calls) != 0 {
		t.Fatalf("invalid setting executed host commands: %v", calls)
	}
}

func TestInitializeRejectsNonLoopbackControllerBeforeElevation(t *testing.T) {
	runner := &fakeRunner{}
	application, err := New(
		WithPaths(testPaths(t.TempDir())),
		WithRunner(runner),
		WithEUID(func() int { return 1000 }),
		WithExecutable(func() (string, error) {
			t.Fatal("invalid controller attempted to execute sudo")
			return "", nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	err = application.Initialize(context.Background(), domain.InitOptions{Controller: "0.0.0.0:9090"})
	var coded interface{ ExitCode() int }
	if !errors.As(err, &coded) || coded.ExitCode() != 2 {
		t.Fatalf("Initialize() error = %T %v, want exit code 2", err, err)
	}
	if calls := runner.snapshotCalls(); len(calls) != 0 {
		t.Fatalf("invalid controller executed host commands: %v", calls)
	}
}

func TestStatusReturnsUnavailableWhenRunningControllerFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusServiceUnavailable)
		_, _ = writer.Write([]byte(`{"message":"not ready"}`))
	}))
	defer server.Close()

	api, err := mihomo.New(server.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	paths := testPaths(t.TempDir())
	if err := os.MkdirAll(paths.DataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	snapshot := domain.EffectiveConfig{Mode: domain.ModeDirect, MixedPort: 7777, AllowLAN: true, LogLevel: "warning"}
	publicContent, err := json.Marshal(publicState{SchemaVersion: 1, Profiles: []publicProfile{}, Settings: &snapshot})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.PublicFile, publicContent, 0o644); err != nil {
		t.Fatal(err)
	}
	application := &App{
		paths:  paths,
		runner: &fakeRunner{active: true},
		euid:   func() int { return 1000 },
		client: &clientState{Service: "mihomo.service", ActiveProfile: "日常"},
		api:    api,
	}
	status, err := application.Status(context.Background())
	if !status.Service.Active || status.CoreVersion != "控制器不可用" {
		t.Fatalf("partial status = %#v", status)
	}
	if !status.ConfigAvailable || status.Mode != snapshot.Mode || status.MixedPort != snapshot.MixedPort || !status.AllowLAN || status.LogLevel != snapshot.LogLevel {
		t.Fatalf("controller failure discarded snapshot: %#v", status)
	}
	var coded interface{ ExitCode() int }
	if !errors.As(err, &coded) || coded.ExitCode() != 4 {
		t.Fatalf("Status() error = %T %v, want exit code 4", err, err)
	}
}

func TestGroupDelayUsesConfiguredTestURL(t *testing.T) {
	const configuredURL = "https://probe.example/generate_204?source=profile"
	testedURLs := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/proxies":
			_, _ = writer.Write([]byte(`{"proxies":{"Node":{"name":"Node","type":"VLESS","alive":true},"AUTO":{"name":"AUTO","type":"URLTest","all":["Node"],"testUrl":"` + configuredURL + `"}}}`))
		case "/group/AUTO/delay":
			testedURLs <- request.URL.Query().Get("url")
			if timeout := request.URL.Query().Get("timeout"); timeout != "5000" {
				t.Errorf("delay timeout = %q, want 5000", timeout)
			}
			_, _ = writer.Write([]byte(`{"Node":123}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	api, err := mihomo.New(server.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	application := &App{api: api, euid: func() int { return 1000 }}
	delays, err := application.TestGroup(context.Background(), "AUTO")
	if err != nil {
		t.Fatal(err)
	}
	if delays["Node"] != 123 {
		t.Fatalf("delays = %#v", delays)
	}
	if testedURL := <-testedURLs; testedURL != configuredURL {
		t.Fatalf("delay URL = %q, want %q", testedURL, configuredURL)
	}
}

func TestNewAllowsPublicStateOutsideDefaultDataDirectory(t *testing.T) {
	root := t.TempDir()
	paths := testPaths(root)
	paths.PublicFile = filepath.Join(root, "runtime", "public.json")
	if _, err := New(
		WithPaths(paths),
		WithRunner(&fakeRunner{}),
		WithEUID(func() int { return 1000 }),
	); err != nil {
		t.Fatalf("New rejected a valid public state override: %v", err)
	}
}

func TestStableApplicationExitCodes(t *testing.T) {
	if code := ErrNotInitialized.ExitCode(); code != 4 {
		t.Fatalf("not initialized exit code = %d", code)
	}
	permission := &PrivilegeError{Cause: os.ErrPermission}
	if code := permission.ExitCode(); code != 3 {
		t.Fatalf("permission exit code = %d", code)
	}
	classified := classifyProfileStoreError(&os.PathError{Op: "open", Path: "/private", Err: os.ErrPermission})
	if !errors.Is(classified, os.ErrPermission) {
		t.Fatalf("profile store permission was reclassified: %T %v", classified, classified)
	}
	corrupt := classifyProfileStoreError(errors.New("decode profile state"))
	var coded interface{ ExitCode() int }
	if !errors.As(corrupt, &coded) || coded.ExitCode() != 2 {
		t.Fatalf("corrupt profile state = %T %v, want exit code 2", corrupt, corrupt)
	}
}

func TestControllerFailuresUseUnavailableExitCode(t *testing.T) {
	for _, err := range []error{
		errors.New("dial tcp: connection refused"),
		&mihomo.APIError{StatusCode: 401, Message: "unauthorized"},
		&mihomo.APIError{StatusCode: 503, Message: "starting"},
	} {
		var coded interface{ ExitCode() int }
		wrapped := controllerError(err)
		if !errors.As(wrapped, &coded) || coded.ExitCode() != 4 {
			t.Fatalf("controllerError(%v) = %T %v, want exit code 4", err, wrapped, wrapped)
		}
	}
	notFound := &mihomo.APIError{StatusCode: 404, Message: "missing group"}
	if wrapped := controllerError(notFound); wrapped != notFound {
		t.Fatalf("business API error was reclassified: %T %v", wrapped, wrapped)
	}
}

func TestNewDefersInvalidClientStateForDiagnostics(t *testing.T) {
	paths := testPaths(t.TempDir())
	if err := os.WriteFile(paths.ClientFile, []byte("version: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	application, err := New(
		WithPaths(paths),
		WithRunner(runner),
		WithEUID(func() int { return 1000 }),
	)
	if err != nil {
		t.Fatalf("New must remain available for help and doctor: %v", err)
	}
	_, apiErr := application.checkAPI()
	var coded interface{ ExitCode() int }
	if !errors.As(apiErr, &coded) || coded.ExitCode() != 2 {
		t.Fatalf("invalid client state = %T %v, want exit code 2", apiErr, apiErr)
	}
	checks, err := application.Doctor(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, check := range checks {
		if check.Name == "mihomoctl 初始化" && !check.OK && strings.Contains(check.Message, "状态文件无效") {
			found = true
		}
	}
	if !found {
		t.Fatalf("doctor did not expose invalid state: %#v", checks)
	}
}

func TestUninitializedMutationDoesNotAttemptElevation(t *testing.T) {
	application, err := New(
		WithPaths(testPaths(t.TempDir())),
		WithRunner(&fakeRunner{}),
		WithEUID(func() int { return 1000 }),
		WithExecutable(func() (string, error) {
			t.Fatal("uninitialized mutation attempted to execute sudo")
			return "", nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	err = application.UpdateProfiles(context.Background(), domain.ProfileUpdateOptions{Due: true})
	var coded interface{ ExitCode() int }
	if !errors.As(err, &coded) || coded.ExitCode() != 4 {
		t.Fatalf("uninitialized update = %T %v, want exit code 4", err, err)
	}
}
