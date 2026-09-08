package profile

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseSubscriptionUserInfo(t *testing.T) {
	info, err := ParseSubscriptionUserInfo("upload=10; download=20; total=100; expire=1893456000; ignored=value")
	if err != nil {
		t.Fatal(err)
	}
	if info.Upload != 10 || info.Download != 20 || info.Total != 100 || !info.Expire.Equal(time.Unix(1893456000, 0).UTC()) {
		t.Fatalf("info = %#v", info)
	}
	if _, err := ParseSubscriptionUserInfo("total=not-a-number"); err == nil {
		t.Fatal("expected malformed quota error")
	}
}

func TestRedactURL(t *testing.T) {
	raw := "https://user:password@example.com:8443/private/token?token=secret&name=visible#fragment"
	if got, want := RedactURL(raw), "https://example.com:8443/[redacted]"; got != want {
		t.Fatalf("RedactURL() = %q, want %q", got, want)
	}
	if got := RedactURL("not a URL secret"); got != "[redacted URL]" {
		t.Fatalf("RedactURL(invalid) = %q", got)
	}
}

func TestFetchRemoteRedirectsNeverForwardReferer(t *testing.T) {
	tests := []struct {
		name  string
		build func(t *testing.T, record func(*http.Request)) (string, *http.Client)
	}{
		{
			name: "same origin",
			build: func(t *testing.T, record func(*http.Request)) (string, *http.Client) {
				t.Helper()
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					if request.URL.Path == "/start" {
						http.Redirect(writer, request, "/result", http.StatusFound)
						return
					}
					record(request)
					_, _ = writer.Write([]byte(validYAML))
				}))
				t.Cleanup(server.Close)
				return server.URL + "/start?token=top-secret", server.Client()
			},
		},
		{
			name: "cross origin",
			build: func(t *testing.T, record func(*http.Request)) (string, *http.Client) {
				t.Helper()
				target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					record(request)
					_, _ = writer.Write([]byte(validYAML))
				}))
				t.Cleanup(target.Close)
				source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					http.Redirect(writer, request, target.URL, http.StatusTemporaryRedirect)
				}))
				t.Cleanup(source.Close)
				return source.URL + "/subscribe?token=top-secret", source.Client()
			},
		},
		{
			name: "HTTP to HTTPS",
			build: func(t *testing.T, record func(*http.Request)) (string, *http.Client) {
				t.Helper()
				target := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					record(request)
					_, _ = writer.Write([]byte(validYAML))
				}))
				t.Cleanup(target.Close)
				source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					http.Redirect(writer, request, target.URL, http.StatusFound)
				}))
				t.Cleanup(source.Close)
				return source.URL + "/subscribe?token=top-secret", target.Client()
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var headers http.Header
			rawURL, client := test.build(t, func(request *http.Request) {
				headers = request.Header.Clone()
			})
			store := newHTTPTestStore(t, client)
			if _, err := store.fetchRemote(context.Background(), rawURL, "", ""); err != nil {
				t.Fatal(err)
			}
			assertHeadersRedacted(t, headers, "top-secret")
		})
	}
}

func TestFetchRemoteMultiHopRedirectStripsRefererEveryTime(t *testing.T) {
	var mu sync.Mutex
	var observed []http.Header
	record := func(request *http.Request) {
		mu.Lock()
		observed = append(observed, request.Header.Clone())
		mu.Unlock()
	}
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		record(request)
		_, _ = writer.Write([]byte(validYAML))
	}))
	defer target.Close()
	middle := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		record(request)
		http.Redirect(writer, request, target.URL+"/result", http.StatusFound)
	}))
	defer middle.Close()
	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, middle.URL+"/next", http.StatusFound)
	}))
	defer source.Close()

	store := newHTTPTestStore(t, source.Client())
	if _, err := store.fetchRemote(context.Background(), source.URL+"/subscribe?token=multi-hop-secret", "", ""); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(observed) != 2 {
		t.Fatalf("observed redirect requests = %d, want 2", len(observed))
	}
	for _, headers := range observed {
		assertHeadersRedacted(t, headers, "multi-hop-secret")
	}
}

func TestFetchRemoteRedirectConditionalHeadersRespectOrigin(t *testing.T) {
	const (
		etag         = `"private-validator"`
		lastModified = "Mon, 02 Jan 2006 15:04:05 GMT"
	)
	tests := []struct {
		name     string
		build    func(t *testing.T, capture *http.Header) (string, *http.Client)
		wantETag string
		wantDate string
	}{
		{
			name: "same origin preserves validators",
			build: func(t *testing.T, capture *http.Header) (string, *http.Client) {
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					if request.URL.Path == "/start" {
						http.Redirect(writer, request, "/result", http.StatusFound)
						return
					}
					*capture = request.Header.Clone()
					_, _ = writer.Write([]byte(validYAML))
				}))
				t.Cleanup(server.Close)
				return server.URL + "/start", server.Client()
			},
			wantETag: etag,
			wantDate: lastModified,
		},
		{
			name: "cross origin removes validators",
			build: func(t *testing.T, capture *http.Header) (string, *http.Client) {
				target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					*capture = request.Header.Clone()
					_, _ = writer.Write([]byte(validYAML))
				}))
				t.Cleanup(target.Close)
				source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					http.Redirect(writer, request, target.URL+"/result", http.StatusFound)
				}))
				t.Cleanup(source.Close)
				return source.URL + "/start", source.Client()
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var headers http.Header
			rawURL, client := test.build(t, &headers)
			store := newHTTPTestStore(t, client)
			if _, err := store.fetchRemote(context.Background(), rawURL, etag, lastModified); err != nil {
				t.Fatal(err)
			}
			if got := headers.Get("If-None-Match"); got != test.wantETag {
				t.Fatalf("If-None-Match = %q, want %q", got, test.wantETag)
			}
			if got := headers.Get("If-Modified-Since"); got != test.wantDate {
				t.Fatalf("If-Modified-Since = %q, want %q", got, test.wantDate)
			}
		})
	}
}

func TestFetchRemoteMultiHopCrossOriginStripsValidatorsAfterCustomHook(t *testing.T) {
	var mu sync.Mutex
	var observed []http.Header
	record := func(request *http.Request) {
		mu.Lock()
		observed = append(observed, request.Header.Clone())
		mu.Unlock()
	}
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		record(request)
		_, _ = writer.Write([]byte(validYAML))
	}))
	defer target.Close()
	middle := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		record(request)
		http.Redirect(writer, request, target.URL+"/result", http.StatusFound)
	}))
	defer middle.Close()
	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, middle.URL+"/next", http.StatusFound)
	}))
	defer source.Close()

	hookCalls := 0
	client := source.Client()
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		hookCalls++
		request.Header.Set("If-None-Match", `"hook-validator"`)
		request.Header.Set("If-Modified-Since", "Tue, 03 Jan 2006 15:04:05 GMT")
		return nil
	}
	store := newHTTPTestStore(t, client)
	if _, err := store.fetchRemote(context.Background(), source.URL+"/start", `"initial-validator"`, "Mon, 02 Jan 2006 15:04:05 GMT"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if hookCalls != 2 || len(observed) != 2 {
		t.Fatalf("redirect hook calls/requests = %d/%d, want 2/2", hookCalls, len(observed))
	}
	for index, headers := range observed {
		if etag, modified := headers.Get("If-None-Match"), headers.Get("If-Modified-Since"); etag != "" || modified != "" {
			t.Fatalf("hop %d leaked validators after custom hook: %q, %q", index+1, etag, modified)
		}
	}
}

func TestFetchRemoteCrossOriginValidatorsStayStrippedOnTargetOriginRedirect(t *testing.T) {
	const (
		initialETag = `"initial-validator"`
		hookETag    = `"hook-validator"`
		initialDate = "Mon, 02 Jan 2006 15:04:05 GMT"
		hookDate    = "Tue, 03 Jan 2006 15:04:05 GMT"
	)
	var observed []http.Header
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		observed = append(observed, request.Header.Clone())
		if request.URL.Path == "/middle" {
			http.Redirect(writer, request, "/result", http.StatusFound)
			return
		}
		_, _ = writer.Write([]byte(validYAML))
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL+"/middle", http.StatusFound)
	}))
	defer source.Close()

	hookCalls := 0
	client := source.Client()
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		hookCalls++
		request.Header.Set("If-None-Match", hookETag)
		request.Header.Set("If-Modified-Since", hookDate)
		return nil
	}
	store := newHTTPTestStore(t, client)
	if _, err := store.fetchRemote(context.Background(), source.URL+"/start", initialETag, initialDate); err != nil {
		t.Fatal(err)
	}
	if hookCalls != 2 || len(observed) != 2 {
		t.Fatalf("redirect hook calls/target requests = %d/%d, want 2/2", hookCalls, len(observed))
	}
	for index, headers := range observed {
		if etag, modified := headers.Get("If-None-Match"), headers.Get("If-Modified-Since"); etag != "" || modified != "" {
			t.Fatalf("target-origin hop %d leaked validators: %q, %q", index+1, etag, modified)
		}
	}
}

func TestFetchRemoteAllowsCrossOriginHTTPSRedirect(t *testing.T) {
	var headers http.Header
	target := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		headers = request.Header.Clone()
		_, _ = writer.Write([]byte(validYAML))
	}))
	defer target.Close()
	source := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL, http.StatusFound)
	}))
	defer source.Close()
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} // #nosec G402 -- test servers use ephemeral certificates.
	store := newHTTPTestStore(t, client)
	if _, err := store.fetchRemote(context.Background(), source.URL+"/subscribe?token=https-secret", "", ""); err != nil {
		t.Fatal(err)
	}
	assertHeadersRedacted(t, headers, "https-secret")
}

func TestFetchRemoteRejectsHTTPSDowngradeWithoutLeakingToken(t *testing.T) {
	var targetRequests int
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		targetRequests++
		_, _ = writer.Write([]byte(validYAML))
	}))
	defer target.Close()
	source := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL+"/token-in-target", http.StatusFound)
	}))
	defer source.Close()
	store := newHTTPTestStore(t, source.Client())
	_, err := store.fetchRemote(context.Background(), source.URL+"/private-token?token=downgrade-secret", "", "")
	if err == nil {
		t.Fatal("HTTPS downgrade unexpectedly succeeded")
	}
	if targetRequests != 0 {
		t.Fatalf("downgrade target requests = %d, want 0", targetRequests)
	}
	for _, secret := range []string{"private-token", "downgrade-secret", "token-in-target"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("redirect error leaks %q: %v", secret, err)
		}
	}
}

func TestFetchRemotePreservesCustomRedirectHookAndSanitizesItsHeaders(t *testing.T) {
	var targetReferer string
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		targetReferer = request.Header.Get("Referer")
		_, _ = writer.Write([]byte(validYAML))
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL, http.StatusFound)
	}))
	defer source.Close()
	calls := 0
	hook := func(request *http.Request, via []*http.Request) error {
		calls++
		request.Header.Set("Referer", "https://example.test/?token=hook-secret")
		return nil
	}
	client := &http.Client{CheckRedirect: hook}
	hookPointer := reflect.ValueOf(client.CheckRedirect).Pointer()
	store := newHTTPTestStore(t, client)
	if _, err := store.fetchRemote(context.Background(), source.URL+"?token=source-secret", "", ""); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || targetReferer != "" {
		t.Fatalf("custom hook calls = %d, target Referer = %q", calls, targetReferer)
	}
	if reflect.ValueOf(client.CheckRedirect).Pointer() != hookPointer {
		t.Fatal("caller-owned HTTP client was modified")
	}

	rejectingClient := &http.Client{CheckRedirect: func(request *http.Request, via []*http.Request) error {
		return fmt.Errorf("custom-hook-secret")
	}}
	rejectingStore := newHTTPTestStore(t, rejectingClient)
	targetReferer = ""
	_, err := rejectingStore.fetchRemote(context.Background(), source.URL+"/private?token=source-secret", "", "")
	if err == nil || strings.Contains(err.Error(), "custom-hook-secret") || strings.Contains(err.Error(), "source-secret") {
		t.Fatalf("custom redirect rejection was not redacted: %v", err)
	}
}

func TestWithHTTPClientDoesNotModifyCaller(t *testing.T) {
	hook := func(request *http.Request, via []*http.Request) error { return nil }
	client := &http.Client{Timeout: time.Second, CheckRedirect: hook}
	store, err := NewStore(t.TempDir(), WithHTTPClient(client), WithHTTPTimeout(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if client.Timeout != time.Second || reflect.ValueOf(client.CheckRedirect).Pointer() != reflect.ValueOf(hook).Pointer() {
		t.Fatalf("caller-owned client was changed: %#v", client)
	}
	if store.client == client || store.client.Timeout != 2*time.Second {
		t.Fatalf("store client was not independently configured: %#v", store.client)
	}
}

func newHTTPTestStore(t *testing.T, client *http.Client) *Store {
	t.Helper()
	store, err := NewStore(t.TempDir(), WithHTTPClient(client))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func assertHeadersRedacted(t *testing.T, headers http.Header, secret string) {
	t.Helper()
	if headers == nil {
		t.Fatal("redirect target was not called")
	}
	if referer := headers.Get("Referer"); referer != "" {
		t.Fatalf("redirect target Referer = %q", referer)
	}
	if strings.Contains(fmt.Sprint(headers), secret) {
		t.Fatalf("redirect target headers leak token: %#v", headers)
	}
}
