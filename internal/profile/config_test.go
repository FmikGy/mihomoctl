package profile

import (
	"encoding/base64"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"mihomoctl/internal/domain"
)

func TestNormalizeSourcePreservesYAML(t *testing.T) {
	raw := []byte("custom-key: keep\nproxies: []\n")
	got, converted, err := NormalizeSource(raw)
	if err != nil {
		t.Fatal(err)
	}
	if converted || string(got) != string(raw) {
		t.Fatalf("NormalizeSource() = %q, %v", got, converted)
	}
}

func TestNormalizeSourceRejectsDuplicateKeysRecursively(t *testing.T) {
	for name, raw := range map[string]string{
		"top level": "mode: rule\nmode: global\n",
		"nested":    "dns:\n  enable: true\n  enable: false\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := NormalizeSource([]byte(raw))
			if err == nil || !strings.Contains(err.Error(), "duplicate YAML key") {
				t.Fatalf("duplicate-key error = %v", err)
			}
		})
	}
}

func TestNormalizeUntrustedSourceRemovesManagementEndpoints(t *testing.T) {
	raw := []byte(`external-controller: 0.0.0.0:9090
external-controller-unix: /tmp/mihomo.sock
external-controller-cors:
  allow-origins: ['*']
external-controller-routing-mark: 6666
external-doh-server: /dns-query
external-ui: ./ui
external-ui-name: unsafe
external-ui-url: https://example.test/ui.zip
secret: provider-secret
tls:
  certificate: provider.crt
  private-key: provider.key
proxies: []
`)
	got, converted, err := NormalizeUntrustedSource(raw)
	if err != nil {
		t.Fatal(err)
	}
	if converted {
		t.Fatal("YAML source was reported as URI conversion")
	}
	var config map[string]any
	if err := yaml.Unmarshal(got, &config); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"external-controller", "external-controller-unix", "external-controller-cors",
		"external-controller-routing-mark", "external-doh-server", "external-ui",
		"external-ui-name", "external-ui-url", "secret", "tls",
	} {
		if _, exists := config[key]; exists {
			t.Errorf("sanitized config retained %q: %s", key, got)
		}
	}
}

func TestNormalizeUntrustedSourceRejectsAdditionalListeners(t *testing.T) {
	for _, key := range []string{"listeners", "tunnels", "ss-config", "vmess-config", "tuic-server"} {
		t.Run(key, func(t *testing.T) {
			raw := []byte(key + ": 7890\nproxies: []\n")
			_, _, err := NormalizeUntrustedSource(raw)
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("forbidden listener error = %v", err)
			}
		})
	}
}

func TestNormalizeUntrustedSourceRemovesInboundAccessSettings(t *testing.T) {
	raw := []byte(`mixed-port: 7889
port: 7890
socks-port: 7891
redir-port: 7892
tproxy-port: 7893
allow-lan: true
bind-address: 0.0.0.0
authentication:
  - attacker:secret
skip-auth-prefixes:
  - 0.0.0.0/0
lan-allowed-ips:
  - 0.0.0.0/0
lan-disallowed-ips:
  - 127.0.0.1/32
proxies: []
`)
	got, converted, err := NormalizeUntrustedSource(raw)
	if err != nil {
		t.Fatal(err)
	}
	if converted {
		t.Fatal("YAML source was reported as URI conversion")
	}
	var config map[string]any
	if err := yaml.Unmarshal(got, &config); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"mixed-port", "port", "socks-port", "redir-port", "tproxy-port",
		"allow-lan", "bind-address", "authentication", "skip-auth-prefixes",
		"lan-allowed-ips", "lan-disallowed-ips",
	} {
		if _, exists := config[key]; exists {
			t.Errorf("sanitized config retained %q: %s", key, got)
		}
	}
}

func TestNormalizeUntrustedSourceRejectsTopLevelMergeAndAliasKeys(t *testing.T) {
	tests := map[string]string{
		"merge":     "defaults: &defaults\n  listeners:\n    - name: hidden\n      type: socks\n      port: 1080\n<<: *defaults\nproxies: []\n",
		"alias key": "field: &field listeners\n*field:\n  - name: hidden\n    type: socks\n    port: 1080\nproxies: []\n",
	}
	for name, source := range tests {
		t.Run(name, func(t *testing.T) {
			_, _, err := NormalizeUntrustedSource([]byte(source))
			if err == nil || !strings.Contains(err.Error(), "top-level YAML key") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestNormalizeSourceURIForms(t *testing.T) {
	direct := "trojan://first@example.com:443#First"
	multiple := direct + "\n" + "vless://00000000-0000-0000-0000-000000000001@example.net:8443#Second"
	encoded := base64.RawStdEncoding.EncodeToString([]byte(multiple))
	vmessJSON := base64.RawStdEncoding.EncodeToString([]byte(`{"v":"2","ps":"First","add":"vm.example","port":"443","id":"00000000-0000-0000-0000-000000000001","aid":"0","net":"tcp"}`))
	for name, source := range map[string]string{
		"single URI":                   direct,
		"multiple URI":                 multiple,
		"base64 list":                  encoded,
		"legacy VMess without slashes": "vmess:" + vmessJSON,
	} {
		t.Run(name, func(t *testing.T) {
			got, converted, err := NormalizeSource([]byte(source))
			if err != nil {
				t.Fatal(err)
			}
			if !converted || !strings.Contains(string(got), "proxy-groups:") || !strings.Contains(string(got), "First") {
				t.Fatalf("NormalizeSource() converted = %v, config = %q", converted, got)
			}
		})
	}
}

func TestBase64SubscriptionFastClassifier(t *testing.T) {
	encoded := base64.RawURLEncoding.EncodeToString([]byte("trojan://secret@example.com:443#Node"))
	if !looksLikeBase64Subscription([]byte(" \n" + encoded + "\n")) {
		t.Fatal("base64 URI subscription was not recognized")
	}
	for _, source := range []string{"proxies: []", "# comment", "[yaml, sequence]", "abcde"} {
		if looksLikeBase64Subscription([]byte(source)) {
			t.Fatalf("%q was incorrectly classified as base64", source)
		}
	}
}

func TestNormalizeSourceClassifiesYAMLBeforeBase64Fallback(t *testing.T) {
	raw := []byte("# provider config\ncustom-key: keep\nproxies: []\n")
	if looksLikeDirectURIList(raw) {
		t.Fatal("YAML was classified as a direct URI list")
	}
	got, converted, err := NormalizeSource(raw)
	if err != nil {
		t.Fatal(err)
	}
	if converted || string(got) != string(raw) {
		t.Fatalf("NormalizeSource() = %q, %v", got, converted)
	}

	// A URI-looking YAML key must still fall back to YAML when URI parsing
	// fails instead of being rejected by the fast classifier.
	raw = []byte("ss://provider: value\nproxies: []\n")
	got, converted, err = NormalizeSource(raw)
	if err != nil {
		t.Fatal(err)
	}
	if converted || string(got) != string(raw) {
		t.Fatalf("URI-looking YAML = %q, %v", got, converted)
	}
}

func TestNormalizeSourceKeepsUsefulInvalidSourceErrors(t *testing.T) {
	_, _, err := NormalizeSource([]byte("[unterminated"))
	if err == nil || !strings.Contains(err.Error(), "neither valid Mihomo YAML nor a URI subscription") {
		t.Fatalf("invalid source error = %v", err)
	}
	_, _, err = NormalizeSource([]byte("plain scalar"))
	if err == nil || !strings.Contains(err.Error(), "root must be a mapping") {
		t.Fatalf("scalar source error = %v", err)
	}
}

func BenchmarkNormalizeSourceLargeYAML(b *testing.B) {
	for _, size := range []int{1 << 20, 5 << 20} {
		raw := []byte("proxies: []\npayload: \"" + strings.Repeat("a", size) + "\"\n")
		b.Run(fmt.Sprintf("%dMiB", size>>20), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(raw)))
			for b.Loop() {
				if _, converted, err := NormalizeSource(raw); err != nil || converted {
					b.Fatalf("NormalizeSource() = converted %v, error %v", converted, err)
				}
			}
		})
	}
}

func BenchmarkNormalizeSourceBase64URI(b *testing.B) {
	line := "trojan://benchmark-secret@example.com:443#Node\n"
	raw := []byte(base64.RawStdEncoding.EncodeToString([]byte(strings.Repeat(line, 1_000))))
	b.ReportAllocs()
	b.SetBytes(int64(len(raw)))
	for b.Loop() {
		if _, converted, err := NormalizeSource(raw); err != nil || !converted {
			b.Fatalf("NormalizeSource() = converted %v, error %v", converted, err)
		}
	}
}

func TestGenerateConfigIncludesRobustDNSDefaults(t *testing.T) {
	content, err := GenerateConfig([]Node{{
		Name: "Node", Type: "ss",
		Config: map[string]any{"server": "example.com", "port": 443, "cipher": "aes-128-gcm", "password": "pass"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := yaml.Unmarshal(content, &config); err != nil {
		t.Fatal(err)
	}
	dns, ok := config["dns"].(map[string]any)
	if !ok {
		t.Fatalf("dns = %#v", config["dns"])
	}
	if dns["enable"] != true || dns["ipv6"] != false || dns["enhanced-mode"] != "fake-ip" || dns["fake-ip-range"] != "198.18.0.1/16" {
		t.Fatalf("DNS defaults = %#v", dns)
	}
	if got := yamlStringSlice(t, dns["default-nameserver"]); len(got) != 2 || got[0] != "223.5.5.5" || got[1] != "1.1.1.1" {
		t.Fatalf("default-nameserver = %#v", got)
	}
	if got := yamlStringSlice(t, dns["nameserver"]); len(got) != 2 || got[0] != "https://dns.alidns.com/dns-query" || got[1] != "https://cloudflare-dns.com/dns-query" {
		t.Fatalf("nameserver = %#v", got)
	}
	if got := yamlStringSlice(t, dns["proxy-server-nameserver"]); len(got) != 2 {
		t.Fatalf("proxy-server-nameserver = %#v", got)
	}
	profileMap := config["profile"].(map[string]any)
	if profileMap["store-selected"] != true || profileMap["store-fake-ip"] != true {
		t.Fatalf("profile defaults = %#v", profileMap)
	}
}

func TestMergeManagedConfigPreservesUnknownFields(t *testing.T) {
	mixedPort := 8888
	allowLAN := true
	ipv6 := true
	raw := []byte(`# original comment
custom-key:
  nested: keep
profile:
  tracing: true
  store-selected: false
tun:
  stack: system
  device: utun-test
mode: direct
mixed-port: 7890
`)
	got, err := MergeManagedConfig(raw, OverlayOptions{
		ExternalController: "127.0.0.1:19090",
		Secret:             "controller-secret",
		Settings: domain.ManagedSettings{
			Mode: domain.ModeRule, TUN: true, MixedPort: &mixedPort,
			AllowLAN: &allowLAN, IPv6: &ipv6, LogLevel: "warning",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	for _, value := range []string{"# original comment", "nested: keep", "tracing: true", "device: utun-test", "stack: system"} {
		if !strings.Contains(text, value) {
			t.Errorf("merged YAML lost %q:\n%s", value, text)
		}
	}
	var config map[string]any
	if err := yaml.Unmarshal(got, &config); err != nil {
		t.Fatal(err)
	}
	if config["external-controller"] != "127.0.0.1:19090" || config["secret"] != "controller-secret" || config["mode"] != "rule" {
		t.Fatalf("managed top-level values = %#v", config)
	}
	if config["mixed-port"] != 8888 || config["allow-lan"] != true || config["ipv6"] != true || config["log-level"] != "warning" {
		t.Fatalf("optional values = %#v", config)
	}
	profileMap := config["profile"].(map[string]any)
	if profileMap["store-selected"] != true || profileMap["tracing"] != true {
		t.Fatalf("profile = %#v", profileMap)
	}
	tun := config["tun"].(map[string]any)
	if tun["enable"] != true || tun["auto-route"] != true || tun["auto-redirect"] != true || tun["auto-detect-interface"] != true {
		t.Fatalf("tun = %#v", tun)
	}
	if tun["stack"] != "system" {
		t.Fatalf("existing TUN value was overwritten: %#v", tun)
	}
	hijack := yamlStringSlice(t, tun["dns-hijack"])
	if len(hijack) != 2 || hijack[0] != "any:53" || hijack[1] != "tcp://any:53" {
		t.Fatalf("dns-hijack = %#v", hijack)
	}
	dns, ok := config["dns"].(map[string]any)
	if !ok {
		t.Fatalf("TUN overlay did not inject DNS defaults: %#v", config["dns"])
	}
	if dns["ipv6"] != true {
		t.Fatalf("injected DNS IPv6 setting = %#v, want true", dns["ipv6"])
	}
	if profileMap["store-fake-ip"] != true {
		t.Fatalf("profile did not enable fake-IP persistence: %#v", profileMap)
	}
}

func TestMergeManagedConfigReplacesLegacyHTTPAndSOCKSListeners(t *testing.T) {
	mixedPort := 7980
	raw := []byte("port: 7890\nsocks-port: 7891\nredir-port: 7892\ntproxy-port: 7893\nproxies: []\n")
	got, err := MergeManagedConfig(raw, OverlayOptions{
		Settings: domain.ManagedSettings{MixedPort: &mixedPort},
	})
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := yaml.Unmarshal(got, &config); err != nil {
		t.Fatal(err)
	}
	if config["mixed-port"] != 7980 {
		t.Fatalf("mixed-port = %#v", config["mixed-port"])
	}
	for _, key := range []string{"port", "socks-port"} {
		if _, exists := config[key]; exists {
			t.Errorf("managed config retained legacy %q: %s", key, got)
		}
	}
	if config["redir-port"] != 7892 || config["tproxy-port"] != 7893 {
		t.Fatalf("advanced transparent listeners were changed: %s", got)
	}
}

func TestMergeManagedConfigTUNOffDoesNotAddDefaults(t *testing.T) {
	storeSelected := false
	got, err := MergeManagedConfig([]byte("proxies: []\n"), OverlayOptions{StoreSelected: &storeSelected})
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := yaml.Unmarshal(got, &config); err != nil {
		t.Fatal(err)
	}
	tun := config["tun"].(map[string]any)
	if len(tun) != 1 || tun["enable"] != false {
		t.Fatalf("tun = %#v", tun)
	}
	profileMap := config["profile"].(map[string]any)
	if profileMap["store-selected"] != false {
		t.Fatalf("profile = %#v", profileMap)
	}
	if _, exists := config["dns"]; exists {
		t.Fatalf("TUN-off overlay unexpectedly added DNS: %#v", config["dns"])
	}
}

func TestMergeManagedConfigPreservesExistingDNSAndHijack(t *testing.T) {
	raw := []byte(`dns:
  enable: false
  nameserver:
    - 192.0.2.53
tun:
  dns-hijack:
    - udp://0.0.0.0:53
`)
	got, err := MergeManagedConfig(raw, OverlayOptions{
		Settings: domain.ManagedSettings{TUN: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := yaml.Unmarshal(got, &config); err != nil {
		t.Fatal(err)
	}
	dns := config["dns"].(map[string]any)
	if len(dns) != 2 || dns["enable"] != false {
		t.Fatalf("existing DNS was changed: %#v", dns)
	}
	if got := yamlStringSlice(t, dns["nameserver"]); len(got) != 1 || got[0] != "192.0.2.53" {
		t.Fatalf("existing nameserver was changed: %#v", got)
	}
	if _, exists := dns["proxy-server-nameserver"]; exists {
		t.Fatalf("disabled DNS gained proxy-server-nameserver: %#v", dns)
	}
	tun := config["tun"].(map[string]any)
	if got := yamlStringSlice(t, tun["dns-hijack"]); len(got) != 1 || got[0] != "udp://0.0.0.0:53" {
		t.Fatalf("existing dns-hijack was changed: %#v", got)
	}
}

func TestMergeManagedConfigAddsSystemProxyServerNameserver(t *testing.T) {
	raw := []byte(`dns:
  enable: true
  nameserver:
    - 192.0.2.53
`)
	got, err := MergeManagedConfig(raw, OverlayOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := yaml.Unmarshal(got, &config); err != nil {
		t.Fatal(err)
	}
	dns := config["dns"].(map[string]any)
	if got := yamlStringSlice(t, dns["proxy-server-nameserver"]); len(got) != 1 || got[0] != "system" {
		t.Fatalf("proxy-server-nameserver = %#v", got)
	}
}

func TestMergeManagedConfigPreservesExplicitProxyServerNameserver(t *testing.T) {
	tests := map[string]string{
		"custom list": "\n    - https://resolver.example/dns-query",
		"empty list":  " []",
		"null":        " null",
		"scalar":      " custom-resolver",
	}
	for name, resolver := range tests {
		t.Run(name, func(t *testing.T) {
			raw := []byte("dns:\n  enable: true\n  nameserver:\n    - 192.0.2.53\n  proxy-server-nameserver:" + resolver + "\n")
			var before map[string]any
			if err := yaml.Unmarshal(raw, &before); err != nil {
				t.Fatal(err)
			}
			got, err := MergeManagedConfig(raw, OverlayOptions{})
			if err != nil {
				t.Fatal(err)
			}
			var after map[string]any
			if err := yaml.Unmarshal(got, &after); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(after["dns"], before["dns"]) {
				t.Fatalf("explicit proxy-server-nameserver changed: before=%#v after=%#v", before["dns"], after["dns"])
			}
		})
	}
}

func TestMergeManagedConfigDoesNotAddProxyServerNameserverWithoutEnabledDNS(t *testing.T) {
	tests := map[string]string{
		"disabled":       "dns:\n  enable: false\n  nameserver: [192.0.2.53]\n",
		"missing enable": "dns:\n  nameserver: [192.0.2.53]\n",
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := MergeManagedConfig([]byte(raw), OverlayOptions{})
			if err != nil {
				t.Fatal(err)
			}
			var config map[string]any
			if err := yaml.Unmarshal(got, &config); err != nil {
				t.Fatal(err)
			}
			dns := config["dns"].(map[string]any)
			if _, exists := dns["proxy-server-nameserver"]; exists {
				t.Fatalf("DNS unexpectedly gained proxy-server-nameserver: %#v", dns)
			}
		})
	}
}

func TestMergeManagedConfigPreservesMergedProxyServerNameserver(t *testing.T) {
	raw := []byte(`dns-defaults: &dns-defaults
  enable: true
  proxy-server-nameserver:
    - https://resolver.example/dns-query
dns:
  <<: *dns-defaults
  nameserver:
    - 192.0.2.53
`)
	got, err := MergeManagedConfig(raw, OverlayOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "- system") {
		t.Fatalf("merged proxy-server-nameserver was overridden:\n%s", got)
	}
	var config map[string]any
	if err := yaml.Unmarshal(got, &config); err != nil {
		t.Fatal(err)
	}
	dns := config["dns"].(map[string]any)
	if got := yamlStringSlice(t, dns["proxy-server-nameserver"]); len(got) != 1 || got[0] != "https://resolver.example/dns-query" {
		t.Fatalf("merged proxy-server-nameserver changed: %#v", got)
	}
}

func TestMergeManagedConfigRejectsInvalidManagedMapping(t *testing.T) {
	if _, err := MergeManagedConfig([]byte("profile: invalid\n"), OverlayOptions{}); err == nil {
		t.Fatal("expected invalid profile mapping error")
	}
	if _, _, err := NormalizeSource([]byte("---\na: 1\n---\nb: 2\n")); err == nil {
		t.Fatal("expected multiple-document error")
	}
}

func yamlStringSlice(t *testing.T, value any) []string {
	t.Helper()
	items, ok := value.([]any)
	if !ok {
		t.Fatalf("value is not a YAML sequence: %#v", value)
	}
	result := make([]string, len(items))
	for index, item := range items {
		text, ok := item.(string)
		if !ok {
			t.Fatalf("sequence item is not a string: %#v", item)
		}
		result[index] = text
	}
	return result
}
