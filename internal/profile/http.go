package profile

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"mihomoctl/internal/domain"
)

type fetchResult struct {
	body         []byte
	notModified  bool
	etag         string
	lastModified string
	subscription domain.SubscriptionInfo
}

func (s *Store) fetchRemote(ctx context.Context, rawURL, etag, lastModified string) (fetchResult, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fetchResult{}, fmt.Errorf("remote source must be an HTTP or HTTPS URL")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fetchResult{}, fmt.Errorf("create subscription request for %s: invalid request", RedactURL(rawURL))
	}
	request.Header.Set("Accept", "application/yaml, text/yaml, text/plain, */*")
	request.Header.Set("User-Agent", "mihomoctl/1")
	if etag != "" {
		request.Header.Set("If-None-Match", etag)
	}
	if lastModified != "" {
		request.Header.Set("If-Modified-Since", lastModified)
	}
	response, err := redirectSafeClient(s.client).Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fetchResult{}, fmt.Errorf("download %s: %w", RedactURL(rawURL), ctxErr)
		}
		// net/http's error commonly embeds request.URL, including credentials
		// and provider tokens. Keep the useful target host but never forward it.
		return fetchResult{}, fmt.Errorf("download %s: HTTP request failed", RedactURL(rawURL))
	}
	defer response.Body.Close()

	result := fetchResult{
		etag:         response.Header.Get("ETag"),
		lastModified: response.Header.Get("Last-Modified"),
	}
	if value := response.Header.Get("Subscription-Userinfo"); value != "" {
		if info, parseErr := ParseSubscriptionUserInfo(value); parseErr == nil {
			result.subscription = info
		}
	}
	if response.StatusCode == http.StatusNotModified {
		result.notModified = true
		return result, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fetchResult{}, fmt.Errorf("download %s: HTTP %s", RedactURL(rawURL), response.Status)
	}
	result.body, err = readLimited(response.Body, s.maxSourceBytes)
	if err != nil {
		return fetchResult{}, fmt.Errorf("download %s: %w", RedactURL(rawURL), err)
	}
	return result, nil
}

// redirectSafeClient returns a shallow copy so redirect policy remains local
// to this request. In particular, WithHTTPClient callers retain ownership of
// their client and its CheckRedirect hook.
func redirectSafeClient(base *http.Client) *http.Client {
	client := *base
	original := base.CheckRedirect
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		request.Header.Del("Referer")
		if original != nil {
			if err := original(request, via); err != nil {
				return err
			}
		} else if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}

		// Go derives Referer from the complete previous URL, including query
		// values commonly used as subscription tokens. Never forward it.
		request.Header.Del("Referer")
		if len(via) == 0 {
			return nil
		}
		previousURL := via[len(via)-1].URL
		initialURL := via[0].URL
		if !sameHTTPOrigin(initialURL, request.URL) {
			// Validators can identify a private subscription just as readily as
			// credentials. Anchor this decision to the initial origin so a later
			// same-origin hop on the redirect target cannot add them back.
			request.Header.Del("If-None-Match")
			request.Header.Del("If-Modified-Since")
		}
		previousScheme := strings.ToLower(previousURL.Scheme)
		nextScheme := strings.ToLower(request.URL.Scheme)
		if nextScheme != "http" && nextScheme != "https" {
			return fmt.Errorf("redirect target must use HTTP or HTTPS")
		}
		if previousScheme == "https" && nextScheme != "https" {
			return fmt.Errorf("refusing HTTPS redirect downgrade")
		}
		return nil
	}
	return &client
}

func sameHTTPOrigin(left, right *url.URL) bool {
	if left == nil || right == nil ||
		!strings.EqualFold(left.Scheme, right.Scheme) ||
		!strings.EqualFold(left.Hostname(), right.Hostname()) {
		return false
	}
	return originPort(left) == originPort(right)
}

func originPort(value *url.URL) string {
	if port := value.Port(); port != "" {
		return port
	}
	switch strings.ToLower(value.Scheme) {
	case "http":
		return "80"
	case "https":
		return "443"
	default:
		return ""
	}
}

func readLimited(reader io.Reader, limit int64) ([]byte, error) {
	content, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > limit {
		return nil, ErrSourceTooLarge
	}
	return content, nil
}

// ParseSubscriptionUserInfo parses the standard provider quota header.
func ParseSubscriptionUserInfo(value string) (domain.SubscriptionInfo, error) {
	var info domain.SubscriptionInfo
	for _, field := range strings.Split(value, ";") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		key, raw, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		raw = strings.TrimSpace(raw)
		if key != "upload" && key != "download" && key != "total" && key != "expire" {
			continue
		}
		number, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || number < 0 {
			return domain.SubscriptionInfo{}, fmt.Errorf("invalid subscription-userinfo %s value %q", key, raw)
		}
		switch key {
		case "upload":
			info.Upload = number
		case "download":
			info.Download = number
		case "total":
			info.Total = number
		case "expire":
			if number > 0 {
				info.Expire = time.Unix(number, 0).UTC()
			}
		}
	}
	return info, nil
}

// RedactURL removes URL credentials, path tokens, query values, and fragments
// while retaining enough host information for useful diagnostics.
func RedactURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "[redacted URL]"
	}
	result := parsed.Scheme + "://" + parsed.Host
	if parsed.Path == "" || parsed.Path == "/" {
		return result + "/"
	}
	return result + "/[redacted]"
}
