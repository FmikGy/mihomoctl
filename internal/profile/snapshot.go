package profile

import (
	"fmt"
	"os"
	"path/filepath"
)

type checkpointFiles struct {
	origin []byte
	source []byte
	config []byte
}

// Checkpoint is an opaque recovery point for store metadata and selected
// profile files. It can contain credentials and must not be logged or encoded.
// Pass IDs for profiles that an operation may update or remove; newly added
// profile directories are detected from the saved directory set and removed
// during restore.
type Checkpoint struct {
	store          *Store
	state          []byte
	profileIDs     map[string]struct{}
	profileEntries map[string]struct{}
	files          map[string]checkpointFiles
}

func (Checkpoint) String() string   { return "[redacted profile checkpoint]" }
func (Checkpoint) GoString() string { return "profile.Checkpoint{[redacted]}" }

// Checkpoint captures metadata plus the private files for selected profiles.
func (s *Store) Checkpoint(ids ...string) (Checkpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stateBytes, err := os.ReadFile(s.statePath())
	if err != nil {
		return Checkpoint{}, fmt.Errorf("checkpoint profile state: %w", err)
	}
	state, err := decodeState(stateBytes)
	if err != nil {
		return Checkpoint{}, err
	}
	entries, err := os.ReadDir(s.profilesRoot())
	if err != nil {
		return Checkpoint{}, fmt.Errorf("checkpoint profile directories: %w", err)
	}
	checkpoint := Checkpoint{
		store:          s,
		state:          stateBytes,
		profileIDs:     make(map[string]struct{}, len(state.Profiles)),
		profileEntries: make(map[string]struct{}, len(entries)),
		files:          make(map[string]checkpointFiles, len(ids)),
	}
	for _, profile := range state.Profiles {
		checkpoint.profileIDs[profile.ID] = struct{}{}
	}
	for _, entry := range entries {
		checkpoint.profileEntries[entry.Name()] = struct{}{}
	}
	for _, id := range ids {
		if _, exists := checkpoint.files[id]; exists {
			continue
		}
		if _, _, err := findProfile(state, id); err != nil {
			return Checkpoint{}, err
		}
		files, err := s.readCheckpointFilesUnlocked(id)
		if err != nil {
			return Checkpoint{}, err
		}
		checkpoint.files[id] = files
	}
	if err := s.validateCheckpoint(checkpoint); err != nil {
		return Checkpoint{}, err
	}
	return checkpoint, nil
}

func (s *Store) readCheckpointFilesUnlocked(id string) (checkpointFiles, error) {
	directory := s.profileDir(id)
	origin, err := os.ReadFile(filepath.Join(directory, "origin"))
	if err != nil {
		return checkpointFiles{}, fmt.Errorf("checkpoint profile origin: %w", err)
	}
	source, err := os.ReadFile(filepath.Join(directory, "source"))
	if err != nil {
		return checkpointFiles{}, fmt.Errorf("checkpoint profile source: %w", err)
	}
	config, err := os.ReadFile(filepath.Join(directory, "config.yaml"))
	if err != nil {
		return checkpointFiles{}, fmt.Errorf("checkpoint profile config: %w", err)
	}
	return checkpointFiles{origin: origin, source: source, config: config}, nil
}

// RestoreCheckpoint restores selected private files and metadata, then removes
// profile directories introduced after the checkpoint. Metadata is committed
// before cleanup so a cleanup error cannot leave state pointing at deleted data.
func (s *Store) RestoreCheckpoint(checkpoint Checkpoint) error {
	if err := s.validateCheckpoint(checkpoint); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	addedEntries, err := s.profileEntriesAddedAfterCheckpointUnlocked(checkpoint.profileEntries)
	if err != nil {
		return err
	}
	for id, files := range checkpoint.files {
		directory := s.profileDir(id)
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return fmt.Errorf("restore profile directory: %w", err)
		}
		if err := os.Chmod(directory, 0o700); err != nil {
			return fmt.Errorf("secure restored profile directory: %w", err)
		}
		if err := atomicWrite(filepath.Join(directory, "origin"), files.origin, 0o600); err != nil {
			return fmt.Errorf("restore profile origin: %w", err)
		}
		if err := atomicWrite(filepath.Join(directory, "source"), files.source, 0o600); err != nil {
			return fmt.Errorf("restore profile source: %w", err)
		}
		if err := atomicWrite(filepath.Join(directory, "config.yaml"), files.config, 0o600); err != nil {
			return fmt.Errorf("restore profile config: %w", err)
		}
	}
	if err := atomicWrite(s.statePath(), checkpoint.state, 0o600); err != nil {
		return fmt.Errorf("restore profile state: %w", err)
	}
	for _, name := range addedEntries {
		if err := os.RemoveAll(s.profileDir(name)); err != nil {
			return fmt.Errorf("remove profile added after checkpoint: %w", err)
		}
	}
	return nil
}

func (s *Store) validateCheckpoint(checkpoint Checkpoint) error {
	if checkpoint.store != s || len(checkpoint.state) == 0 || checkpoint.profileIDs == nil ||
		checkpoint.profileEntries == nil || checkpoint.files == nil {
		return fmt.Errorf("invalid profile checkpoint")
	}
	restored, err := decodeState(checkpoint.state)
	if err != nil || len(checkpoint.profileIDs) != len(restored.Profiles) {
		return fmt.Errorf("invalid profile checkpoint")
	}
	for _, profile := range restored.Profiles {
		if _, ok := checkpoint.profileIDs[profile.ID]; !ok {
			return fmt.Errorf("invalid profile checkpoint")
		}
		if _, ok := checkpoint.profileEntries[profile.ID]; !ok {
			return fmt.Errorf("invalid profile checkpoint")
		}
	}
	for id := range checkpoint.profileIDs {
		if !validID(id) {
			return fmt.Errorf("invalid profile checkpoint")
		}
	}
	for id := range checkpoint.files {
		if _, ok := checkpoint.profileIDs[id]; !ok {
			return fmt.Errorf("invalid profile checkpoint")
		}
	}
	return nil
}

func (s *Store) profileEntriesAddedAfterCheckpointUnlocked(existing map[string]struct{}) ([]string, error) {
	entries, err := os.ReadDir(s.profilesRoot())
	if err != nil {
		return nil, fmt.Errorf("list profile directories for restore: %w", err)
	}
	added := make([]string, 0)
	for _, entry := range entries {
		name := entry.Name()
		if _, existed := existing[name]; existed || !validID(name) {
			continue
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			added = append(added, name)
		}
	}
	return added, nil
}

// Snapshot is retained for compatibility with active-profile update rollback.
type Snapshot struct {
	checkpoint Checkpoint
}

func (Snapshot) String() string   { return "[redacted profile snapshot]" }
func (Snapshot) GoString() string { return "profile.Snapshot{[redacted]}" }

func (s *Store) Snapshot(id string) (Snapshot, error) {
	checkpoint, err := s.Checkpoint(id)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{checkpoint: checkpoint}, nil
}

func (s *Store) Restore(snapshot Snapshot) error {
	return s.RestoreCheckpoint(snapshot.checkpoint)
}
