package mihomo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"mihomoctl/internal/i18n"
)

const (
	defaultRequestTimeout        = 10 * time.Second
	defaultResponseHeaderTimeout = 10 * time.Second
	maxErrorBody                 = 64 << 10
	maxErrorMessageRunes         = 512
	authErrorMessage             = "认证失败"
	redactedCredential           = "[redacted]"
)

// Client is a client for Mihomo's external-controller API.
type Client struct {
	baseURL        *url.URL
	secret         string
	httpClient     *http.Client
	requestTimeout time.Duration
}

// Option customizes a Client.
type Option func(*Client)

// WithHTTPClient replaces the HTTP client used for requests. Its Timeout should
// normally remain zero because Mihomo's streaming endpoints are long-lived.
func WithHTTPClient(client *http.Client) Option {
	return func(c *Client) {
		c.httpClient = client
	}
}

// WithRequestTimeout sets the timeout for non-streaming requests. A value of
// zero disables the implicit timeout. Streaming calls are controlled only by
// the context passed to them.
func WithRequestTimeout(timeout time.Duration) Option {
	return func(c *Client) {
		c.requestTimeout = timeout
	}
}

// NewClient creates a Mihomo API client. controller may be either a full HTTP(S)
// URL or a host:port pair such as 127.0.0.1:9090.
func NewClient(controller, secret string, options ...Option) (*Client, error) {
	controller = strings.TrimSpace(controller)
	if controller == "" {
		return nil, i18n.Errorf("Mihomo 控制器地址不能为空")
	}
	if !strings.Contains(controller, "://") {
		controller = "http://" + controller
	}

	baseURL, err := url.Parse(controller)
	if err != nil {
		return nil, i18n.Errorf("Mihomo 控制器地址无效: %w", err)
	}
	if (baseURL.Scheme != "http" && baseURL.Scheme != "https") || baseURL.Host == "" {
		return nil, i18n.Errorf("Mihomo 控制器地址必须是有效的 HTTP 或 HTTPS 地址")
	}
	if baseURL.User != nil || baseURL.RawQuery != "" || baseURL.Fragment != "" {
		return nil, i18n.Errorf("Mihomo 控制器地址不能包含用户信息、查询参数或片段")
	}
	baseURL.Path = strings.TrimRight(baseURL.Path, "/")
	baseURL.RawPath = strings.TrimRight(baseURL.EscapedPath(), "/")

	transport := clientTransport(http.DefaultTransport)
	client := &Client{
		baseURL:        baseURL,
		secret:         secret,
		httpClient:     &http.Client{Transport: transport},
		requestTimeout: defaultRequestTimeout,
	}
	for _, option := range options {
		if option != nil {
			option(client)
		}
	}
	if client.httpClient == nil {
		return nil, i18n.Errorf("Mihomo HTTP 客户端不能为空")
	}
	if client.requestTimeout < 0 {
		return nil, i18n.Errorf("Mihomo 请求超时不能为负数")
	}
	return client, nil
}

func clientTransport(base http.RoundTripper) http.RoundTripper {
	if transport, ok := base.(*http.Transport); ok && transport != nil {
		clone := transport.Clone()
		clone.ResponseHeaderTimeout = defaultResponseHeaderTimeout
		return clone
	}
	if base != nil {
		return base
	}
	return &http.Transport{Proxy: http.ProxyFromEnvironment, ResponseHeaderTimeout: defaultResponseHeaderTimeout}
}

// New is a short alias for NewClient.
func New(controller, secret string, options ...Option) (*Client, error) {
	return NewClient(controller, secret, options...)
}

// APIError describes a non-2xx response from Mihomo.
type APIError struct {
	Method     string
	Path       string
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("Mihomo API 请求失败（HTTP %d）", e.StatusCode)
	}
	return fmt.Sprintf("Mihomo API 请求失败（HTTP %d）：%s", e.StatusCode, e.Message)
}

func (e *APIError) Localized(language i18n.Language) string {
	if e.Message == "" {
		return i18n.T(language, "Mihomo API 请求失败（HTTP %d）", e.StatusCode)
	}
	message := e.Message
	if message == authErrorMessage {
		message = i18n.T(language, authErrorMessage)
	}
	return i18n.T(language, "Mihomo API 请求失败（HTTP %d）：%s", e.StatusCode, message)
}

func (c *Client) endpoint(parts ...string) *url.URL {
	u := *c.baseURL
	decodedPath := strings.TrimRight(u.Path, "/")
	escapedPath := strings.TrimRight(u.EscapedPath(), "/")
	for _, part := range parts {
		decodedPath += "/" + part
		escapedPath += "/" + url.PathEscape(part)
	}
	u.Path = decodedPath
	u.RawPath = escapedPath
	u.RawQuery = ""
	u.Fragment = ""
	return &u
}

func (c *Client) request(
	ctx context.Context,
	method string,
	parts []string,
	query url.Values,
	body any,
	stream bool,
) (*http.Response, context.CancelFunc, error) {
	if ctx == nil {
		return nil, nil, i18n.Errorf("Mihomo API 请求 context 不能为空")
	}

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, nil, i18n.Errorf("Mihomo API 请求数据编码失败: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}

	requestCtx := ctx
	cancel := func() {}
	if !stream && c.requestTimeout > 0 {
		requestCtx, cancel = context.WithTimeout(ctx, c.requestTimeout)
	}

	endpoint := c.endpoint(parts...)
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(requestCtx, method, endpoint.String(), reader)
	if err != nil {
		cancel()
		return nil, nil, i18n.Errorf("Mihomo API 请求创建失败: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.secret != "" {
		req.Header.Set("Authorization", "Bearer "+c.secret)
	}
	req.Header.Set("User-Agent", "mihomoctl")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		requestErr := requestCtx.Err()
		cancel()
		if requestErr != nil {
			return nil, nil, i18n.Errorf("Mihomo API 请求已取消或超时: %w", requestErr)
		}
		return nil, nil, i18n.Errorf("无法连接 Mihomo API: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		apiErr := readAPIError(req, resp)
		resp.Body.Close()
		cancel()
		return nil, nil, apiErr
	}
	return resp, cancel, nil
}

func readAPIError(req *http.Request, resp *http.Response) error {
	message := authErrorMessage
	// Authentication failures are untrusted and may reflect Authorization.
	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		message = ""
		var payload struct {
			Message string `json:"message"`
			Error   string `json:"error"`
		}
		if json.Unmarshal(data, &payload) == nil {
			message = strings.TrimSpace(payload.Message)
			if message == "" {
				message = strings.TrimSpace(payload.Error)
			}
		}
		if message == "" {
			message = strings.TrimSpace(string(data))
		}
		if message == "" {
			message = http.StatusText(resp.StatusCode)
		}
		message = sanitizeAPIErrorMessage(req, message)
	}
	return &APIError{
		Method:     req.Method,
		Path:       req.URL.EscapedPath(),
		StatusCode: resp.StatusCode,
		Message:    message,
	}
}

func sanitizeAPIErrorMessage(req *http.Request, message string) string {
	if req != nil {
		authorization := req.Header.Get("Authorization")
		scheme, credential, found := strings.Cut(authorization, " ")
		if found && strings.EqualFold(scheme, "Bearer") && credential != "" {
			message = strings.NewReplacer(
				authorization, redactedCredential,
				credential, redactedCredential,
			).Replace(message)
		}
	}
	runes := []rune(message)
	if len(runes) > maxErrorMessageRunes {
		message = string(runes[:maxErrorMessageRunes])
	}
	return message
}

func decodeOne[Wire any, Result any](
	ctx context.Context,
	c *Client,
	method string,
	parts []string,
	query url.Values,
	body any,
	convert func(Wire) (Result, error),
) (Result, error) {
	var zero Result
	resp, cancel, err := c.request(ctx, method, parts, query, body, false)
	if err != nil {
		return zero, err
	}
	defer cancel()
	defer resp.Body.Close()

	var wire Wire
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		if resp.Request.Context().Err() != nil {
			return zero, i18n.Errorf("Mihomo API 请求已取消或超时: %w", resp.Request.Context().Err())
		}
		return zero, i18n.Errorf("Mihomo API 响应解析失败: %w", err)
	}
	result, err := convert(wire)
	if err != nil {
		return zero, i18n.Errorf("Mihomo API 响应无效: %w", err)
	}
	return result, nil
}

func doNoContent(
	ctx context.Context,
	c *Client,
	method string,
	parts []string,
	query url.Values,
	body any,
) error {
	resp, cancel, err := c.request(ctx, method, parts, query, body, false)
	if err != nil {
		return err
	}
	defer cancel()
	defer resp.Body.Close()
	_, err = io.Copy(io.Discard, resp.Body)
	if err != nil {
		if resp.Request.Context().Err() != nil {
			return i18n.Errorf("Mihomo API 请求已取消或超时: %w", resp.Request.Context().Err())
		}
		return i18n.Errorf("Mihomo API 响应读取失败: %w", err)
	}
	return nil
}

func decodeStream[Wire any, Result any](
	ctx context.Context,
	c *Client,
	parts []string,
	query url.Values,
	convert func(Wire) (Result, error),
	consume func(Result) error,
) error {
	if consume == nil {
		return i18n.Errorf("Mihomo 流式响应回调不能为空")
	}
	resp, cancel, err := c.request(ctx, http.MethodGet, parts, query, nil, true)
	if err != nil {
		return err
	}
	defer cancel()
	defer resp.Body.Close()

	decoder := json.NewDecoder(resp.Body)
	for {
		var wire Wire
		err := decoder.Decode(&wire)
		if err != nil {
			if resp.Request.Context().Err() != nil {
				return i18n.Errorf("Mihomo API 流已取消: %w", resp.Request.Context().Err())
			}
			if errors.Is(err, io.EOF) {
				return nil
			}
			return i18n.Errorf("Mihomo API 流式响应解析失败: %w", err)
		}
		result, err := convert(wire)
		if err != nil {
			return i18n.Errorf("Mihomo API 流式响应无效: %w", err)
		}
		if err := consume(result); err != nil {
			return err
		}
	}
}
