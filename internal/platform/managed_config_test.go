package platform

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestReadManagedConfigReadsBoundedRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	want := []byte("mode: rule\n")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadManagedConfig(path, int64(len(want)))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("managed config = %q, want %q", got, want)
	}
}

func TestReadManagedConfigRejectsSymlink(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target.yaml")
	link := filepath.Join(directory, "config.yaml")
	if err := os.WriteFile(target, []byte("mode: direct\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManagedConfig(link, 64); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("symlink error = %v", err)
	}
}

func TestReadManagedConfigRejectsFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := ReadManagedConfig(path, 64)
		result <- err
	}()
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("FIFO error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("reading a FIFO blocked")
	}
}

func TestReadManagedConfigRejectsOversizedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 65)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManagedConfig(path, 64); err == nil || !strings.Contains(err.Error(), "64-byte size limit") {
		t.Fatalf("oversized config error = %v", err)
	}
}

func TestReadManagedConfigRejectsInvalidPathAndLimit(t *testing.T) {
	if _, err := ReadManagedConfig("relative.yaml", 64); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("relative path error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if _, err := ReadManagedConfig(path, 0); err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("zero limit error = %v", err)
	}
}

func TestExportedRootDirectoryHelpersRejectFilesystemRoot(t *testing.T) {
	if err := EnsureRootManagedDirectory(string(filepath.Separator)); err == nil {
		t.Fatal("EnsureRootManagedDirectory accepted the filesystem root")
	}
	if err := ValidateRootManagedDirectory(string(filepath.Separator)); err == nil {
		t.Fatal("ValidateRootManagedDirectory accepted the filesystem root")
	}
}

func TestRootManagedConfigReadRejectsUntrustedDirectoryOwner(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.yaml")
	if err := os.WriteFile(path, []byte("mode: rule\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(directory, 65534, 65534); err != nil {
			t.Fatal(err)
		}
	}
	fd, err := openManagedConfig(path, true)
	if fd >= 0 {
		_ = unix.Close(fd)
	}
	if err == nil || !strings.Contains(err.Error(), "root-owned directory") {
		t.Fatalf("untrusted directory error = %v", err)
	}
}

func TestRootManagedConfigReadRejectsNonRootFileOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("mode: rule\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(path, 65534, 65534); err != nil {
			t.Fatal(err)
		}
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readManagedConfigFD(fd, path, 64, true); err == nil || !strings.Contains(err.Error(), "owned by root") {
		t.Fatalf("non-root owner error = %v", err)
	}
}

func TestRootManagedConfigRejectsGroupOrWorldWritableFile(t *testing.T) {
	for _, mode := range []uint32{0o620, 0o602, 0o666} {
		t.Run(fmt.Sprintf("%o", mode), func(t *testing.T) {
			if err := validateRootManagedFile(0, unix.S_IFREG|mode); err == nil || !strings.Contains(err.Error(), "writable by group or others") {
				t.Fatalf("root-managed mode %o error = %v", mode, err)
			}
		})
	}
	if err := validateRootManagedFile(0, unix.S_IFREG|0o644); err != nil {
		t.Fatalf("readable root-managed config was rejected: %v", err)
	}
}

func TestReadManagedConfigAsRootRejectsWritableFile(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root ownership policy requires a real root-owned test directory")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("mode: rule\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o660); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManagedConfig(path, 64); err == nil || !strings.Contains(err.Error(), "writable by group or others") {
		t.Fatalf("writable root-owned config error = %v", err)
	}
}

func TestConfigApplierRejectsOversizedConfigBeforeStaging(t *testing.T) {
	fixture := newApplierFixture(t, []byte("mode: old\n"))
	fixture.applier.Activate = func(context.Context) error { return nil }
	fixture.applier.HealthCheck = func(context.Context) error { return nil }
	config := make([]byte, MaxManagedConfigBytes+1)
	if _, err := fixture.applier.Apply(context.Background(), config); err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("oversized apply error = %v", err)
	}
	if len(fixture.runner.calls) != 0 {
		t.Fatalf("oversized config reached validation: %#v", fixture.runner.calls)
	}
	assertFileContents(t, fixture.configPath, "mode: old\n")
}

func TestConfigApplierAsRootRejectsWritableActiveConfig(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root ownership policy requires a real root-owned fixture")
	}
	fixture := newApplierFixture(t, []byte("mode: old\n"))
	if err := os.Chmod(fixture.configPath, 0o660); err != nil {
		t.Fatal(err)
	}
	fixture.applier.Activate = func(context.Context) error { return nil }
	fixture.applier.HealthCheck = func(context.Context) error { return nil }
	_, err := fixture.applier.Apply(context.Background(), []byte("mode: new\n"))
	var applyErr *ApplyError
	if !errors.As(err, &applyErr) || applyErr.Stage != "backup" || !strings.Contains(applyErr.Cause.Error(), "writable by group or others") {
		t.Fatalf("writable active config error = %v", err)
	}
	assertFileContents(t, fixture.configPath, "mode: old\n")
}

func TestConfigApplierRollbackRejectsSymlinkedInstalledConfig(t *testing.T) {
	fixture := newApplierFixture(t, []byte("mode: old\n"))
	activationCalls := 0
	fixture.applier.Activate = func(_ context.Context) error {
		activationCalls++
		return nil
	}
	fixture.applier.HealthCheck = func(context.Context) error { return nil }
	installed := []byte("mode: applied\n")
	result, err := fixture.applier.Apply(context.Background(), installed)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(fixture.configDir, "same-content.yaml")
	if err := os.WriteFile(target, installed, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(fixture.configPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, fixture.configPath); err != nil {
		t.Fatal(err)
	}
	if err := fixture.applier.Rollback(context.Background(), result); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("symlinked rollback error = %v", err)
	}
	if activationCalls != 1 {
		t.Fatalf("activation calls = %d, want 1", activationCalls)
	}
	if info, err := os.Lstat(fixture.configPath); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("installed config symlink was changed: info=%v err=%v", info, err)
	}
}
