package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mihomoctl/internal/domain"
	"mihomoctl/internal/platform"
)

func TestInitializeRejectsSymlinkedMihomoConfigBeforeImport(t *testing.T) {
	root := t.TempDir()
	paths := testPaths(root)
	configDir := filepath.Join(root, "mihomo")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(configDir, "target.yaml")
	configPath := filepath.Join(configDir, "config.yaml")
	if err := os.WriteFile(target, []byte("mode: rule\nproxies: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, configPath); err != nil {
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
	)
	if err != nil {
		t.Fatal(err)
	}
	err = application.Initialize(context.Background(), domain.InitOptions{})
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("symlinked initial config error = %v", err)
	}
	entries, listErr := os.ReadDir(paths.ProfileRoot)
	if listErr == nil && len(entries) != 0 {
		t.Fatalf("profile data was created before config validation: %#v", entries)
	}
	if listErr != nil && !os.IsNotExist(listErr) {
		t.Fatalf("inspect profile store after rejected config: %v", listErr)
	}
}

func TestWritePublicProfilesRejectsSymlinkedMihomoConfig(t *testing.T) {
	root := t.TempDir()
	paths := testPaths(root)
	configDir := filepath.Join(root, "mihomo")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(configDir, "target.yaml")
	configPath := filepath.Join(configDir, "config.yaml")
	if err := os.WriteFile(target, []byte("mode: direct\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, configPath); err != nil {
		t.Fatal(err)
	}
	application := &App{
		paths: paths,
		state: &persistedState{Installation: platform.Installation{ConfigPath: configPath}},
	}
	err := application.writePublicProfiles([]domain.Profile{{ID: "active", Name: "active", Active: true}})
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("symlinked public-state config error = %v", err)
	}
	if _, err := os.Stat(paths.PublicFile); !os.IsNotExist(err) {
		t.Fatalf("public state was written from a symlinked config: %v", err)
	}
}

func TestWritePublicProfilesRejectsOversizedMihomoConfig(t *testing.T) {
	root := t.TempDir()
	paths := testPaths(root)
	configDir := filepath.Join(root, "mihomo")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDir, "config.yaml")
	file, err := os.Create(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(platform.MaxManagedConfigBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	application := &App{
		paths: paths,
		state: &persistedState{Installation: platform.Installation{ConfigPath: configPath}},
	}
	err = application.writePublicProfiles(nil)
	if err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("oversized public-state config error = %v", err)
	}
}
