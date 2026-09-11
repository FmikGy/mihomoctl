package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mihomoctl/internal/i18n"
)

func TestPreferencesRoundTripPreservesFieldsAndUsesPrivateMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mihomoctl", "preferences.yaml")
	preferences := Preferences{Path: path}
	if language, err := preferences.LoadLanguage(); err != nil || language != i18n.Chinese {
		t.Fatalf("missing preference = %q, %v", language, err)
	}
	if err := preferences.SaveLanguage(i18n.English); err != nil {
		t.Fatal(err)
	}
	if language, err := preferences.LoadLanguage(); err != nil || language != i18n.English {
		t.Fatalf("saved preference = %q, %v", language, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("preference mode = %04o", info.Mode().Perm())
	}
	if err := os.WriteFile(path, []byte("theme: compact\nlanguage: zh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := preferences.SaveLanguage(i18n.English); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "theme: compact") || !strings.Contains(string(content), "language: en") {
		t.Fatalf("preference content = %q", content)
	}
}

func TestPreferencesRejectInvalidAndUnsafeFiles(t *testing.T) {
	directory := t.TempDir()
	t.Run("invalid language", func(t *testing.T) {
		path := filepath.Join(directory, "invalid.yaml")
		if err := os.WriteFile(path, []byte("language: french\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := (Preferences{Path: path}).LoadLanguage(); err == nil {
			t.Fatal("invalid language was accepted")
		}
	})
	t.Run("permissive mode", func(t *testing.T) {
		path := filepath.Join(directory, "public.yaml")
		if err := os.WriteFile(path, []byte("language: en\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := (Preferences{Path: path}).LoadLanguage(); err == nil {
			t.Fatal("public preference file was accepted")
		}
	})
	t.Run("symlink", func(t *testing.T) {
		target := filepath.Join(directory, "target.yaml")
		link := filepath.Join(directory, "link.yaml")
		if err := os.WriteFile(target, []byte("language: en\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if _, err := (Preferences{Path: link}).LoadLanguage(); err == nil {
			t.Fatal("symlinked preference file was accepted")
		}
	})
}
