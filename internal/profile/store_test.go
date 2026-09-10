package profile

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"mihomoctl/internal/domain"
)

const validYAML = "proxies:\n  - name: test\n    type: ss\n    server: one.example\n    port: 443\n    cipher: aes-128-gcm\n    password: pass\n"

func TestStoreURIAndLocalCRUD(t *testing.T) {
	ctx := context.Background()
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if store.client.Timeout != DefaultHTTPTimeout {
		t.Fatalf("default timeout = %s", store.client.Timeout)
	}
	uri, err := store.AddURI(ctx, "URI", "trojan://private@example.com:443#Node")
	if err != nil {
		t.Fatal(err)
	}
	if !uri.Active || uri.Source != "[redacted URI subscription]" {
		t.Fatalf("URI profile = %#v", uri)
	}
	config, err := store.ActiveConfig()
	if err != nil || !strings.Contains(string(config), "MATCH,PROXY") {
		t.Fatalf("active config = %q, %v", config, err)
	}

	localPath := filepath.Join(t.TempDir(), "local.yaml")
	if err := os.WriteFile(localPath, []byte(validYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	local, err := store.AddLocal(ctx, "Local", localPath)
	if err != nil {
		t.Fatal(err)
	}
	if local.Active {
		t.Fatal("second profile should not replace the active profile")
	}
	if _, err := store.Use(local.ID); err != nil {
		t.Fatal(err)
	}
	active, err := store.Active()
	if err != nil || active.ID != local.ID {
		t.Fatalf("active = %#v, %v", active, err)
	}
	if err := store.Remove(local.ID); err != nil {
		t.Fatal(err)
	}
	active, err = store.Active()
	if err != nil || active.ID != uri.ID {
		t.Fatalf("fallback active = %#v, %v", active, err)
	}
	if _, err := store.Get(local.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(removed) error = %v", err)
	}

	for _, path := range []string{store.statePath(), filepath.Join(store.profileDir(uri.ID), "origin"), filepath.Join(store.profileDir(uri.ID), "source")} {
		stat, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if stat.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s mode = %o, want no group/world bits", path, stat.Mode().Perm())
		}
	}
}

func TestStoreRejectsNormalizedConfigLargerThanLimit(t *testing.T) {
	store, err := NewStore(t.TempDir(), WithMaxSourceBytes(512))
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.AddURI(context.Background(), "Expanded", "trojan://private@example.com:443#Node")
	if !errors.Is(err, ErrSourceTooLarge) {
		t.Fatalf("expanded URI error = %v, want ErrSourceTooLarge", err)
	}
}

func TestStoreLocalInvalidUpdateKeepsLastGood(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "profile.yaml")
	if err := os.WriteFile(path, []byte(validYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	profile, err := store.AddLocal(ctx, "Local", path)
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.Config(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("this is not a proxy subscription"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(ctx, profile.ID); err == nil {
		t.Fatal("invalid update unexpectedly succeeded")
	}
	after, err := store.Config(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("invalid update replaced last-known-good config")
	}
}

func TestStoreLocalSourceMustResolveToRegularFile(t *testing.T) {
	ctx := context.Background()
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	t.Run("fifo is rejected without blocking", func(t *testing.T) {
		fifo := filepath.Join(t.TempDir(), "subscription.fifo")
		if err := unix.Mkfifo(fifo, 0o600); err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		_, err := store.AddLocal(ctx, "FIFO", fifo)
		if err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("FIFO import error = %v", err)
		}
		if elapsed := time.Since(started); elapsed > time.Second {
			t.Fatalf("FIFO import blocked for %s", elapsed)
		}
	})

	t.Run("fifo replacement is rejected without blocking", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "subscription.yaml")
		if err := os.WriteFile(path, []byte(validYAML), 0o600); err != nil {
			t.Fatal(err)
		}
		created, err := store.AddLocal(ctx, "FIFO update", path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := unix.Mkfifo(path, 0o600); err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		_, err = store.PrepareUpdate(ctx, created.ID)
		if err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("FIFO update error = %v", err)
		}
		if elapsed := time.Since(started); elapsed > time.Second {
			t.Fatalf("FIFO update blocked for %s", elapsed)
		}
	})

	t.Run("symlink to regular file remains supported", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "subscription.yaml")
		if err := os.WriteFile(target, []byte(validYAML), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(t.TempDir(), "subscription.yaml")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if _, err := store.AddLocal(ctx, "Symlink", link); err != nil {
			t.Fatalf("regular-file symlink import failed: %v", err)
		}
	})
}

func TestStoreRemoteConditionalUpdateAndRedaction(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	requestCount := 0
	mode := "initial"
	var conditionalETag, conditionalModified string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		requestCount++
		conditionalETag = request.Header.Get("If-None-Match")
		conditionalModified = request.Header.Get("If-Modified-Since")
		switch mode {
		case "initial":
			writer.Header().Set("ETag", `"v1"`)
			writer.Header().Set("Last-Modified", "Mon, 02 Jan 2006 15:04:05 GMT")
			writer.Header().Set("Subscription-Userinfo", "upload=1; download=2; total=100; expire=1893456000")
			_, _ = writer.Write([]byte(validYAML))
		case "not-modified":
			writer.WriteHeader(http.StatusNotModified)
		case "not-modified-clear":
			writer.Header().Set("Subscription-Userinfo", "upload=0; download=0; total=0; expire=0")
			writer.WriteHeader(http.StatusNotModified)
		case "changed":
			writer.Header().Set("ETag", `"v2"`)
			_, _ = writer.Write([]byte(strings.Replace(validYAML, "one.example", "two.example", 1)))
		case "invalid":
			_, _ = writer.Write([]byte("invalid scalar"))
		}
	}))
	defer server.Close()
	remoteURL := strings.Replace(server.URL, "://", "://user:password@", 1) + "/private-token?access_token=secret"
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fixed := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return fixed }
	profile, err := store.AddRemote(ctx, "Remote", remoteURL, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Source != server.URL+"/[redacted]" || strings.Contains(profile.Source, "secret") || profile.Subscription.Total != 100 {
		t.Fatalf("remote profile = %#v", profile)
	}
	stateContent, err := os.ReadFile(store.statePath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stateContent), "password") || strings.Contains(string(stateContent), "access_token") {
		t.Fatalf("state leaks URL credentials:\n%s", stateContent)
	}

	mu.Lock()
	mode = "not-modified"
	mu.Unlock()
	store.now = func() time.Time { return fixed.Add(time.Hour) }
	result, err := store.Update(ctx, profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !result.NotModified || result.Changed || !result.Profile.LastUpdated.Equal(fixed.Add(time.Hour)) {
		t.Fatalf("304 result = %#v", result)
	}
	if result.Profile.Subscription.Total != 100 {
		t.Fatalf("304 without quota header cleared subscription: %#v", result.Profile.Subscription)
	}
	mu.Lock()
	if conditionalETag != `"v1"` || conditionalModified != "Mon, 02 Jan 2006 15:04:05 GMT" {
		t.Errorf("conditional headers = %q, %q", conditionalETag, conditionalModified)
	}
	mode = "not-modified-clear"
	mu.Unlock()
	store.now = func() time.Time { return fixed.Add(2 * time.Hour) }
	result, err = store.Update(ctx, profile.ID)
	if err != nil || !result.NotModified || result.Profile.Subscription != (domain.SubscriptionInfo{}) {
		t.Fatalf("304 explicit zero quota result = %#v, %v", result, err)
	}
	mu.Lock()
	mode = "changed"
	mu.Unlock()
	result, err = store.Update(ctx, profile.ID)
	if err != nil || !result.Changed || result.Profile.ETag != `"v2"` {
		t.Fatalf("changed result = %#v, %v", result, err)
	}
	changed, err := store.Config(profile.ID)
	if err != nil || !strings.Contains(string(changed), "two.example") {
		t.Fatalf("changed config = %q, %v", changed, err)
	}

	mu.Lock()
	mode = "invalid"
	mu.Unlock()
	if _, err := store.Update(ctx, profile.ID); err == nil {
		t.Fatal("invalid remote update unexpectedly succeeded")
	}
	stillGood, err := store.Config(profile.ID)
	if err != nil || string(stillGood) != string(changed) {
		t.Fatal("invalid remote update replaced last-known-good config")
	}
}

func TestStoreStateReadRejectsSymlinkAndOversizedFile(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "state.yaml")
	if err := os.WriteFile(target, []byte("version: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(store.statePath()); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, store.statePath()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(); err == nil {
		t.Fatal("symlinked profile state was accepted")
	}
	if err := os.Remove(store.statePath()); err != nil {
		t.Fatal(err)
	}
	oversized := []byte(strings.Repeat("x", maxProfileStateBytes+1))
	if err := os.WriteFile(store.statePath(), oversized, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(); err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("oversized profile state error = %v", err)
	}
}

func TestRemoteAddDoesNotBlockStoreReadsWhileDownloading(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseResponse := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		close(requestStarted)
		select {
		case <-releaseResponse:
			_, _ = writer.Write([]byte(validYAML))
		case <-request.Context().Done():
		}
	}))
	defer server.Close()

	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	addDone := make(chan error, 1)
	go func() {
		_, addErr := store.AddRemote(context.Background(), "Remote", server.URL, time.Hour)
		addDone <- addErr
	}()
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		close(releaseResponse)
		t.Fatal("remote add did not start")
	}

	listDone := make(chan error, 1)
	go func() {
		_, listErr := store.List()
		listDone <- listErr
	}()
	select {
	case err := <-listDone:
		if err != nil {
			close(releaseResponse)
			t.Fatal(err)
		}
	case <-time.After(500 * time.Millisecond):
		close(releaseResponse)
		t.Fatal("store read blocked behind remote download")
	}
	close(releaseResponse)
	if err := <-addDone; err != nil {
		t.Fatal(err)
	}
}

func TestPrepareUpdateDoesNotHoldStoreMutexDuringDownload(t *testing.T) {
	ctx := context.Background()
	requestCount := 0
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestCount++
		if requestCount == 2 {
			close(started)
			<-release
		}
		_, _ = writer.Write([]byte(validYAML))
	}))
	defer server.Close()
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	profile, err := store.AddRemote(ctx, "Remote", server.URL, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	preparedResult := make(chan PreparedUpdate, 1)
	prepareErrors := make(chan error, 1)
	go func() {
		prepared, prepareErr := store.PrepareUpdate(ctx, profile.ID)
		preparedResult <- prepared
		prepareErrors <- prepareErr
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("update download did not start")
	}
	listDone := make(chan error, 1)
	go func() {
		_, listErr := store.List()
		listDone <- listErr
	}()
	select {
	case listErr := <-listDone:
		if listErr != nil {
			t.Fatal(listErr)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Store.List blocked behind update download")
	}
	close(release)
	prepared := <-preparedResult
	if err := <-prepareErrors; err != nil {
		t.Fatal(err)
	}
	if _, err := store.CommitUpdate(prepared); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareUpdateHonorsCanceledContextForLocalAndURI(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	localPath := filepath.Join(t.TempDir(), "local.yaml")
	if err := os.WriteFile(localPath, []byte(validYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	local, err := store.AddLocal(context.Background(), "Local", localPath)
	if err != nil {
		t.Fatal(err)
	}
	uri, err := store.AddURI(context.Background(), "URI", "trojan://private@example.com:443#Node")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, profile := range []domain.Profile{local, uri} {
		if _, err := store.PrepareUpdate(ctx, profile.ID); !errors.Is(err, context.Canceled) {
			t.Fatalf("PrepareUpdate(%s) error = %v, want context.Canceled", profile.Name, err)
		}
		if _, err := store.Update(ctx, profile.ID); !errors.Is(err, context.Canceled) {
			t.Fatalf("Update(%s) error = %v, want context.Canceled", profile.Name, err)
		}
		unchanged, err := store.Get(profile.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !unchanged.LastUpdated.Equal(profile.LastUpdated) {
			t.Fatalf("canceled update changed %s timestamp", profile.Name)
		}
	}
}

func TestCommitUpdateRejectsStaleBaseline(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	profile, err := store.AddURI(context.Background(), "URI", "trojan://private@example.com:443#Node")
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := store.PrepareUpdate(context.Background(), profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CommitUpdate(prepared); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CommitUpdate(prepared); !errors.Is(err, ErrStaleUpdate) {
		t.Fatalf("second CommitUpdate() error = %v, want ErrStaleUpdate", err)
	}

	prepared, err = store.PrepareUpdate(context.Background(), profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(filepath.Join(store.profileDir(profile.ID), "origin"), []byte("trojan://changed@example.com:443"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CommitUpdate(prepared); !errors.Is(err, ErrStaleUpdate) {
		t.Fatalf("CommitUpdate() after origin change error = %v, want ErrStaleUpdate", err)
	}
}

func TestStoreRemoteSizeLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = fmt.Fprint(writer, strings.Repeat("x", 65))
	}))
	defer server.Close()
	store, err := NewStore(t.TempDir(), WithMaxSourceBytes(64))
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.AddRemote(context.Background(), "Large", server.URL, time.Hour)
	if !errors.Is(err, ErrSourceTooLarge) {
		t.Fatalf("AddRemote() error = %v", err)
	}
	profiles, listErr := store.List()
	if listErr != nil || len(profiles) != 0 {
		t.Fatalf("profiles after rejected add = %#v, %v", profiles, listErr)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestStoreRemoteErrorDoesNotLeakURL(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("cannot fetch %s", request.URL.String())
	})}
	store, err := NewStore(t.TempDir(), WithHTTPClient(client))
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.AddRemote(context.Background(), "Remote", "https://user:password@example.com/private-token?access_token=secret", time.Hour)
	if err == nil {
		t.Fatal("expected request error")
	}
	message := err.Error()
	for _, secret := range []string{"password", "private-token", "access_token", "secret"} {
		if strings.Contains(message, secret) {
			t.Fatalf("request error leaks %q: %v", secret, err)
		}
	}
}

func TestStoreDue(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return base }
	profile, err := store.AddURI(context.Background(), "Due", "trojan://password@example.com:443")
	if err != nil {
		t.Fatal(err)
	}
	if due, err := store.Due(base.Add(DefaultUpdateInterval - time.Second)); err != nil || len(due) != 0 {
		t.Fatalf("early due = %#v, %v", due, err)
	}
	if due, err := store.Due(base.Add(DefaultUpdateInterval)); err != nil || len(due) != 1 || due[0].ID != profile.ID {
		t.Fatalf("due = %#v, %v", due, err)
	}
}

func TestStoreRejectsUnknownID(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Config("../../etc/passwd"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Config(traversal) error = %v", err)
	}
	if _, err := store.Add(context.Background(), AddRequest{Name: "bad", Kind: domain.ProfileKind("other"), Source: "x"}); err == nil {
		t.Fatal("unsupported profile kind unexpectedly succeeded")
	}
}

func TestImmutableSnapshotNeverRefreshes(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	original := []byte(strings.Replace(validYAML, "one.example", "original.example", 1))
	snapshot, err := store.AddSnapshot("System original", original)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Kind != domain.ProfileLocal || snapshot.UpdateInterval != 0 || !snapshot.Active {
		t.Fatalf("snapshot metadata = %#v", snapshot)
	}
	origin, err := os.ReadFile(filepath.Join(store.profileDir(snapshot.ID), "origin"))
	if err != nil {
		t.Fatal(err)
	}
	if len(origin) != 0 {
		t.Fatal("immutable snapshot retained a live origin")
	}

	refreshable, err := store.AddURI(context.Background(), "Refreshable", "trojan://password@example.com:443")
	if err != nil {
		t.Fatal(err)
	}
	updatable, err := store.Updatable()
	if err != nil {
		t.Fatal(err)
	}
	if len(updatable) != 1 || updatable[0].ID != refreshable.ID {
		t.Fatalf("updatable profiles = %#v", updatable)
	}
	due, err := store.Due(time.Now().Add(100 * 365 * 24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].ID != refreshable.ID {
		t.Fatalf("due profiles = %#v", due)
	}
	if _, err := store.Update(context.Background(), snapshot.ID); !errors.Is(err, ErrImmutable) {
		t.Fatalf("snapshot update error = %v, want ErrImmutable", err)
	}

	reopened, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Update(context.Background(), snapshot.ID); !errors.Is(err, ErrImmutable) {
		t.Fatalf("reopened snapshot update error = %v, want ErrImmutable", err)
	}
	stored, err := reopened.Config(snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != string(original) {
		t.Fatalf("immutable config changed:\n%s", stored)
	}
}

func TestAddSnapshotNormalizesBeforeWaitingForStoreLock(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	store.mu.Lock()
	locked := true
	defer func() {
		if locked {
			store.mu.Unlock()
		}
	}()

	done := make(chan error, 1)
	go func() {
		_, addErr := store.AddSnapshot("Invalid", []byte("[unterminated"))
		done <- addErr
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("invalid snapshot unexpectedly succeeded")
		}
	case <-time.After(time.Second):
		store.mu.Unlock()
		locked = false
		<-done
		t.Fatal("snapshot normalization waited for the store lock")
	}
}
