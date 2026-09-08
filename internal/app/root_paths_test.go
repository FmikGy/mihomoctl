package app

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

func TestRootApplicationDirectoriesValidatesAndDeduplicatesPaths(t *testing.T) {
	paths := Paths{
		ConfigFile:    "/etc/mihomoctl/state.yaml",
		DataDir:       "/var/lib/mihomoctl",
		ProfileRoot:   "/var/lib/mihomoctl/store",
		BackupDir:     "/var/lib/mihomoctl/backups",
		OperationLock: "/var/lib/mihomoctl/.operation.lock",
		PublicFile:    "/var/lib/mihomoctl/public.json",
	}
	directories, err := rootApplicationDirectories(paths)
	if err != nil {
		t.Fatal(err)
	}
	want := []rootApplicationDirectory{
		{name: "配置状态目录", path: "/etc/mihomoctl"},
		{name: "数据目录", path: "/var/lib/mihomoctl"},
		{name: "配置档案目录", path: "/var/lib/mihomoctl/store"},
		{name: "备份目录", path: "/var/lib/mihomoctl/backups"},
	}
	if !reflect.DeepEqual(directories, want) {
		t.Fatalf("root application directories = %#v, want %#v", directories, want)
	}

	paths.PublicFile = "relative/public.json"
	if _, err := rootApplicationDirectories(paths); err == nil || !strings.Contains(err.Error(), "绝对路径") {
		t.Fatalf("relative public-state path error = %v", err)
	}
	paths.PublicFile = "/var/lib/mihomoctl/../public.json"
	if _, err := rootApplicationDirectories(paths); err == nil || !strings.Contains(err.Error(), "规范路径") {
		t.Fatalf("non-canonical public-state path error = %v", err)
	}
}

func TestEnsureRootApplicationDirectoriesCreatesSecureDirectories(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root directory ownership policy requires a real root process")
	}
	root := t.TempDir()
	paths := testPaths(root)
	paths.ConfigFile = filepath.Join(root, "state", "mihomoctl.yaml")
	paths.OperationLock = filepath.Join(root, "run", "operation.lock")
	paths.PublicFile = filepath.Join(root, "public", "state.json")

	if err := ensureRootApplicationDirectories(paths); err != nil {
		t.Fatal(err)
	}
	directories, err := rootApplicationDirectories(paths)
	if err != nil {
		t.Fatal(err)
	}
	for _, directory := range directories {
		info, err := os.Stat(directory.path)
		if err != nil {
			t.Fatalf("stat %s: %v", directory.path, err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.IsDir() || stat.Uid != 0 || info.Mode().Perm()&0o022 != 0 {
			t.Fatalf("unsafe managed directory %s: mode=%v uid=%v", directory.path, info.Mode(), stat)
		}
	}
}

func TestEnsureRootApplicationDirectoriesRejectsSymlink(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root directory ownership policy requires a real root process")
	}
	root := t.TempDir()
	outside := t.TempDir()
	paths := testPaths(root)
	if err := os.Symlink(outside, paths.DataDir); err != nil {
		t.Fatal(err)
	}
	if err := ensureRootApplicationDirectories(paths); err == nil {
		t.Fatal("symlinked managed directory was accepted")
	}
}

func TestNewAsRootRejectsUnsafeDirectoriesBeforeStateLoad(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root directory ownership policy requires a real root process")
	}
	t.Setenv("SUDO_UID", "")
	root := t.TempDir()
	outside := t.TempDir()
	unsafeDirectory := filepath.Join(root, "state")
	if err := os.Symlink(outside, unsafeDirectory); err != nil {
		t.Fatal(err)
	}
	paths := testPaths(root)
	paths.ConfigFile = filepath.Join(unsafeDirectory, "mihomoctl.yaml")
	if err := os.WriteFile(filepath.Join(outside, "mihomoctl.yaml"), []byte("version: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	application, err := New(WithPaths(paths))
	if application != nil || err == nil || !strings.Contains(err.Error(), "受管目录") {
		t.Fatalf("New() with symlinked state directory = %#v, %v", application, err)
	}
	if _, err := os.Stat(paths.DataDir); !os.IsNotExist(err) {
		t.Fatalf("data directory was created after the first unsafe path: %v", err)
	}
}

func TestNewAsRootRecordsWritableStateFileError(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root state ownership policy requires a real root process")
	}
	t.Setenv("SUDO_UID", "")
	root := t.TempDir()
	paths := testPaths(root)
	paths.OperationLock = filepath.Join(paths.DataDir, ".operation.lock")
	if err := ensureRootApplicationDirectories(paths); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.ConfigFile, []byte("version: 1\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(paths.ConfigFile, 0o666); err != nil {
		t.Fatal(err)
	}

	application, err := New(WithPaths(paths))
	if application == nil || err != nil {
		t.Fatalf("New() = %#v, %v", application, err)
	}
	if application.stateLoadErr == nil || !strings.Contains(application.stateLoadErr.Error(), "writable by group or others") {
		t.Fatalf("writable root state error = %v", application.stateLoadErr)
	}
}

func TestWithMutationRevalidatesRootApplicationDirectories(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root directory ownership policy requires a real root process")
	}
	root := t.TempDir()
	paths := testPaths(root)
	paths.OperationLock = filepath.Join(paths.DataDir, ".operation.lock")
	if err := ensureRootApplicationDirectories(paths); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(paths.DataDir, 0o777); err != nil {
		t.Fatal(err)
	}
	called := false
	application := &App{paths: paths}
	err := application.withMutation(context.Background(), func() error {
		called = true
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "writable by group or others") {
		t.Fatalf("writable managed directory error = %v", err)
	}
	if called {
		t.Fatal("mutation ran after managed directory validation failed")
	}
}
