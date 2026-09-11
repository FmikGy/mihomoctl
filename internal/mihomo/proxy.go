package mihomo

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"mihomoctl/internal/domain"
	"mihomoctl/internal/i18n"
)

const defaultDelayTimeout = 5 * time.Second

type proxyResponse struct {
	Proxies map[string]proxyPayload `json:"proxies"`
}

type proxyPayload struct {
	Name         string             `json:"name"`
	Type         string             `json:"type"`
	Alive        bool               `json:"alive"`
	UDP          bool               `json:"udp"`
	ProviderName string             `json:"provider-name"`
	History      []delayHistoryWire `json:"history"`
	Now          string             `json:"now"`
	All          []string           `json:"all"`
	Hidden       bool               `json:"hidden"`
	TestURL      string             `json:"testUrl"`
}

type delayHistoryWire struct {
	Time  string `json:"time"`
	Delay uint16 `json:"delay"`
}

type proxySnapshot struct {
	proxies map[string]domain.Proxy
	groups  map[string]domain.ProxyGroup
}

func (c *Client) proxySnapshot(ctx context.Context) (proxySnapshot, error) {
	return decodeOne[proxyResponse](ctx, c, http.MethodGet, []string{"proxies"}, url.Values{}, nil, mapProxyResponse)
}

func mapProxyResponse(response proxyResponse) (proxySnapshot, error) {
	proxies := make(map[string]domain.Proxy, len(response.Proxies))
	for key, payload := range response.Proxies {
		name := payload.Name
		if name == "" {
			name = key
		}
		history := make([]domain.DelaySample, 0, len(payload.History))
		for _, sample := range payload.History {
			parsedTime, _ := time.Parse(time.RFC3339Nano, sample.Time)
			history = append(history, domain.DelaySample{Time: parsedTime, Delay: sample.Delay})
		}
		delay := uint16(0)
		if len(history) != 0 {
			delay = history[len(history)-1].Delay
		}
		proxies[key] = domain.Proxy{
			Name:         name,
			Type:         payload.Type,
			Alive:        payload.Alive,
			UDP:          payload.UDP,
			ProviderName: payload.ProviderName,
			Delay:        delay,
			History:      history,
		}
	}

	groups := make(map[string]domain.ProxyGroup)
	for key, payload := range response.Proxies {
		if payload.All == nil && !isGroupType(payload.Type) {
			continue
		}
		name := payload.Name
		if name == "" {
			name = key
		}
		group := domain.ProxyGroup{
			Name:    name,
			Type:    payload.Type,
			Now:     payload.Now,
			All:     append([]string(nil), payload.All...),
			Hidden:  payload.Hidden,
			TestURL: payload.TestURL,
			Proxies: make([]domain.Proxy, 0, len(payload.All)),
		}
		for _, member := range payload.All {
			if proxy, ok := proxies[member]; ok {
				group.Proxies = append(group.Proxies, proxy)
			} else {
				group.Proxies = append(group.Proxies, domain.Proxy{Name: member})
			}
		}
		groups[key] = group
	}
	return proxySnapshot{proxies: proxies, groups: groups}, nil
}

func isGroupType(proxyType string) bool {
	switch strings.ToLower(proxyType) {
	case "selector", "urltest", "url-test", "fallback", "loadbalance", "load-balance", "relay":
		return true
	default:
		return false
	}
}

// Proxies returns all proxy and group entries keyed exactly as /proxies reports
// them. Use Groups for the richer policy-group representation.
func (c *Client) Proxies(ctx context.Context) (map[string]domain.Proxy, error) {
	snapshot, err := c.proxySnapshot(ctx)
	if err != nil {
		return nil, err
	}
	return snapshot.proxies, nil
}

// Groups returns policy groups and fills their Proxies from the same /proxies
// response, preserving the order in each group's All field.
func (c *Client) Groups(ctx context.Context) (map[string]domain.ProxyGroup, error) {
	snapshot, err := c.proxySnapshot(ctx)
	if err != nil {
		return nil, err
	}
	return snapshot.groups, nil
}

func (c *Client) SelectProxy(ctx context.Context, group, proxy string) error {
	if group == "" || proxy == "" {
		return i18n.Errorf("策略组和代理名称不能为空")
	}
	return doNoContent(ctx, c, http.MethodPut, []string{"proxies", group}, url.Values{}, map[string]string{"name": proxy})
}

func delayQuery(testURL string, timeout time.Duration) (url.Values, error) {
	if timeout < 0 {
		return nil, i18n.Errorf("测速超时不能为负数")
	}
	if timeout == 0 {
		timeout = defaultDelayTimeout
	}
	milliseconds := int64(math.Ceil(float64(timeout) / float64(time.Millisecond)))
	if milliseconds < 1 {
		milliseconds = 1
	}
	query := url.Values{"timeout": []string{fmt.Sprintf("%d", milliseconds)}}
	if testURL != "" {
		query.Set("url", testURL)
	}
	return query, nil
}

func (c *Client) TestProxy(ctx context.Context, name, testURL string, timeout time.Duration) (uint16, error) {
	if name == "" {
		return 0, i18n.Errorf("代理名称不能为空")
	}
	query, err := delayQuery(testURL, timeout)
	if err != nil {
		return 0, err
	}
	type delayResponse struct {
		Delay uint16 `json:"delay"`
	}
	result, err := decodeOne[delayResponse](ctx, c, http.MethodGet, []string{"proxies", name, "delay"}, query, nil,
		func(value delayResponse) (delayResponse, error) { return value, nil })
	return result.Delay, err
}

func (c *Client) TestGroup(ctx context.Context, name, testURL string, timeout time.Duration) (map[string]uint16, error) {
	if name == "" {
		return nil, i18n.Errorf("策略组名称不能为空")
	}
	query, err := delayQuery(testURL, timeout)
	if err != nil {
		return nil, err
	}
	return decodeOne[map[string]uint16](ctx, c, http.MethodGet, []string{"group", name, "delay"}, query, nil,
		func(value map[string]uint16) (map[string]uint16, error) { return value, nil })
}
