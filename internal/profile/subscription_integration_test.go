package profile

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"mihomoctl/internal/domain"
)

// TestLiveSubscriptionEndToEnd validates a real subscription without ever
// including its URL, downloaded config, or proxy credentials in test output.
func TestLiveSubscriptionEndToEnd(t *testing.T) {
	subscriptionURL := os.Getenv("MIHOMOCTL_SUBSCRIPTION_URL")
	if subscriptionURL == "" {
		t.Skip("set MIHOMOCTL_SUBSCRIPTION_URL to run the live subscription test")
	}
	parsedSource, err := url.Parse(subscriptionURL)
	if err != nil || parsedSource.Scheme == "" || parsedSource.Host == "" {
		t.Fatal("MIHOMOCTL_SUBSCRIPTION_URL is not a valid absolute URL")
	}
	binary, err := exec.LookPath("mihomo")
	if err != nil {
		t.Skip("mihomo is not installed")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	storeRoot := secureTempDir(t)
	store, err := NewStore(storeRoot)
	if err != nil {
		t.Fatal("initialize temporary profile store failed")
	}
	profile, err := store.AddRemote(ctx, "live-subscription", subscriptionURL, DefaultUpdateInterval)
	if err != nil {
		t.Fatal("download or normalize live subscription failed")
	}
	if profile.Source != RedactURL(subscriptionURL) {
		t.Fatal("stored subscription source is not canonically redacted")
	}
	redactedSource, err := url.Parse(profile.Source)
	if err != nil || redactedSource.User != nil || redactedSource.RawQuery != "" || redactedSource.Fragment != "" {
		t.Fatal("stored subscription source exposes private URL components")
	}
	if parsedSource.RawQuery != "" && profile.Source == subscriptionURL {
		t.Fatal("stored subscription source retained the original query")
	}

	activeConfig, err := store.ActiveConfig()
	if err != nil {
		t.Fatal("read active subscription config failed")
	}
	mixedPort := 17890
	allowLAN := false
	ipv6 := false
	managedConfig, err := MergeManagedConfig(activeConfig, OverlayOptions{
		ExternalController: "127.0.0.1:19090",
		Secret:             "mihomoctl-live-test-secret",
		Settings: domain.ManagedSettings{
			Mode: domain.ModeRule, TUN: true, MixedPort: &mixedPort,
			AllowLAN: &allowLAN, IPv6: &ipv6, LogLevel: "warning",
		},
	})
	if err != nil {
		t.Fatal("apply managed settings to live subscription failed")
	}

	coreRoot := secureTempDir(t)
	configPath := filepath.Join(coreRoot, "config.yaml")
	if err := os.WriteFile(configPath, managedConfig, 0o600); err != nil {
		t.Fatal("write temporary managed config failed")
	}
	command := exec.CommandContext(ctx, binary, "-t", "-d", coreRoot, "-f", configPath)
	command.Env = childEnvironmentWithoutSubscription(coreRoot)
	output, runErr := command.CombinedOutput()
	if runErr != nil {
		if ctx.Err() != nil {
			t.Fatal("mihomo validation of live subscription timed out")
		}
		t.Fatalf("mihomo rejected the managed live subscription config: %s", sanitizeMihomoTestOutput(output, subscriptionURL))
	}
}

var mihomoTestURL = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://[^\s"'<>]+`)

func sanitizeMihomoTestOutput(output []byte, secrets ...string) string {
	message := string(output)
	for _, secret := range secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	message = mihomoTestURL.ReplaceAllString(message, "[redacted URL]")
	message = strings.TrimSpace(message)
	const limit = 4 << 10
	if len(message) > limit {
		message = message[:limit] + "..."
	}
	return message
}

func TestSanitizeMihomoTestOutput(t *testing.T) {
	const secretURL = "https://provider.example/rules.yaml?token=private"
	message := sanitizeMihomoTestOutput([]byte(`provider download `+secretURL+` failed; fallback ss://credential@example:443`), secretURL)
	if strings.Contains(message, "private") || strings.Contains(message, "credential") || strings.Contains(message, "provider.example") {
		t.Fatalf("sanitized output retained sensitive data: %q", message)
	}
	if !strings.Contains(message, "provider download") || !strings.Contains(message, "failed") {
		t.Fatalf("sanitized output lost useful context: %q", message)
	}
}

func secureTempDir(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal("secure temporary directory failed")
	}
	return directory
}

func childEnvironmentWithoutSubscription(home string) []string {
	environment := make([]string, 0, len(os.Environ())+1)
	for _, variable := range os.Environ() {
		if strings.HasPrefix(variable, "MIHOMOCTL_SUBSCRIPTION_URL=") || strings.HasPrefix(variable, "HOME=") {
			continue
		}
		environment = append(environment, variable)
	}
	return append(environment, "HOME="+home)
}
