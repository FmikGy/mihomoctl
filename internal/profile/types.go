package profile

import (
	"errors"
	"net/http"
	"time"

	"mihomoctl/internal/domain"
)

const (
	DefaultUpdateInterval = 24 * time.Hour
	DefaultHTTPTimeout    = 30 * time.Second
	DefaultMaxSourceBytes = int64(10 << 20)
	// InlineSnapshotPrefix marks content read by the unprivileged caller
	// before it crosses the sudo boundary.
	InlineSnapshotPrefix = "mihomoctl:immutable-snapshot:v1\n"
)

var (
	ErrNotFound       = errors.New("profile not found")
	ErrNoActive       = errors.New("no active profile")
	ErrImmutable      = errors.New("profile is an immutable snapshot")
	ErrStaleUpdate    = errors.New("profile changed while update was being prepared")
	ErrSourceTooLarge = errors.New("profile source exceeds size limit")
)

// PreparedUpdate is an opaque, validated profile refresh. It may contain
// subscription credentials and source content, so callers must not log it.
type PreparedUpdate struct {
	store       *Store
	profile     domain.Profile
	originHash  [32]byte
	sourceHash  [32]byte
	configHash  [32]byte
	raw         []byte
	config      []byte
	fetched     fetchResult
	preparedAt  time.Time
	notModified bool
}

func (PreparedUpdate) String() string   { return "[redacted prepared profile update]" }
func (PreparedUpdate) GoString() string { return "profile.PreparedUpdate{[redacted]}" }

// ValidateManagedConfig confirms that a prepared update can accept the
// mihomoctl overlay without exceeding the final config size limit.
func (p PreparedUpdate) ValidateManagedConfig(options OverlayOptions, limit int64) error {
	// Structural validity is owned by CommitUpdate. Keeping that check there
	// preserves its stale/invalid transaction semantics.
	if p.store == nil || p.profile.ID == "" || p.preparedAt.IsZero() {
		return nil
	}
	if p.notModified {
		return nil
	}
	return ValidateManagedConfigSize(p.config, options, limit)
}

// ValidateManagedConfigSize validates the size of the final encoded config,
// including all settings owned by mihomoctl.
func ValidateManagedConfigSize(raw []byte, options OverlayOptions, limit int64) error {
	managed, err := MergeManagedConfig(raw, options)
	if err != nil {
		return err
	}
	if limit > 0 && int64(len(managed)) > limit {
		return ErrSourceTooLarge
	}
	return nil
}

// AddRequest describes a remote URL, local YAML file, or URI subscription.
type AddRequest struct {
	Name           string
	Kind           domain.ProfileKind
	Source         string
	UpdateInterval time.Duration
}

// UpdateResult reports whether an update installed a different source.
type UpdateResult struct {
	Profile     domain.Profile
	Changed     bool
	NotModified bool
}

// Node is a normalized Mihomo proxy entry. Config contains protocol-specific
// fields; name and type are supplied separately to keep those invariants clear.
type Node struct {
	Name   string
	Type   string
	Config map[string]any
}

// OverlayOptions are settings owned by mihomoctl. A nil StoreSelected means
// true, which is the safe default for preserving group choices across restarts.
type OverlayOptions struct {
	ExternalController string
	Secret             string
	StoreSelected      *bool
	Settings           domain.ManagedSettings
}

// StoreOption customizes a Store.
type StoreOption func(*Store)

// WithHTTPClient installs the client used for remote profiles. Callers own it.
func WithHTTPClient(client *http.Client) StoreOption {
	return func(s *Store) {
		if client != nil {
			clone := *client
			s.client = &clone
		}
	}
}

// WithHTTPTimeout changes the default HTTP client's timeout.
func WithHTTPTimeout(timeout time.Duration) StoreOption {
	return func(s *Store) {
		if timeout > 0 {
			s.client.Timeout = timeout
		}
	}
}

// WithMaxSourceBytes changes the source-size limit. It mainly exists for
// constrained environments and tests; the production default is 10 MiB.
func WithMaxSourceBytes(limit int64) StoreOption {
	return func(s *Store) {
		if limit > 0 {
			s.maxSourceBytes = limit
		}
	}
}
