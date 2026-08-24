package mihomo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"testing"
	"time"
)

func TestGroupsMapMembersFromProxyResponse(t *testing.T) {
	t.Parallel()
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/proxies" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{
            "proxies": {
              "Node A": {"name":"Node A","type":"VMess","alive":true,"udp":true,"provider-name":"remote","history":[{"time":"2026-08-23T10:11:12.123Z","delay":42}]},
              "AUTO": {"name":"AUTO","type":"URLTest","alive":true,"now":"Node A","all":["Node A","Missing"],"hidden":true,"testUrl":"https://cp.cloudflare.com/generate_204"}
            }
          }`))
	})

	groups, err := client.Groups(context.Background())
	if err != nil {
		t.Fatalf("Groups() error = %v", err)
	}
	group := groups["AUTO"]
	if group.Name != "AUTO" || group.Now != "Node A" || !group.Hidden || len(group.Proxies) != 2 {
		t.Fatalf("group = %#v", group)
	}
	if got := group.Proxies[0]; got.Name != "Node A" || got.Type != "VMess" || !got.Alive || got.Delay != 42 || got.ProviderName != "remote" {
		t.Fatalf("mapped member = %#v", got)
	}
	if got := group.Proxies[1]; got.Name != "Missing" {
		t.Fatalf("missing placeholder = %#v", got)
	}
	if group.Proxies[0].History[0].Time.IsZero() {
		t.Fatal("history time was not parsed")
	}

	proxies, err := client.Proxies(context.Background())
	if err != nil {
		t.Fatalf("Proxies() error = %v", err)
	}
	if proxies["Node A"].Delay != 42 || proxies["AUTO"].Type != "URLTest" {
		t.Fatalf("proxies = %#v", proxies)
	}
}

func TestProxyCommandsEscapePathAndEncodeQueries(t *testing.T) {
	t.Parallel()
	groupName := "策略 /组?"
	proxyName := "节点 /A?"
	requests := make(chan string, 3)
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Method + " " + r.URL.EscapedPath()
		switch r.Method {
		case http.MethodPut:
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["name"] != proxyName {
				t.Errorf("select body = %#v, error = %v", body, err)
			}
			w.WriteHeader(http.StatusNoContent)
		case http.MethodGet:
			if got, want := r.URL.Query().Get("timeout"), "2"; got != want {
				t.Errorf("timeout = %q, want %q", got, want)
			}
			if got, want := r.URL.Query().Get("url"), "https://example.com/a?x=1&y=2"; got != want {
				t.Errorf("url = %q, want %q", got, want)
			}
			if r.URL.Path[:len("/api/group")] == "/api/group" {
				_, _ = w.Write([]byte(`{"节点 /A?":23,"zero-ms":0}`))
			} else {
				_, _ = w.Write([]byte(`{"delay":17}`))
			}
		}
	})

	if err := client.SelectProxy(context.Background(), groupName, proxyName); err != nil {
		t.Fatalf("SelectProxy() error = %v", err)
	}
	const testURL = "https://example.com/a?x=1&y=2"
	delay, err := client.TestProxy(context.Background(), proxyName, testURL, 1501*time.Microsecond)
	if err != nil || delay != 17 {
		t.Fatalf("TestProxy() = %d, %v", delay, err)
	}
	delays, err := client.TestGroup(context.Background(), groupName, testURL, 1501*time.Microsecond)
	if err != nil || !reflect.DeepEqual(delays, map[string]uint16{"节点 /A?": 23, "zero-ms": 0}) {
		t.Fatalf("TestGroup() = %#v, %v", delays, err)
	}

	want := []string{
		"PUT /api/proxies/" + url.PathEscape(groupName),
		"GET /api/proxies/" + url.PathEscape(proxyName) + "/delay",
		"GET /api/group/" + url.PathEscape(groupName) + "/delay",
	}
	for i := range want {
		if got := <-requests; got != want[i] {
			t.Errorf("request %d = %q, want %q", i, got, want[i])
		}
	}
}
