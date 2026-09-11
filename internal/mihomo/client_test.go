package mihomo

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

func testClient(t *testing.T, handler http.HandlerFunc, options ...Option) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewClient(server.URL+"/api/", "top-secret", options...)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client
}

func TestNewClientValidation(t *testing.T) {
	t.Parallel()
	tests := []string{"", "ftp://localhost:9090", "http://u:p@localhost:9090", "http://localhost:9090?a=b"}
	for _, address := range tests {
		if _, err := NewClient(address, ""); err == nil {
			t.Errorf("NewClient(%q) succeeded, want error", address)
		}
	}
	if _, err := NewClient("127.0.0.1:9090", ""); err != nil {
		t.Fatalf("host:port should be accepted: %v", err)
	}
	if _, err := NewClient("http://localhost", "", WithHTTPClient(nil)); err == nil {
		t.Fatal("nil HTTP client should be rejected")
	}
	if _, err := NewClient("http://localhost", "", WithRequestTimeout(-time.Second)); err == nil {
		t.Fatal("negative request timeout should be rejected")
	}
}

func TestVersionUsesBearerAuthentication(t *testing.T) {
	t.Parallel()
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/api/version"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		if got, want := r.Header.Get("Authorization"), "Bearer top-secret"; got != want {
			t.Errorf("Authorization = %q, want %q", got, want)
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q", got)
		}
		_, _ = w.Write([]byte(`{"meta":true,"version":"1.19.30"}`))
	})

	version, err := client.Version(context.Background())
	if err != nil {
		t.Fatalf("Version() error = %v", err)
	}
	if !version.Meta || version.Version != "1.19.30" {
		t.Fatalf("Version() = %#v", version)
	}
}

func TestConfigsPatchAndCacheEndpoints(t *testing.T) {
	t.Parallel()
	var patchSeen atomic.Bool
	seen := make(chan string, 2)
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/configs":
			_, _ = w.Write([]byte(`{"mixed-port":7890,"allow-lan":true,"mode":"rule","log-level":"info","ipv6":false,"tun":{"enable":true,"stack":"mixed","auto-route":true}}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/api/configs":
			if got := r.Header.Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type = %q", got)
			}
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("decode patch: %v", err)
			}
			if payload["mixed-port"] != float64(8899) || payload["allow-lan"] != false {
				t.Errorf("patch payload = %#v", payload)
			}
			patchSeen.Store(true)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && (r.URL.Path == "/api/cache/dns/flush" || r.URL.Path == "/api/cache/fakeip/flush"):
			seen <- r.URL.Path
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})

	config, err := client.Configs(context.Background())
	if err != nil {
		t.Fatalf("Configs() error = %v", err)
	}
	if config.MixedPort != 7890 || !config.AllowLAN || !config.TUN.Enable || !config.TUN.AutoRoute {
		t.Fatalf("Configs() = %#v", config)
	}
	port, allowLAN := 8899, false
	if err := client.PatchConfigs(context.Background(), ConfigPatch{MixedPort: &port, AllowLAN: &allowLAN}); err != nil {
		t.Fatalf("PatchConfigs() error = %v", err)
	}
	if !patchSeen.Load() {
		t.Fatal("PATCH /configs was not observed")
	}
	if err := client.FlushDNSCache(context.Background()); err != nil {
		t.Fatalf("FlushDNSCache() error = %v", err)
	}
	if err := client.FlushFakeIPCache(context.Background()); err != nil {
		t.Fatalf("FlushFakeIPCache() error = %v", err)
	}
	got := map[string]bool{<-seen: true, <-seen: true}
	if !got["/api/cache/dns/flush"] || !got["/api/cache/fakeip/flush"] {
		t.Fatalf("cache endpoints = %#v", got)
	}
}

func TestAPIErrorAuthenticationResponsesAreFixed(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		status := status
		t.Run(http.StatusText(status), func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"message": "reflected " + r.Header.Get("Authorization"),
				})
			})

			_, err := client.Version(context.Background())
			if err == nil {
				t.Fatal("Version() succeeded, want error")
			}
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("error type = %T, want *APIError", err)
			}
			if apiErr.StatusCode != status || apiErr.Path != "/api/version" || apiErr.Message != authErrorMessage {
				t.Fatalf("APIError = %#v", apiErr)
			}
			if strings.Contains(err.Error(), "reflected") || strings.Contains(err.Error(), "top-secret") {
				t.Fatalf("authentication response was exposed: %q", err)
			}
		})
	}
}

func TestAPIErrorRedactsReflectedBearer(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		json        bool
		wantMessage string
	}{
		{
			name:        "JSON",
			json:        true,
			wantMessage: "authorization [redacted]; credential [redacted]",
		},
		{
			name:        "raw",
			wantMessage: "authorization [redacted]; credential [redacted]",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				authorization := r.Header.Get("Authorization")
				credential := strings.TrimPrefix(authorization, "Bearer ")
				message := "authorization " + authorization + "; credential " + credential
				w.WriteHeader(http.StatusBadGateway)
				if test.json {
					_ = json.NewEncoder(w).Encode(map[string]string{"message": message})
					return
				}
				_, _ = w.Write([]byte(message))
			})

			_, err := client.Version(context.Background())
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("error type = %T, want *APIError", err)
			}
			if apiErr.Message != test.wantMessage {
				t.Fatalf("message = %q, want %q", apiErr.Message, test.wantMessage)
			}
			if strings.Contains(err.Error(), "top-secret") {
				t.Fatalf("credential was exposed: %q", err)
			}
		})
	}
}

func TestAPIErrorPreservesOrdinaryBusinessMessage(t *testing.T) {
	t.Parallel()
	const message = "配置校验失败：端口已占用"
	client := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":"` + message + `"}`))
	})

	_, err := client.Version(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error type = %T, want *APIError", err)
	}
	if apiErr.Message != message {
		t.Fatalf("message = %q, want %q", apiErr.Message, message)
	}
}

func TestSanitizeAPIErrorMessageTruncatesUTF8ByRune(t *testing.T) {
	t.Parallel()
	message := strings.Repeat("界", maxErrorMessageRunes+1)
	got := sanitizeAPIErrorMessage(nil, message)
	if !utf8.ValidString(got) {
		t.Fatalf("message is not valid UTF-8")
	}
	if utf8.RuneCountInString(got) != maxErrorMessageRunes {
		t.Fatalf("rune count = %d, want %d", utf8.RuneCountInString(got), maxErrorMessageRunes)
	}
	if got != strings.Repeat("界", maxErrorMessageRunes) {
		t.Fatal("message was not truncated at the rune boundary")
	}
}

func TestRequestTimeoutAndCanceledContext(t *testing.T) {
	t.Parallel()
	client := testClient(t, func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}, WithRequestTimeout(20*time.Millisecond))

	_, err := client.Version(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.Version(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled error = %v", err)
	}
}

func TestDefaultClientLimitsResponseHeaderWait(t *testing.T) {
	client, err := NewClient("http://127.0.0.1:9090", "")
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := client.httpClient.Transport.(*http.Transport)
	if !ok || transport.ResponseHeaderTimeout != defaultResponseHeaderTimeout {
		t.Fatalf("default transport = %#v", client.httpClient.Transport)
	}
	if transport.ResponseHeaderTimeout <= defaultDelayTimeout {
		t.Fatalf("response header timeout = %v, must exceed delay probe timeout %v", transport.ResponseHeaderTimeout, defaultDelayTimeout)
	}
}

var errStubTransport = errors.New("stub transport")

type stubRoundTripper struct{}

func (stubRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errStubTransport
}

func TestRequestPreservesTransportError(t *testing.T) {
	client, err := NewClient("http://127.0.0.1:9090", "", WithHTTPClient(&http.Client{Transport: stubRoundTripper{}}))
	if err != nil {
		t.Fatal(err)
	}

	_, err = client.Version(context.Background())
	if !errors.Is(err, errStubTransport) {
		t.Fatalf("Version() error = %v, want original transport error", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("transport error was incorrectly reclassified as canceled: %v", err)
	}
}

func TestClientTransportAcceptsCustomAndNilDefaults(t *testing.T) {
	custom := stubRoundTripper{}
	if got := clientTransport(custom); got != custom {
		t.Fatalf("custom transport was replaced: %#v", got)
	}
	if got := clientTransport(nil); got == nil {
		t.Fatal("nil default transport was not replaced")
	}
}
