package profile

import (
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
