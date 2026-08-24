package profile

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mihomoctl/internal/domain"
)

// TestMihomoAcceptsGeneratedAndManagedConfig is opt-in because a clean Mihomo
// home may download geodata while resolving the generated CN rules.
func TestMihomoAcceptsGeneratedAndManagedConfig(t *testing.T) {
	if os.Getenv("MIHOMO_INTEGRATION") != "1" {
		t.Skip("set MIHOMO_INTEGRATION=1 to validate against an installed Mihomo")
	}
	binary, err := exec.LookPath("mihomo")
	if err != nil {
		t.Skip("mihomo is not installed")
	}

	vmessJSON, err := json.Marshal(map[string]any{
		"v": "2", "ps": "VMess", "add": "vmess.example.com", "port": "443",
		"id": "11111111-1111-4111-8111-111111111111", "aid": "0", "scy": "auto",
		"net": "ws", "host": "cdn.example.com", "path": "/vmess", "tls": "tls",
		"sni": "vmess.example.com", "fp": "chrome",
	})
	if err != nil {
		t.Fatal(err)
	}
	ssCredentials := base64.RawURLEncoding.EncodeToString([]byte("aes-128-gcm:ss-password"))
	inputs := []string{
		"ss://" + ssCredentials + "@ss.example.com:8388#SS",
		"vmess://" + base64.RawStdEncoding.EncodeToString(vmessJSON),
		"vless://22222222-2222-4222-8222-222222222222@vless.example.com:443?encryption=none&security=tls&sni=vless.example.com&type=ws&host=cdn.example.com&path=%2Fvless&fp=chrome#VLESS",
		"trojan://trojan-password@trojan.example.com:443?security=tls&sni=trojan.example.com&type=grpc&serviceName=trojan#Trojan",
		"hysteria2://hysteria-password@hysteria.example.com:443?sni=hysteria.example.com&insecure=1&obfs=salamander&obfs-password=obfs-password#Hysteria2",
		"tuic://33333333-3333-4333-8333-333333333333:tuic-password@tuic.example.com:443?sni=tuic.example.com&alpn=h3&congestion_control=bbr&udp_relay_mode=native&allow_insecure=1#TUIC",
	}
	nodes := make([]Node, 0, len(inputs))
	for _, input := range inputs {
		node, parseErr := ParseURI(input)
		if parseErr != nil {
			t.Fatalf("ParseURI(%s) error = %v", strings.SplitN(input, ":", 2)[0], parseErr)
		}
		nodes = append(nodes, node)
	}
	generated, err := GenerateConfig(nodes)
	if err != nil {
		t.Fatal(err)
	}

	workDir := t.TempDir()
	assertMihomoConfig(t, binary, workDir, "generated.yaml", generated)

	mixedPort := 17890
	allowLAN := false
	ipv6 := false
	managed, err := MergeManagedConfig(generated, OverlayOptions{
		ExternalController: "127.0.0.1:19090",
		Secret:             "integration-secret",
		Settings: domain.ManagedSettings{
			Mode: domain.ModeRule, TUN: true, MixedPort: &mixedPort,
			AllowLAN: &allowLAN, IPv6: &ipv6, LogLevel: "warning",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertMihomoConfig(t, binary, workDir, "managed.yaml", managed)
}

func assertMihomoConfig(t *testing.T, binary, workDir, name string, config []byte) {
	t.Helper()
	configPath := filepath.Join(workDir, name)
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "-t", "-d", workDir, "-f", configPath)
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("mihomo -t %s timed out: %v\n%s", name, ctx.Err(), output)
	}
	if err != nil {
		t.Fatalf("mihomo -t %s failed: %v\n%s\n--- config ---\n%s", name, err, output, config)
	}
	t.Logf("mihomo accepted %s: %s", name, strings.TrimSpace(string(output)))
}
