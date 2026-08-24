package mihomo

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"mihomoctl/internal/domain"
)

func TestOneShotTelemetryEndpoints(t *testing.T) {
	t.Parallel()
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/traffic":
			_, _ = w.Write([]byte(`{"up":10,"down":20,"upTotal":30,"downTotal":40}`))
		case "/api/memory":
			_, _ = w.Write([]byte(`{"inuse":1024,"oslimit":0}`))
		case "/api/logs":
			if r.URL.Query().Get("level") != "warning" {
				t.Errorf("log level query = %q", r.URL.Query().Get("level"))
			}
			_, _ = w.Write([]byte(`{"type":"warning","payload":"test log"}`))
		default:
			http.NotFound(w, r)
		}
	})

	traffic, err := client.Traffic(context.Background())
	if err != nil || traffic.Up != 10 || traffic.DownTotal != 40 {
		t.Fatalf("Traffic() = %#v, %v", traffic, err)
	}
	memory, err := client.Memory(context.Background())
	if err != nil || memory.InUse != 1024 {
		t.Fatalf("Memory() = %#v, %v", memory, err)
	}
	entry, err := client.Logs(context.Background(), "warning")
	if err != nil || entry.Level != "warning" || entry.Message != "test log" {
		t.Fatalf("Logs() = %#v, %v", entry, err)
	}
}

func TestStreamingTelemetryDecodesJSONSequence(t *testing.T) {
	t.Parallel()
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/traffic":
			_, _ = w.Write([]byte("{\"up\":1,\"down\":2}\n{\"up\":3,\"down\":4}\n"))
		case "/api/memory":
			_, _ = w.Write([]byte("{\"inuse\":10}\n{\"inuse\":20}\n"))
		case "/api/logs":
			_, _ = w.Write([]byte("{\"type\":\"info\",\"payload\":\"plain\"}\n{\"time\":\"12:00:00\",\"level\":\"error\",\"message\":\"structured\"}\n"))
		default:
			http.NotFound(w, r)
		}
	})

	var traffic []domain.Traffic
	if err := client.StreamTraffic(context.Background(), func(value domain.Traffic) error {
		traffic = append(traffic, value)
		return nil
	}); err != nil {
		t.Fatalf("StreamTraffic() error = %v", err)
	}
	if len(traffic) != 2 || traffic[1].Up != 3 {
		t.Fatalf("traffic = %#v", traffic)
	}

	var memory []Memory
	if err := client.StreamMemory(context.Background(), func(value Memory) error {
		memory = append(memory, value)
		return nil
	}); err != nil {
		t.Fatalf("StreamMemory() error = %v", err)
	}
	if len(memory) != 2 || memory[1].InUse != 20 {
		t.Fatalf("memory = %#v", memory)
	}

	var logs []domain.LogEntry
	if err := client.StreamLogs(context.Background(), "", func(value domain.LogEntry) error {
		logs = append(logs, value)
		return nil
	}); err != nil {
		t.Fatalf("StreamLogs() error = %v", err)
	}
	if len(logs) != 2 || logs[0].Message != "plain" || logs[1].Time != "12:00:00" {
		t.Fatalf("logs = %#v", logs)
	}
}

func TestStreamHonorsContextCancellation(t *testing.T) {
	t.Parallel()
	wrote := make(chan struct{})
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("response writer is not a flusher")
			return
		}
		_, _ = w.Write([]byte("{\"up\":1}\n"))
		flusher.Flush()
		close(wrote)
		<-r.Context().Done()
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := client.StreamTraffic(ctx, func(domain.Traffic) error {
		<-wrote
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("StreamTraffic() cancel error = %v", err)
	}
}

func TestStreamCallbackAndMalformedJSONErrors(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("stop")
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("level") == "bad" {
			_, _ = w.Write([]byte("{not-json}\n"))
			return
		}
		_, _ = w.Write([]byte("{\"type\":\"info\",\"payload\":\"x\"}\n"))
	})
	if err := client.StreamLogs(context.Background(), "", func(domain.LogEntry) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("callback error = %v", err)
	}
	if err := client.StreamLogs(context.Background(), "bad", func(domain.LogEntry) error { return nil }); err == nil {
		t.Fatal("malformed stream succeeded")
	}
	if err := client.StreamTraffic(context.Background(), nil); err == nil {
		t.Fatal("nil callback succeeded")
	}
}
