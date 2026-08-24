package profile

import (
	"fmt"
	"os"
	"path/filepath"
)

// Snapshot is an opaque recovery point used when a refreshed active profile
// passes source parsing but fails Mihomo's full configuration validation.
type Snapshot struct {
	id     string
	state  []byte
	source []byte
	config []byte
}

func (s *Store) Snapshot(id string) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadStateUnlocked()
	if err != nil {
		return Snapshot{}, err
	}
	if _, _, err := findProfile(state, id); err != nil {
		return Snapshot{}, err
	}
	stateBytes, err := os.ReadFile(s.statePath())
	if err != nil {
		return Snapshot{}, fmt.Errorf("snapshot profile state: %w", err)
	}
	source, err := os.ReadFile(filepath.Join(s.profileDir(id), "source"))
	if err != nil {
		return Snapshot{}, fmt.Errorf("snapshot profile source: %w", err)
	}
	config, err := os.ReadFile(filepath.Join(s.profileDir(id), "config.yaml"))
	if err != nil {
		return Snapshot{}, fmt.Errorf("snapshot profile config: %w", err)
	}
	return Snapshot{id: id, state: stateBytes, source: source, config: config}, nil
}

func (s *Store) Restore(snapshot Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if snapshot.id == "" || len(snapshot.state) == 0 || len(snapshot.config) == 0 {
		return fmt.Errorf("invalid profile snapshot")
	}
	state, err := s.loadStateUnlocked()
	if err != nil {
		return err
	}
	if _, _, err := findProfile(state, snapshot.id); err != nil {
		return err
	}
	if err := atomicWrite(filepath.Join(s.profileDir(snapshot.id), "source"), snapshot.source, 0o600); err != nil {
		return err
	}
	if err := atomicWrite(filepath.Join(s.profileDir(snapshot.id), "config.yaml"), snapshot.config, 0o600); err != nil {
		return err
	}
	if err := atomicWrite(s.statePath(), snapshot.state, 0o600); err != nil {
		return err
	}
	return nil
}
