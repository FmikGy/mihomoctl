package profile

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"mihomoctl/internal/domain"
)

// TestExternalSubscriptionCompatibility is opt-in so normal test runs remain
// deterministic and never embed subscription credentials in source or logs.
func TestExternalSubscriptionCompatibility(t *testing.T) {
	source := os.Getenv("MIHOMOCTL_TEST_SUBSCRIPTION_URL")
	if source == "" {
		t.Skip("MIHOMOCTL_TEST_SUBSCRIPTION_URL is not set")
	}
	core := os.Getenv("MIHOMOCTL_TEST_MIHOMO")
	if core == "" {
		var err error
		core, err = exec.LookPath("mihomo")
		if err != nil {
			t.Skip("mihomo is not installed")
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.AddRemote(ctx, "external", source, time.Hour)
	if err != nil {
		t.Fatalf("download or normalize external subscription: %v", err)
	}
	config, err := store.Config(created.ID)
	if err != nil {
		t.Fatalf("read normalized external subscription: %v", err)
	}
	port, allowLAN, ipv6 := 17890, false, false
	managed, err := MergeManagedConfig(config, OverlayOptions{
		ExternalController: "127.0.0.1:19090",
		Secret:             "integration-test-secret",
		Settings: domain.ManagedSettings{
			Mode: domain.ModeRule, MixedPort: &port, AllowLAN: &allowLAN,
			IPv6: &ipv6, LogLevel: "info",
		},
	})
	if err != nil {
		t.Fatalf("apply managed settings: %v", err)
	}
	dataDir := t.TempDir()
	configPath := filepath.Join(dataDir, "config.yaml")
	if err := os.WriteFile(configPath, managed, 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, core, "-t", "-d", dataDir, "-f", configPath)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			t.Fatal("mihomo validation timed out")
		}
		t.Fatalf("mihomo rejected normalized external subscription: %v (%s)", err, sanitizeIntegrationDiagnostic(output.String()))
	}
}

var integrationURLPattern = regexp.MustCompile(`(?i)https?://[^\s"']+`)

func sanitizeIntegrationDiagnostic(value string) string {
	value = integrationURLPattern.ReplaceAllString(value, "[redacted URL]")
	value = strings.ReplaceAll(value, "integration-test-secret", "[redacted]")
	lines := strings.Split(strings.TrimSpace(value), "\n")
	if len(lines) > 8 {
		lines = lines[len(lines)-8:]
	}
	value = strings.Join(lines, " | ")
	if len(value) > 2000 {
		value = value[len(value)-2000:]
	}
	return value
}
