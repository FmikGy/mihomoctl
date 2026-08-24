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
)

var (
	ErrNotFound       = errors.New("profile not found")
	ErrNoActive       = errors.New("no active profile")
	ErrImmutable      = errors.New("profile is an immutable snapshot")
	ErrSourceTooLarge = errors.New("profile source exceeds size limit")
)

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
			s.client = client
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
