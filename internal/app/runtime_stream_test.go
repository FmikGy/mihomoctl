package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mihomoctl/internal/mihomo"
)

func TestWatchTrafficStreamsSamplesAndCloses(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/traffic" {
			http.NotFound(writer, request)
			return
		}
		_, _ = writer.Write([]byte("{\"up\":1,\"down\":2,\"upTotal\":3,\"downTotal\":4}\n" +
			"{\"up\":5,\"down\":6,\"upTotal\":7,\"downTotal\":8}\n"))
	}))
	defer server.Close()

	api, err := mihomo.New(server.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	application := &App{api: api, euid: func() int { return 1000 }}
	samples, errs := application.WatchTraffic(context.Background())

	var gotUp []int64
	for sample := range samples {
		gotUp = append(gotUp, sample.Up)
		if sample.Up == 5 && sample.DownTotal != 8 {
			t.Fatalf("second traffic sample = %#v", sample)
		}
	}
	if len(gotUp) != 2 || gotUp[0] != 1 || gotUp[1] != 5 {
		t.Fatalf("traffic samples = %v", gotUp)
	}
	if streamErr, ok := <-errs; ok {
		t.Fatalf("unexpected stream error: %v", streamErr)
	}
}

func TestWatchTrafficCancellationIsSilent(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		flusher, ok := writer.(http.Flusher)
		if !ok {
			t.Error("response writer cannot flush")
			return
		}
		_, _ = writer.Write([]byte("{\"up\":1,\"down\":2}\n"))
		flusher.Flush()
		close(started)
		<-request.Context().Done()
	}))
	defer server.Close()

	api, err := mihomo.New(server.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	application := &App{api: api, euid: func() int { return 1000 }}
	ctx, cancel := context.WithCancel(context.Background())
	samples, errs := application.WatchTraffic(ctx)
	select {
	case <-samples:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for traffic sample")
	}
	<-started
	cancel()

	for range samples {
	}
	if streamErr, ok := <-errs; ok {
		t.Fatalf("cancellation reported as stream error: %v", streamErr)
	}
}

func TestWatchTrafficReportsUnavailableController(t *testing.T) {
	t.Parallel()
	application := &App{euid: func() int { return 1000 }}
	samples, errs := application.WatchTraffic(context.Background())
	if _, ok := <-samples; ok {
		t.Fatal("uninitialized traffic stream remained open")
	}
	streamErr, ok := <-errs
	if !ok || !errors.Is(streamErr, ErrNotInitialized) {
		t.Fatalf("stream error = %T %v", streamErr, streamErr)
	}
	if _, ok := <-errs; ok {
		t.Fatal("uninitialized error channel remained open")
	}
}

func TestWatchTrafficWrapsStreamFailure(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("{not-json}\n"))
	}))
	defer server.Close()

	api, err := mihomo.New(server.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	application := &App{api: api, euid: func() int { return 1000 }}
	samples, errs := application.WatchTraffic(context.Background())
	for range samples {
	}
	streamErr, ok := <-errs
	if !ok || !strings.Contains(streamErr.Error(), "流量流已断开") {
		t.Fatalf("stream error = %T %v", streamErr, streamErr)
	}
	var unavailable *UnavailableError
	if !errors.As(streamErr, &unavailable) {
		t.Fatalf("stream error = %T, want UnavailableError", streamErr)
	}
}

func TestWatchTrafficTimesOutWhenStreamHasNoSamples(t *testing.T) {
	original := trafficNoSampleTimeout
	trafficNoSampleTimeout = 40 * time.Millisecond
	t.Cleanup(func() { trafficNoSampleTimeout = original })
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)
		writer.(http.Flusher).Flush()
		<-request.Context().Done()
	}))
	defer server.Close()
	api, err := mihomo.New(server.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	application := &App{api: api, euid: func() int { return 1000 }}
	samples, errs := application.WatchTraffic(context.Background())
	for range samples {
	}
	streamErr := <-errs
	if streamErr == nil || !strings.Contains(streamErr.Error(), "没有返回数据") {
		t.Fatalf("no-sample stream error = %v", streamErr)
	}
}

func TestWatchTrafficWatchdogUnblocksFullConsumerBuffer(t *testing.T) {
	original := trafficNoSampleTimeout
	trafficNoSampleTimeout = 40 * time.Millisecond
	t.Cleanup(func() { trafficNoSampleTimeout = original })
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		for index := 0; index < 32; index++ {
			_, _ = fmt.Fprintf(writer, "{\"up\":%d,\"down\":%d}\n", index, index)
		}
		writer.(http.Flusher).Flush()
		<-request.Context().Done()
	}))
	defer server.Close()
	api, err := mihomo.New(server.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	application := &App{api: api, euid: func() int { return 1000 }}
	_, errs := application.WatchTraffic(context.Background())
	select {
	case streamErr := <-errs:
		if streamErr == nil || !strings.Contains(streamErr.Error(), "没有返回数据") {
			t.Fatalf("full-buffer stream error = %v", streamErr)
		}
	case <-time.After(time.Second):
		t.Fatal("full consumer buffer prevented watchdog cancellation")
	}
}
