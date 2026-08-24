package mihomo

import (
	"context"
	"net/http"
	"net/url"

	"mihomoctl/internal/domain"
)

// VersionInfo is returned by Mihomo's /version endpoint.
type VersionInfo struct {
	Meta    bool   `json:"meta"`
	Version string `json:"version"`
}

// TUNConfig contains the TUN fields needed by mihomoctl.
type TUNConfig struct {
	Enable              bool     `json:"enable"`
	Device              string   `json:"device,omitempty"`
	Stack               string   `json:"stack,omitempty"`
	DNSHijack           []string `json:"dns-hijack,omitempty"`
	AutoRoute           bool     `json:"auto-route"`
	AutoRedirect        bool     `json:"auto-redirect"`
	AutoDetectInterface bool     `json:"auto-detect-interface"`
}

// Config is the basic runtime configuration returned by /configs.
type Config struct {
	Port               int         `json:"port"`
	SocksPort          int         `json:"socks-port"`
	RedirPort          int         `json:"redir-port"`
	TProxyPort         int         `json:"tproxy-port"`
	MixedPort          int         `json:"mixed-port"`
	AllowLAN           bool        `json:"allow-lan"`
	BindAddress        string      `json:"bind-address,omitempty"`
	Mode               domain.Mode `json:"mode"`
	LogLevel           string      `json:"log-level"`
	IPv6               bool        `json:"ipv6"`
	ExternalController string      `json:"external-controller,omitempty"`
	TUN                TUNConfig   `json:"tun"`
}

// TUNPatch is the mutable subset of TUN configuration.
type TUNPatch struct {
	Enable              *bool    `json:"enable,omitempty"`
	Device              *string  `json:"device,omitempty"`
	Stack               *string  `json:"stack,omitempty"`
	DNSHijack           []string `json:"dns-hijack,omitempty"`
	AutoRoute           *bool    `json:"auto-route,omitempty"`
	AutoRedirect        *bool    `json:"auto-redirect,omitempty"`
	AutoDetectInterface *bool    `json:"auto-detect-interface,omitempty"`
}

// ConfigPatch is the supported typed payload for PATCH /configs. PatchConfigs
// also accepts maps when a caller needs to use a newer Mihomo field.
type ConfigPatch struct {
	MixedPort *int         `json:"mixed-port,omitempty"`
	AllowLAN  *bool        `json:"allow-lan,omitempty"`
	IPv6      *bool        `json:"ipv6,omitempty"`
	LogLevel  *string      `json:"log-level,omitempty"`
	Mode      *domain.Mode `json:"mode,omitempty"`
	TUN       *TUNPatch    `json:"tun,omitempty"`
}

func (c *Client) Version(ctx context.Context) (VersionInfo, error) {
	return decodeOne[VersionInfo](ctx, c, http.MethodGet, []string{"version"}, url.Values{}, nil,
		func(value VersionInfo) (VersionInfo, error) { return value, nil })
}

func (c *Client) Configs(ctx context.Context) (Config, error) {
	return decodeOne[Config](ctx, c, http.MethodGet, []string{"configs"}, url.Values{}, nil,
		func(value Config) (Config, error) { return value, nil })
}

// PatchConfigs applies runtime settings. patch is normally ConfigPatch, but may
// be any JSON-serializable value to preserve forward compatibility with Mihomo.
func (c *Client) PatchConfigs(ctx context.Context, patch any) error {
	return doNoContent(ctx, c, http.MethodPatch, []string{"configs"}, url.Values{}, patch)
}

func (c *Client) FlushFakeIPCache(ctx context.Context) error {
	return doNoContent(ctx, c, http.MethodPost, []string{"cache", "fakeip", "flush"}, url.Values{}, nil)
}

func (c *Client) FlushDNSCache(ctx context.Context) error {
	return doNoContent(ctx, c, http.MethodPost, []string{"cache", "dns", "flush"}, url.Values{}, nil)
}
