package domain

import "time"

type Mode string

const (
	ModeRule   Mode = "rule"
	ModeGlobal Mode = "global"
	ModeDirect Mode = "direct"
)

type ServiceStatus struct {
	Active  bool   `json:"active"`
	Enabled bool   `json:"enabled"`
	State   string `json:"state"`
	PID     int    `json:"pid,omitempty"`
}

type Traffic struct {
	Up        int64 `json:"up"`
	Down      int64 `json:"down"`
	UpTotal   int64 `json:"up_total"`
	DownTotal int64 `json:"down_total"`
}

type RuntimeStatus struct {
	Service         ServiceStatus    `json:"service"`
	CoreVersion     string           `json:"core_version,omitempty"`
	ActiveProfile   string           `json:"active_profile,omitempty"`
	ConfigAvailable bool             `json:"config_available"`
	Mode            Mode             `json:"mode,omitempty"`
	TUN             bool             `json:"tun"`
	MixedPort       int              `json:"mixed_port,omitempty"`
	AllowLAN        bool             `json:"allow_lan"`
	IPv6            bool             `json:"ipv6"`
	LogLevel        string           `json:"log_level,omitempty"`
	Memory          int64            `json:"memory,omitempty"`
	ConnectionCount int              `json:"connection_count"`
	Traffic         Traffic          `json:"traffic"`
	ExpectedConfig  *EffectiveConfig `json:"expected_config,omitempty"`
	LiveConfig      *EffectiveConfig `json:"live_config,omitempty"`
	ConfigDrift     []string         `json:"config_drift,omitempty"`
}

// EffectiveConfig is the non-sensitive subset of the active Mihomo
// configuration that remains useful while the controller is offline.
type EffectiveConfig struct {
	Mode      Mode   `json:"mode"`
	TUN       bool   `json:"tun"`
	MixedPort int    `json:"mixed_port"`
	AllowLAN  bool   `json:"allow_lan"`
	IPv6      bool   `json:"ipv6"`
	LogLevel  string `json:"log_level"`
}

type DelaySample struct {
	Time  time.Time `json:"time"`
	Delay uint16    `json:"delay"`
}

type Proxy struct {
	Name         string        `json:"name"`
	Type         string        `json:"type"`
	Alive        bool          `json:"alive"`
	UDP          bool          `json:"udp"`
	ProviderName string        `json:"provider_name,omitempty"`
	Delay        uint16        `json:"delay,omitempty"`
	History      []DelaySample `json:"history,omitempty"`
}

type ProxyGroup struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Now     string   `json:"now,omitempty"`
	All     []string `json:"all"`
	Hidden  bool     `json:"hidden"`
	TestURL string   `json:"test_url,omitempty"`
	Proxies []Proxy  `json:"proxies,omitempty"`
}

type Connection struct {
	ID          string    `json:"id"`
	Network     string    `json:"network,omitempty"`
	Type        string    `json:"type,omitempty"`
	Source      string    `json:"source,omitempty"`
	Destination string    `json:"destination,omitempty"`
	Host        string    `json:"host,omitempty"`
	Process     string    `json:"process,omitempty"`
	Rule        string    `json:"rule,omitempty"`
	RulePayload string    `json:"rule_payload,omitempty"`
	Chains      []string  `json:"chains,omitempty"`
	Upload      int64     `json:"upload"`
	Download    int64     `json:"download"`
	Start       time.Time `json:"start,omitempty"`
}

type LogEntry struct {
	Time    string `json:"time,omitempty"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

type ProfileKind string

const (
	ProfileLocal  ProfileKind = "local"
	ProfileRemote ProfileKind = "remote"
	ProfileURI    ProfileKind = "uri"
)

type SubscriptionInfo struct {
	Upload   int64     `json:"upload,omitempty" yaml:"upload,omitempty"`
	Download int64     `json:"download,omitempty" yaml:"download,omitempty"`
	Total    int64     `json:"total,omitempty" yaml:"total,omitempty"`
	Expire   time.Time `json:"expire,omitempty" yaml:"expire,omitempty"`
}

type Profile struct {
	ID             string           `json:"id" yaml:"id"`
	Name           string           `json:"name" yaml:"name"`
	Kind           ProfileKind      `json:"kind" yaml:"kind"`
	Source         string           `json:"source,omitempty" yaml:"source,omitempty"`
	Active         bool             `json:"active" yaml:"-"`
	UpdateInterval time.Duration    `json:"update_interval" yaml:"update_interval"`
	LastUpdated    time.Time        `json:"last_updated,omitempty" yaml:"last_updated,omitempty"`
	ETag           string           `json:"-" yaml:"etag,omitempty"`
	LastModified   string           `json:"-" yaml:"last_modified,omitempty"`
	Subscription   SubscriptionInfo `json:"subscription,omitempty" yaml:"subscription,omitempty"`
}

type ManagedSettings struct {
	Mode      Mode   `json:"mode" yaml:"mode"`
	TUN       bool   `json:"tun" yaml:"tun"`
	MixedPort *int   `json:"mixed_port,omitempty" yaml:"mixed_port,omitempty"`
	AllowLAN  *bool  `json:"allow_lan,omitempty" yaml:"allow_lan,omitempty"`
	IPv6      *bool  `json:"ipv6,omitempty" yaml:"ipv6,omitempty"`
	LogLevel  string `json:"log_level,omitempty" yaml:"log_level,omitempty"`
}

type InitOptions struct {
	Service    string `json:"service,omitempty"`
	ConfigPath string `json:"config_path,omitempty"`
	Controller string `json:"controller,omitempty"`
	Force      bool   `json:"force"`
}

type ProfileUpdateOptions struct {
	Name string `json:"name,omitempty"`
	All  bool   `json:"all"`
	Due  bool   `json:"due"`
}

type DoctorCheck struct {
	ID           string `json:"-"`
	Name         string `json:"name"`
	OK           bool   `json:"ok"`
	Message      string `json:"message"`
	Fixed        bool   `json:"fixed,omitempty"`
	MessageKey   string `json:"-"`
	MessageArgs  []any  `json:"-"`
	MessageError error  `json:"-"`
}

type ScheduleStatus struct {
	Enabled bool   `json:"enabled"`
	State   string `json:"state"`
}
