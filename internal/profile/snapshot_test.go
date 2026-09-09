package profile

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnapshotRestore(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.AddURI(context.Background(), "原始", "ss://YWVzLTEyOC1nY206cGFzcw@example.com:443#one")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(context.Background(), created.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Restore(snapshot); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "原始" {
		t.Fatalf("restored profile name = %q", got.Name)
	}
}

func TestCheckpointRestoresAddRemoveUseAndUpdate(t *testing.T) {
	ctx := context.Background()
	localPath := filepath.Join(t.TempDir(), "local.yaml")
	if err := os.WriteFile(localPath, []byte(validYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	local, err := store.AddLocal(ctx, "Local", localPath)
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.AddURI(ctx, "Other", "trojan://other-secret@example.net:443#Other")
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := store.Checkpoint(local.ID, other.ID)
	if err != nil {
		t.Fatal(err)
	}

	changedYAML := strings.Replace(validYAML, "one.example", "changed.example", 1)
	if err := os.WriteFile(localPath, []byte(changedYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(ctx, local.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Remove(other.ID); err != nil {
		t.Fatal(err)
	}
	added, err := store.AddURI(ctx, "Added", "trojan://added-secret@example.org:443#Added")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Use(added.ID); err != nil {
		t.Fatal(err)
	}

	if err := store.RestoreCheckpoint(checkpoint); err != nil {
		t.Fatal(err)
	}
	profiles, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 2 || profiles[0].ID != local.ID || !profiles[0].Active || profiles[1].ID != other.ID {
		t.Fatalf("restored profiles = %#v", profiles)
	}
	config, err := store.Config(local.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(config), "one.example") || strings.Contains(string(config), "changed.example") {
		t.Fatalf("restored config = %q", config)
	}
	if _, err := os.Stat(store.profileDir(added.ID)); !os.IsNotExist(err) {
		t.Fatalf("added profile directory still exists: %v", err)
	}
	if _, err := store.Get(other.ID); err != nil {
		t.Fatalf("removed profile was not restored: %v", err)
	}
}

func TestSelectionsPersistAndFollowCheckpointRollback(t *testing.T) {
	storeRoot := t.TempDir()
	store, err := NewStore(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.AddURI(context.Background(), "First", "trojan://first@example.com:443#First")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.AddURI(context.Background(), "Second", "trojan://second@example.com:443#Second")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"Proxies": "First-2", "AI": "First-3"}
	if err := store.SaveSelections(first.ID, want); err != nil {
		t.Fatal(err)
	}
	want["Proxies"] = "caller-mutated"

	reopened, err := NewStore(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Selections(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got["Proxies"] != "First-2" || got["AI"] != "First-3" {
		t.Fatalf("persisted selections = %#v", got)
	}
	got["Proxies"] = "result-mutated"
	again, err := reopened.Selections(first.ID)
	if err != nil || again["Proxies"] != "First-2" {
		t.Fatalf("store returned aliased selections: %#v, %v", again, err)
	}

	checkpoint, err := reopened.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.SaveSelections(first.ID, map[string]string{"Proxies": "New"}); err != nil {
		t.Fatal(err)
	}
	if err := reopened.SaveSelections(second.ID, map[string]string{"Proxies": "Second-2"}); err != nil {
		t.Fatal(err)
	}
	if err := reopened.RestoreCheckpoint(checkpoint); err != nil {
		t.Fatal(err)
	}
	restored, err := reopened.Selections(first.ID)
	if err != nil || restored["Proxies"] != "First-2" || len(restored) != 2 {
		t.Fatalf("checkpoint selections = %#v, %v", restored, err)
	}
	secondSelections, err := reopened.Selections(second.ID)
	if err != nil || len(secondSelections) != 0 {
		t.Fatalf("checkpoint retained new selections = %#v, %v", secondSelections, err)
	}

	if err := reopened.SaveSelections(second.ID, map[string]string{"Proxies": "Second-2"}); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Remove(second.ID); err != nil {
		t.Fatal(err)
	}
	state, err := reopened.loadStateUnlocked()
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := state.Selections[second.ID]; exists {
		t.Fatal("removing a profile retained its selections")
	}
}

func TestRestoreCheckpointRecoversCorruptStateAndSafelyCleansNewEntries(t *testing.T) {
	ctx := context.Background()
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	original, err := store.AddURI(ctx, "Original", "trojan://original@example.com:443#Original")
	if err != nil {
		t.Fatal(err)
	}
	preexistingOrphan := strings.Repeat("a", 24)
	if err := os.Mkdir(store.profileDir(preexistingOrphan), 0o700); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := store.Checkpoint(original.ID)
	if err != nil {
		t.Fatal(err)
	}

	added, err := store.AddURI(ctx, "Added", "trojan://added@example.net:443#Added")
	if err != nil {
		t.Fatal(err)
	}
	newDirectory := strings.Repeat("b", 24)
	if err := os.Mkdir(store.profileDir(newDirectory), 0o700); err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	marker := filepath.Join(external, "keep")
	if err := os.WriteFile(marker, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	newSymlink := strings.Repeat("c", 24)
	if err := os.Symlink(external, store.profileDir(newSymlink)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.statePath(), []byte("not: [valid"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := store.RestoreCheckpoint(checkpoint); err != nil {
		t.Fatal(err)
	}
	profiles, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 1 || profiles[0].ID != original.ID || !profiles[0].Active {
		t.Fatalf("restored profiles = %#v", profiles)
	}
	for name, path := range map[string]string{
		"added profile": added.ID,
		"new directory": newDirectory,
		"new symlink":   newSymlink,
	} {
		if _, err := os.Lstat(store.profileDir(path)); !os.IsNotExist(err) {
			t.Fatalf("%s was not removed: %v", name, err)
		}
	}
	if _, err := os.Stat(store.profileDir(preexistingOrphan)); err != nil {
		t.Fatalf("pre-checkpoint orphan was removed: %v", err)
	}
	if content, err := os.ReadFile(marker); err != nil || string(content) != "outside" {
		t.Fatalf("symlink target was modified: content=%q err=%v", content, err)
	}
}

func TestRestoreCheckpointValidatesCheckpointBeforeCurrentStore(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.AddURI(context.Background(), "Original", "trojan://original@example.com:443#Original")
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := store.Checkpoint(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint.state = []byte("invalid: [checkpoint")
	if err := os.WriteFile(store.statePath(), []byte("invalid: [current"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(store.profilesRoot()); err != nil {
		t.Fatal(err)
	}

	err = store.RestoreCheckpoint(checkpoint)
	if err == nil || err.Error() != "invalid profile checkpoint" {
		t.Fatalf("RestoreCheckpoint() error = %v, want invalid checkpoint", err)
	}
}

func TestRecoveryObjectsRedactSensitiveContent(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.AddURI(context.Background(), "Sensitive", "trojan://checkpoint-secret@example.com:443#one")
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := store.Checkpoint(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := store.PrepareUpdate(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]any{"checkpoint": checkpoint, "prepared": prepared, "snapshot": snapshot} {
		for _, formatted := range []string{fmt.Sprint(value), fmt.Sprintf("%+v", value), fmt.Sprintf("%#v", value)} {
			if strings.Contains(formatted, "checkpoint-secret") {
				t.Fatalf("%s formatting leaks secret: %s", name, formatted)
			}
		}
	}
}
