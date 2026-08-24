package mihomo

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"
)

const connectionJSON = `{
  "downloadTotal": 300,
  "uploadTotal": 200,
  "memory": 100,
  "connections": [{
    "id": "id /?",
    "metadata": {
      "network": "tcp",
      "type": "Inner",
      "sourceIP": "2001:db8::1",
      "sourcePort": "1234",
      "destinationIP": "1.1.1.1",
      "destinationPort": 443,
      "host": "example.com",
      "processPath": "/usr/bin/curl"
    },
    "upload": 20,
    "download": 30,
    "start": "2026-08-23T10:11:12.123Z",
    "chains": ["Node A", "GLOBAL"],
    "rule": "MATCH",
    "rulePayload": ""
  }]
}`

func TestConnectionsMappingAndCloseEndpoints(t *testing.T) {
	t.Parallel()
	closed := make(chan string, 2)
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(connectionJSON))
			return
		}
		closed <- r.Method + " " + r.URL.EscapedPath()
		w.WriteHeader(http.StatusNoContent)
	})

	snapshot, err := client.Connections(context.Background())
	if err != nil {
		t.Fatalf("Connections() error = %v", err)
	}
	if snapshot.DownloadTotal != 300 || snapshot.UploadTotal != 200 || snapshot.Memory != 100 || len(snapshot.Connections) != 1 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	connection := snapshot.Connections[0]
	if connection.Source != "[2001:db8::1]:1234" || connection.Destination != "1.1.1.1:443" || connection.Process != "/usr/bin/curl" || connection.Start.IsZero() {
		t.Fatalf("connection = %#v", connection)
	}

	list, err := client.ListConnections(context.Background())
	if err != nil || len(list) != 1 {
		t.Fatalf("ListConnections() = %#v, %v", list, err)
	}
	if err := client.CloseConnection(context.Background(), "id /?"); err != nil {
		t.Fatalf("CloseConnection() error = %v", err)
	}
	if err := client.CloseAllConnections(context.Background()); err != nil {
		t.Fatalf("CloseAllConnections() error = %v", err)
	}
	want := []string{
		"DELETE /api/connections/" + url.PathEscape("id /?"),
		"DELETE /api/connections",
	}
	for i := range want {
		if got := <-closed; got != want[i] {
			t.Errorf("close request %d = %q, want %q", i, got, want[i])
		}
	}
}

func TestStreamConnections(t *testing.T) {
	t.Parallel()
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Query().Get("interval"), "2"; got != want {
			t.Errorf("interval = %q, want %q", got, want)
		}
		_, _ = w.Write([]byte(connectionJSON + "\n" + connectionJSON + "\n"))
	})

	count := 0
	err := client.StreamConnections(context.Background(), 1501*time.Microsecond, func(value ConnectionSnapshot) error {
		count++
		if len(value.Connections) != 1 {
			t.Errorf("connections = %#v", value.Connections)
		}
		return nil
	})
	if err != nil || count != 2 {
		t.Fatalf("StreamConnections() count = %d, error = %v", count, err)
	}
}
