package profile

import (
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
	tun := config["tun"].(map[string]any)
	if got := yamlStringSlice(t, tun["dns-hijack"]); len(got) != 1 || got[0] != "udp://0.0.0.0:53" {
		t.Fatalf("existing dns-hijack was changed: %#v", got)
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
