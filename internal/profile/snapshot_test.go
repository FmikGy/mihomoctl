package profile

import (
	"context"
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
