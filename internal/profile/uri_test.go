package profile

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestParseURIProtocols(t *testing.T) {
	vmessJSON, err := json.Marshal(map[string]any{
		"v": "2", "ps": "vm", "add": "vm.example", "port": "443", "id": "uuid-vm",
		"aid": "0", "scy": "auto", "net": "ws", "host": "cdn.example", "path": "/ws",
		"tls": "tls", "sni": "vm.example", "fp": "chrome",
	})
	if err != nil {
		t.Fatal(err)
	}
	ssUser := base64.RawURLEncoding.EncodeToString([]byte("aes-128-gcm:p@ss"))
	cases := []struct {
		name       string
		uri        string
		wantType   string
		wantName   string
		wantServer string
		wantPort   int
		checks     map[string]any
	}{
		{
			name: "ss", uri: "ss://" + ssUser + "@ss.example:8388?udp=false#Hong%20Kong",
			wantType: "ss", wantName: "Hong Kong", wantServer: "ss.example", wantPort: 8388,
			checks: map[string]any{"cipher": "aes-128-gcm", "password": "p@ss", "udp": false},
		},
		{
			name: "vmess", uri: "vmess://" + base64.RawStdEncoding.EncodeToString(vmessJSON),
			wantType: "vmess", wantName: "vm", wantServer: "vm.example", wantPort: 443,
			checks: map[string]any{"uuid": "uuid-vm", "network": "ws", "tls": true},
		},
		{
			name: "vless", uri: "vless://uuid-vl@vl.example:443?encryption=none&security=reality&sni=www.example&type=ws&host=cdn.example&path=%2Fedge&fp=chrome&pbk=public-key&sid=12ab#VL",
			wantType: "vless", wantName: "VL", wantServer: "vl.example", wantPort: 443,
			checks: map[string]any{"uuid": "uuid-vl", "network": "ws", "tls": true},
		},
		{
			name: "trojan", uri: "trojan://secret@tr.example:443?security=tls&sni=tr.example&type=grpc&serviceName=svc#TR",
			wantType: "trojan", wantName: "TR", wantServer: "tr.example", wantPort: 443,
			checks: map[string]any{"password": "secret", "network": "grpc", "tls": true},
		},
		{
			name: "hysteria2", uri: "hy2://auth@hy.example:8443?sni=hy.example&insecure=1&obfs=salamander&obfs-password=obfs#HY",
			wantType: "hysteria2", wantName: "HY", wantServer: "hy.example", wantPort: 8443,
			checks: map[string]any{"password": "auth", "skip-cert-verify": true, "obfs": "salamander"},
		},
		{
			name: "tuic", uri: "tuic://uuid-tu:pass@tu.example:443?sni=tu.example&alpn=h3&congestion_control=bbr&udp_relay_mode=native&allow_insecure=1#TU",
			wantType: "tuic", wantName: "TU", wantServer: "tu.example", wantPort: 443,
			checks: map[string]any{"uuid": "uuid-tu", "password": "pass", "congestion-controller": "bbr", "skip-cert-verify": true},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			node, err := ParseURI(test.uri)
			if err != nil {
				t.Fatalf("ParseURI() error = %v", err)
			}
			if node.Type != test.wantType || node.Name != test.wantName {
				t.Fatalf("node = %#v, want type %q name %q", node, test.wantType, test.wantName)
			}
			if node.Config["server"] != test.wantServer || node.Config["port"] != test.wantPort {
				t.Fatalf("server config = %#v", node.Config)
			}
			for key, want := range test.checks {
				if got := node.Config[key]; got != want {
					t.Errorf("Config[%q] = %#v, want %#v", key, got, want)
				}
			}
		})
	}
}

func TestParseURIUsesH2TransportOptions(t *testing.T) {
	for name, uri := range map[string]string{
		"vless":  "vless://uuid@example.com:443?encryption=none&security=tls&type=h2&host=one.example,two.example&path=%2Fedge#H2-VLESS",
		"trojan": "trojan://secret@example.com:443?security=tls&type=h2&host=one.example,two.example&path=%2Fedge#H2-Trojan",
	} {
		t.Run(name, func(t *testing.T) {
			node, err := ParseURI(uri)
			if err != nil {
				t.Fatal(err)
			}
			if _, exists := node.Config["http-opts"]; exists {
				t.Fatalf("H2 node retained http-opts: %#v", node.Config)
			}
			got, ok := node.Config["h2-opts"].(map[string]any)
			if !ok {
				t.Fatalf("h2-opts = %#v", node.Config["h2-opts"])
			}
			want := map[string]any{"path": "/edge", "host": []string{"one.example", "two.example"}}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("h2-opts = %#v, want %#v", got, want)
			}
		})
	}
}

func TestParseSSLegacyAndPlugin(t *testing.T) {
	payload := base64.RawStdEncoding.EncodeToString([]byte("chacha20-ietf-poly1305:legacy-pass@[2001:db8::1]:1443"))
	node, err := ParseURI("ss://" + payload + "?plugin=obfs-local%3Bobfs%3Dhttp%3Bobfs-host%3Dcdn.example#Legacy")
	if err != nil {
		t.Fatal(err)
	}
	if node.Config["server"] != "2001:db8::1" || node.Config["port"] != 1443 {
		t.Fatalf("legacy server = %#v", node.Config)
	}
	if node.Config["plugin"] != "obfs" {
		t.Fatalf("plugin = %#v", node.Config["plugin"])
	}
}

func TestParseURIListBase64AndDuplicateNames(t *testing.T) {
	user := base64.RawURLEncoding.EncodeToString([]byte("aes-128-gcm:password"))
	plain := "ss://" + user + "@one.example:443#Same\n" +
		"trojan://secret@two.example:443#Same\n"
	encoded := base64.StdEncoding.EncodeToString([]byte(plain))
	nodes, err := ParseURIList(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 || nodes[0].Name != "Same" || nodes[1].Name != "Same (2)" {
		t.Fatalf("nodes = %#v", nodes)
	}
}

func TestParseURIRejectsUnsupportedDetails(t *testing.T) {
	for _, input := range []string{
		"socks5://user:pass@example.com:1080",
		"vless://uuid@example.com:443?unknown=value",
		"trojan://pass@example.com:443?type=quic",
	} {
		if _, err := ParseURI(input); err == nil {
			t.Fatalf("ParseURI(%q) unexpectedly succeeded", input)
		}
	}
}

func TestParseURIErrorDoesNotLeakCredentials(t *testing.T) {
	secret := "password-must-not-leak"
	_, err := ParseURI("trojan://" + secret + "@example.com:%zz")
	if err == nil {
		t.Fatal("expected invalid URI error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("parse error leaks credentials: %v", err)
	}
}

func TestGenerateConfig(t *testing.T) {
	nodes := []Node{
		{Name: "Node", Type: "ss", Config: map[string]any{"server": "one.example", "port": 443, "cipher": "aes-128-gcm", "password": "p"}},
		{Name: "Node", Type: "trojan", Config: map[string]any{"server": "two.example", "port": 443, "password": "p"}},
	}
	content, err := GenerateConfig(nodes)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := yaml.Unmarshal(content, &config); err != nil {
		t.Fatal(err)
	}
	groups, ok := config["proxy-groups"].([]any)
	if !ok || len(groups) != 2 {
		t.Fatalf("proxy-groups = %#v", config["proxy-groups"])
	}
	rules, ok := config["rules"].([]any)
	if !ok || len(rules) != 4 || rules[len(rules)-1] != "MATCH,PROXY" {
		t.Fatalf("rules = %#v", config["rules"])
	}
	text := string(content)
	for _, required := range []string{"name: PROXY", "name: AUTO", "GEOSITE,CN,DIRECT", "MetaCubeX"} {
		if !strings.Contains(text, required) {
			t.Errorf("generated config does not contain %q\n%s", required, text)
		}
	}
}
