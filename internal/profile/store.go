package profile

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"

	"mihomoctl/internal/domain"
)

const stateVersion = 1

type persistedState struct {
	Version  int              `yaml:"version"`
	ActiveID string           `yaml:"active_profile,omitempty"`
	Profiles []domain.Profile `yaml:"profiles,omitempty"`
}

// Store owns profile metadata, original sources, and last-known-good configs.
// root is explicit so callers can use /var/lib/mihomoctl or a test directory.
type Store struct {
	root           string
	client         *http.Client
	maxSourceBytes int64
	now            func() time.Time
	mu             sync.Mutex
}

// NewStore opens or initializes a profile store at root.
func NewStore(root string, options ...StoreOption) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("profile store root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve profile store root: %w", err)
	}
	store := &Store{
		root:           absolute,
		client:         &http.Client{Timeout: DefaultHTTPTimeout},
		maxSourceBytes: DefaultMaxSourceBytes,
		now:            time.Now,
	}
	for _, option := range options {
		option(store)
	}
	if err := os.MkdirAll(store.profilesRoot(), 0o700); err != nil {
		return nil, fmt.Errorf("create profile store: %w", err)
	}
	if err := os.Chmod(store.root, 0o700); err != nil {
		return nil, fmt.Errorf("secure profile store root: %w", err)
	}
	if err := os.Chmod(store.profilesRoot(), 0o700); err != nil {
		return nil, fmt.Errorf("secure profiles directory: %w", err)
	}
	state, err := store.loadStateUnlocked()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(store.statePath()); errors.Is(err, os.ErrNotExist) {
		if err := store.saveStateUnlocked(state); err != nil {
			return nil, err
		}
	}
	return store, nil
}

func (s *Store) Root() string { return s.root }

// Add imports the source, validates it, and stores a last-known-good config.
func (s *Store) Add(ctx context.Context, request AddRequest) (domain.Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	request.Name = strings.TrimSpace(request.Name)
	request.Source = strings.TrimSpace(request.Source)
	if request.Name == "" || request.Source == "" {
		return domain.Profile{}, fmt.Errorf("profile name and source are required")
	}
	if request.Kind != domain.ProfileRemote && request.Kind != domain.ProfileLocal && request.Kind != domain.ProfileURI {
		return domain.Profile{}, fmt.Errorf("unsupported profile kind %q", request.Kind)
	}
	if request.UpdateInterval <= 0 {
		request.UpdateInterval = DefaultUpdateInterval
	}
	if request.Kind == domain.ProfileLocal {
		absolute, err := filepath.Abs(request.Source)
		if err != nil {
			return domain.Profile{}, fmt.Errorf("resolve local source: %w", err)
		}
		request.Source = absolute
	}

	profile := domain.Profile{
		Name: request.Name, Kind: request.Kind, UpdateInterval: request.UpdateInterval,
	}
	var raw []byte
	switch request.Kind {
	case domain.ProfileRemote:
		fetched, err := s.fetchRemote(ctx, request.Source, "", "")
		if err != nil {
			return domain.Profile{}, err
		}
		if fetched.notModified {
			return domain.Profile{}, fmt.Errorf("new remote profile returned HTTP 304")
		}
		raw = fetched.body
		profile.ETag = fetched.etag
		profile.LastModified = fetched.lastModified
		profile.Subscription = fetched.subscription
		profile.Source = RedactURL(request.Source)
	case domain.ProfileLocal:
		file, err := os.Open(request.Source)
		if err != nil {
			return domain.Profile{}, fmt.Errorf("open local profile: %w", err)
		}
		raw, err = readLimited(file, s.maxSourceBytes)
		closeErr := file.Close()
		if err != nil {
			return domain.Profile{}, fmt.Errorf("read local profile: %w", err)
		}
		if closeErr != nil {
			return domain.Profile{}, fmt.Errorf("close local profile: %w", closeErr)
		}
		profile.Source = request.Source
	case domain.ProfileURI:
		if int64(len(request.Source)) > s.maxSourceBytes {
			return domain.Profile{}, ErrSourceTooLarge
		}
		raw = []byte(request.Source)
		profile.Source = "[redacted URI subscription]"
	}
	config, _, err := NormalizeSource(raw)
	if err != nil {
		return domain.Profile{}, err
	}
	return s.addPreparedUnlocked(profile, []byte(request.Source), raw, config)
}

// AddSnapshot imports content as an immutable local recovery point. Snapshots
// have no live origin and are excluded from scheduled and bulk refreshes.
func (s *Store) AddSnapshot(name string, raw []byte) (domain.Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name = strings.TrimSpace(name)
	if name == "" {
		return domain.Profile{}, fmt.Errorf("profile name is required")
	}
	if len(raw) == 0 {
		return domain.Profile{}, fmt.Errorf("profile snapshot is empty")
	}
	if int64(len(raw)) > s.maxSourceBytes {
		return domain.Profile{}, ErrSourceTooLarge
	}
	config, _, err := NormalizeSource(raw)
	if err != nil {
		return domain.Profile{}, err
	}
	profile := domain.Profile{
		Name:           name,
		Kind:           domain.ProfileLocal,
		Source:         "[immutable snapshot]",
		UpdateInterval: 0,
	}
	return s.addPreparedUnlocked(profile, nil, raw, config)
}

func (s *Store) addPreparedUnlocked(profile domain.Profile, origin, raw, config []byte) (domain.Profile, error) {
	var err error
	profile.ID, err = newID()
	if err != nil {
		return domain.Profile{}, err
	}
	profile.LastUpdated = s.now().UTC()

	state, err := s.loadStateUnlocked()
	if err != nil {
		return domain.Profile{}, err
	}
	directory := s.profileDir(profile.ID)
	if err := os.Mkdir(directory, 0o700); err != nil {
		return domain.Profile{}, fmt.Errorf("create profile directory: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(directory)
		}
	}()
	if err := atomicWrite(filepath.Join(directory, "origin"), origin, 0o600); err != nil {
		return domain.Profile{}, err
	}
	if err := atomicWrite(filepath.Join(directory, "source"), raw, 0o600); err != nil {
		return domain.Profile{}, err
	}
	if err := atomicWrite(filepath.Join(directory, "config.yaml"), config, 0o600); err != nil {
		return domain.Profile{}, err
	}
	state.Profiles = append(state.Profiles, profile)
	if state.ActiveID == "" {
		state.ActiveID = profile.ID
	}
	if err := s.saveStateUnlocked(state); err != nil {
		return domain.Profile{}, err
	}
	cleanup = false
	profile.Active = state.ActiveID == profile.ID
	return profile, nil
}

func (s *Store) AddRemote(ctx context.Context, name, remoteURL string, interval time.Duration) (domain.Profile, error) {
	return s.Add(ctx, AddRequest{Name: name, Kind: domain.ProfileRemote, Source: remoteURL, UpdateInterval: interval})
}

func (s *Store) AddLocal(ctx context.Context, name, path string) (domain.Profile, error) {
	return s.Add(ctx, AddRequest{Name: name, Kind: domain.ProfileLocal, Source: path})
}

func (s *Store) AddURI(ctx context.Context, name, input string) (domain.Profile, error) {
	return s.Add(ctx, AddRequest{Name: name, Kind: domain.ProfileURI, Source: input})
}

// List returns profiles in insertion order with the active marker derived from state.
func (s *Store) List() ([]domain.Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadStateUnlocked()
	if err != nil {
		return nil, err
	}
	profiles := append([]domain.Profile(nil), state.Profiles...)
	for index := range profiles {
		profiles[index].Active = profiles[index].ID == state.ActiveID
	}
	return profiles, nil
}

func (s *Store) Get(id string) (domain.Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadStateUnlocked()
	if err != nil {
		return domain.Profile{}, err
	}
	profile, _, err := findProfile(state, id)
	if err != nil {
		return domain.Profile{}, err
	}
	profile.Active = profile.ID == state.ActiveID
	return profile, nil
}

func (s *Store) Active() (domain.Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadStateUnlocked()
	if err != nil {
		return domain.Profile{}, err
	}
	if state.ActiveID == "" {
		return domain.Profile{}, ErrNoActive
	}
	profile, _, err := findProfile(state, state.ActiveID)
	if err != nil {
		return domain.Profile{}, fmt.Errorf("active profile is invalid: %w", err)
	}
	profile.Active = true
	return profile, nil
}

// Use marks id active. The caller can then install ActiveConfig transactionally.
func (s *Store) Use(id string) (domain.Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadStateUnlocked()
	if err != nil {
		return domain.Profile{}, err
	}
	profile, _, err := findProfile(state, id)
	if err != nil {
		return domain.Profile{}, err
	}
	state.ActiveID = id
	if err := s.saveStateUnlocked(state); err != nil {
		return domain.Profile{}, err
	}
	profile.Active = true
	return profile, nil
}

// Remove deletes profile metadata and its private source files.
func (s *Store) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadStateUnlocked()
	if err != nil {
		return err
	}
	_, index, err := findProfile(state, id)
	if err != nil {
		return err
	}
	state.Profiles = append(state.Profiles[:index], state.Profiles[index+1:]...)
	if state.ActiveID == id {
		state.ActiveID = ""
		if len(state.Profiles) > 0 {
			state.ActiveID = state.Profiles[0].ID
		}
	}
	if err := s.saveStateUnlocked(state); err != nil {
		return err
	}
	if err := os.RemoveAll(s.profileDir(id)); err != nil {
		return fmt.Errorf("remove profile files: %w", err)
	}
	return nil
}

// Update refreshes a remote/local source or re-renders a stored URI profile.
func (s *Store) Update(ctx context.Context, id string) (UpdateResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadStateUnlocked()
	if err != nil {
		return UpdateResult{}, err
	}
	profile, index, err := findProfile(state, id)
	if err != nil {
		return UpdateResult{}, err
	}
	if profile.UpdateInterval <= 0 {
		return UpdateResult{}, fmt.Errorf("%w: %s", ErrImmutable, profile.Name)
	}
	origin, err := os.ReadFile(filepath.Join(s.profileDir(id), "origin"))
	if err != nil {
		return UpdateResult{}, fmt.Errorf("read profile origin: %w", err)
	}

	var raw []byte
	var fetched fetchResult
	switch profile.Kind {
	case domain.ProfileRemote:
		fetched, err = s.fetchRemote(ctx, string(origin), profile.ETag, profile.LastModified)
		if err != nil {
			return UpdateResult{}, err
		}
		if fetched.notModified {
			profile.LastUpdated = s.now().UTC()
			if fetched.etag != "" {
				profile.ETag = fetched.etag
			}
			if fetched.lastModified != "" {
				profile.LastModified = fetched.lastModified
			}
			if fetched.subscription != (domain.SubscriptionInfo{}) {
				profile.Subscription = fetched.subscription
			}
			state.Profiles[index] = profile
			if err := s.saveStateUnlocked(state); err != nil {
				return UpdateResult{}, err
			}
			profile.Active = state.ActiveID == id
			return UpdateResult{Profile: profile, NotModified: true}, nil
		}
		raw = fetched.body
	case domain.ProfileLocal:
		file, openErr := os.Open(string(origin))
		if openErr != nil {
			return UpdateResult{}, fmt.Errorf("open local profile: %w", openErr)
		}
		raw, err = readLimited(file, s.maxSourceBytes)
		closeErr := file.Close()
		if err != nil {
			return UpdateResult{}, fmt.Errorf("read local profile: %w", err)
		}
		if closeErr != nil {
			return UpdateResult{}, fmt.Errorf("close local profile: %w", closeErr)
		}
	case domain.ProfileURI:
		raw = append([]byte(nil), origin...)
	default:
		return UpdateResult{}, fmt.Errorf("unsupported profile kind %q", profile.Kind)
	}
	config, _, err := NormalizeSource(raw)
	if err != nil {
		return UpdateResult{}, err
	}
	oldRaw, err := os.ReadFile(filepath.Join(s.profileDir(id), "source"))
	if err != nil {
		return UpdateResult{}, fmt.Errorf("read prior profile source: %w", err)
	}
	oldConfig, err := os.ReadFile(filepath.Join(s.profileDir(id), "config.yaml"))
	if err != nil {
		return UpdateResult{}, fmt.Errorf("read prior profile config: %w", err)
	}
	changed := !bytes.Equal(oldRaw, raw) || !bytes.Equal(oldConfig, config)
	if changed {
		if err := atomicWrite(filepath.Join(s.profileDir(id), "source"), raw, 0o600); err != nil {
			return UpdateResult{}, err
		}
		if err := atomicWrite(filepath.Join(s.profileDir(id), "config.yaml"), config, 0o600); err != nil {
			_ = atomicWrite(filepath.Join(s.profileDir(id), "source"), oldRaw, 0o600)
			return UpdateResult{}, err
		}
	}
	oldProfile := state.Profiles[index]
	profile.LastUpdated = s.now().UTC()
	if profile.Kind == domain.ProfileRemote {
		profile.ETag = fetched.etag
		profile.LastModified = fetched.lastModified
		profile.Subscription = fetched.subscription
	}
	state.Profiles[index] = profile
	if err := s.saveStateUnlocked(state); err != nil {
		if changed {
			_ = atomicWrite(filepath.Join(s.profileDir(id), "source"), oldRaw, 0o600)
			_ = atomicWrite(filepath.Join(s.profileDir(id), "config.yaml"), oldConfig, 0o600)
		}
		state.Profiles[index] = oldProfile
		return UpdateResult{}, err
	}
	profile.Active = state.ActiveID == id
	return UpdateResult{Profile: profile, Changed: changed}, nil
}

// Updatable returns profiles backed by a refreshable remote, local, or URI
// origin. Immutable recovery snapshots are deliberately omitted.
func (s *Store) Updatable() ([]domain.Profile, error) {
	profiles, err := s.List()
	if err != nil {
		return nil, err
	}
	updatable := profiles[:0]
	for _, profile := range profiles {
		if profile.UpdateInterval > 0 {
			updatable = append(updatable, profile)
		}
	}
	return updatable, nil
}

// Due returns profiles whose update interval has elapsed, oldest first.
func (s *Store) Due(at time.Time) ([]domain.Profile, error) {
	profiles, err := s.List()
	if err != nil {
		return nil, err
	}
	due := profiles[:0]
	for _, profile := range profiles {
		if profile.UpdateInterval > 0 && !profile.LastUpdated.Add(profile.UpdateInterval).After(at) {
			due = append(due, profile)
		}
	}
	sort.SliceStable(due, func(i, j int) bool { return due[i].LastUpdated.Before(due[j].LastUpdated) })
	return due, nil
}

func (s *Store) Config(id string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadStateUnlocked()
	if err != nil {
		return nil, err
	}
	if _, _, err := findProfile(state, id); err != nil {
		return nil, err
	}
	content, err := os.ReadFile(filepath.Join(s.profileDir(id), "config.yaml"))
	if err != nil {
		return nil, fmt.Errorf("read profile config: %w", err)
	}
	return content, nil
}

func (s *Store) ActiveConfig() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadStateUnlocked()
	if err != nil {
		return nil, err
	}
	if state.ActiveID == "" {
		return nil, ErrNoActive
	}
	if _, _, err := findProfile(state, state.ActiveID); err != nil {
		return nil, fmt.Errorf("active profile is invalid: %w", err)
	}
	content, err := os.ReadFile(filepath.Join(s.profileDir(state.ActiveID), "config.yaml"))
	if err != nil {
		return nil, fmt.Errorf("read active profile config: %w", err)
	}
	return content, nil
}

// Raw returns the imported source body, never the remote URL or URI origin.
func (s *Store) Raw(id string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadStateUnlocked()
	if err != nil {
		return nil, err
	}
	if _, _, err := findProfile(state, id); err != nil {
		return nil, err
	}
	content, err := os.ReadFile(filepath.Join(s.profileDir(id), "source"))
	if err != nil {
		return nil, fmt.Errorf("read profile source: %w", err)
	}
	return content, nil
}

func (s *Store) statePath() string           { return filepath.Join(s.root, "state.yaml") }
func (s *Store) profilesRoot() string        { return filepath.Join(s.root, "profiles") }
func (s *Store) profileDir(id string) string { return filepath.Join(s.profilesRoot(), id) }

func (s *Store) loadStateUnlocked() (persistedState, error) {
	content, err := os.ReadFile(s.statePath())
	if errors.Is(err, os.ErrNotExist) {
		return persistedState{Version: stateVersion}, nil
	}
	if err != nil {
		return persistedState{}, fmt.Errorf("read profile state: %w", err)
	}
	var state persistedState
	if err := yaml.Unmarshal(content, &state); err != nil {
		return persistedState{}, fmt.Errorf("decode profile state: %w", err)
	}
	if state.Version != stateVersion {
		return persistedState{}, fmt.Errorf("unsupported profile state version %d", state.Version)
	}
	seen := make(map[string]bool, len(state.Profiles))
	for _, profile := range state.Profiles {
		if !validID(profile.ID) || seen[profile.ID] {
			return persistedState{}, fmt.Errorf("invalid profile id in state")
		}
		seen[profile.ID] = true
	}
	if state.ActiveID != "" && !seen[state.ActiveID] {
		return persistedState{}, fmt.Errorf("active profile does not exist")
	}
	return state, nil
}

func (s *Store) saveStateUnlocked(state persistedState) error {
	state.Version = stateVersion
	content, err := yaml.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode profile state: %w", err)
	}
	return atomicWrite(s.statePath(), content, 0o600)
}

func findProfile(state persistedState, id string) (domain.Profile, int, error) {
	if !validID(id) {
		return domain.Profile{}, -1, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	for index, profile := range state.Profiles {
		if profile.ID == id {
			return profile, index, nil
		}
	}
	return domain.Profile{}, -1, fmt.Errorf("%w: %s", ErrNotFound, id)
}

func validID(id string) bool {
	if len(id) != 24 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func atomicWrite(path string, content []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	ok := false
	defer func() {
		_ = temporary.Close()
		if !ok {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(mode); err != nil {
		return fmt.Errorf("set temporary file mode: %w", err)
	}
	if _, err := temporary.Write(content); err != nil {
		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace %s: %w", filepath.Base(path), err)
	}
	ok = true
	if directoryHandle, err := os.Open(directory); err == nil {
		_ = directoryHandle.Sync()
		_ = directoryHandle.Close()
	}
	return nil
}
