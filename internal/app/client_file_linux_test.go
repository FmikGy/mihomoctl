package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testClientOwnerPlan(t *testing.T) *clientOwnerPlan {
	t.Helper()
	return &clientOwnerPlan{
		uid: os.Getuid(), gid: os.Getgid(), home: t.TempDir(),
		directoryComponents: []string{".config", "mihomoctl"}, filename: "client.yaml",
	}
}

func TestOwnedClientFileUsesSecureDirectoryTraversal(t *testing.T) {
	plan := testClientOwnerPlan(t)
	if err := writeOwnedClientFile(plan, []byte("secret: first\n"), 0o600, plan.uid, plan.gid); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(append([]string{plan.home}, plan.directoryComponents...)...)
	path := filepath.Join(directory, plan.filename)
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "secret: first\n" {
		t.Fatalf("client state = %q, %v", content, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("client state mode = %v, %v", info, err)
	}
	if info, err := os.Stat(directory); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("client directory mode = %v, %v", info, err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".mihomoctl-") {
			t.Fatalf("temporary client file was retained: %s", entry.Name())
		}
	}
}

func TestOwnedClientFileRejectsSymlinkDirectory(t *testing.T) {
	plan := testClientOwnerPlan(t)
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(plan.home, ".config")); err != nil {
		t.Fatal(err)
	}
	if err := writeOwnedClientFile(plan, []byte("secret: blocked\n"), 0o600, plan.uid, plan.gid); err == nil {
		t.Fatal("symlinked client directory was accepted")
	}
	if entries, err := os.ReadDir(target); err != nil || len(entries) != 0 {
		t.Fatalf("symlink target was modified: entries=%v err=%v", entries, err)
	}
}

func TestClientDirectoryOwnershipNeverAdoptsExistingRootDirectory(t *testing.T) {
	if validClientDirectoryOwner(0, 1000, 0, false) {
		t.Fatal("existing root-owned directory was accepted for a non-root caller")
	}
	if !validClientDirectoryOwner(1000, 1000, 0, false) {
		t.Fatal("existing caller-owned directory was rejected")
	}
	if !validClientDirectoryOwner(0, 1000, 0, true) {
		t.Fatal("directory just created by the root child was rejected before chown")
	}
}

func TestOwnedClientFileReadRejectsSymlinkAndOversizedFiles(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		plan := testClientOwnerPlan(t)
		directory := filepath.Join(append([]string{plan.home}, plan.directoryComponents...)...)
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "target.yaml")
		if err := os.WriteFile(target, []byte("secret: outside\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(directory, plan.filename)); err != nil {
			t.Fatal(err)
		}
		if _, err := readOwnedClientFile(plan); err == nil {
			t.Fatal("symlinked client file was accepted")
		}
	})

	t.Run("oversized", func(t *testing.T) {
		plan := testClientOwnerPlan(t)
		content := []byte(strings.Repeat("x", maxClientStateBytes+1))
		if err := writeOwnedClientFile(plan, content, 0o600, plan.uid, plan.gid); err == nil || !strings.Contains(err.Error(), "过大") {
			t.Fatalf("oversized client write error = %v", err)
		}
		directory := filepath.Join(append([]string{plan.home}, plan.directoryComponents...)...)
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(directory, plan.filename)
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readOwnedClientFile(plan); err == nil || !strings.Contains(err.Error(), "过大") {
			t.Fatalf("oversized client file error = %v", err)
		}
		if _, err := captureOwnedClientFile(path, plan); err == nil || !strings.Contains(err.Error(), "过大") {
			t.Fatalf("oversized checkpoint error = %v", err)
		}
	})

	t.Run("permissive mode", func(t *testing.T) {
		plan := testClientOwnerPlan(t)
		if err := writeOwnedClientFile(plan, []byte("secret: private\n"), 0o600, plan.uid, plan.gid); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(filepath.Join(append([]string{plan.home}, plan.directoryComponents...)...), plan.filename)
		if err := os.Chmod(path, 0o640); err != nil {
			t.Fatal(err)
		}
		if _, err := readOwnedClientFile(plan); err == nil || !strings.Contains(err.Error(), "权限过宽") {
			t.Fatalf("permissive owned client error = %v", err)
		}
	})
}

func TestLocalClientFileRejectsSymlinkAndOversizedFiles(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target.yaml")
	path := filepath.Join(directory, "client.yaml")
	if err := os.WriteFile(target, []byte("secret: outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := readLocalClientFile(path); err == nil {
		t.Fatal("symlinked local client file was accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", maxClientStateBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readLocalClientFile(path); err == nil || !strings.Contains(err.Error(), "过大") {
		t.Fatalf("oversized local client file error = %v", err)
	}
	if err := os.WriteFile(path, []byte("secret: private\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readLocalClientFile(path); err == nil || !strings.Contains(err.Error(), "权限过宽") {
		t.Fatalf("permissive local client error = %v", err)
	}
}

func TestOwnedClientCheckpointRestoresAndRemovesByDirectoryFD(t *testing.T) {
	plan := testClientOwnerPlan(t)
	path := filepath.Join(filepath.Join(append([]string{plan.home}, plan.directoryComponents...)...), plan.filename)
	missing, err := captureOwnedClientFile(path, plan)
	if err != nil || missing.exists {
		t.Fatalf("missing checkpoint = %#v, %v", missing, err)
	}
	if err := writeOwnedClientFile(plan, []byte("secret: temporary\n"), 0o600, plan.uid, plan.gid); err != nil {
		t.Fatal(err)
	}
	if err := restoreOwnedClientFile(missing); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("missing checkpoint did not remove new file: %v", err)
	}

	if err := writeOwnedClientFile(plan, []byte("secret: original\n"), 0o600, plan.uid, plan.gid); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := captureOwnedClientFile(path, plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeOwnedClientFile(plan, []byte("secret: changed\n"), 0o600, plan.uid, plan.gid); err != nil {
		t.Fatal(err)
	}
	if err := restoreOwnedClientFile(checkpoint); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "secret: original\n" {
		t.Fatalf("restored client state = %q, %v", content, err)
	}
}
