package mihomo

import (
	"context"
	"net/http"
	"net/url"

	"mihomoctl/internal/domain"
)

type trafficWire struct {
	Up        int64 `json:"up"`
	Down      int64 `json:"down"`
	UpTotal   int64 `json:"upTotal"`
	DownTotal int64 `json:"downTotal"`
}

func mapTraffic(value trafficWire) (domain.Traffic, error) {
	return domain.Traffic{
		Up:        value.Up,
		Down:      value.Down,
		UpTotal:   value.UpTotal,
		DownTotal: value.DownTotal,
	}, nil
}

// Memory is a Mihomo memory usage sample in bytes.
type Memory struct {
	InUse   int64 `json:"inuse"`
	OSLimit int64 `json:"oslimit"`
}

type logWire struct {
	Time    string `json:"time"`
	Type    string `json:"type"`
	Payload string `json:"payload"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

func mapLog(value logWire) (domain.LogEntry, error) {
	level := value.Level
	if level == "" {
		level = value.Type
	}
	message := value.Message
	if message == "" {
		message = value.Payload
	}
	return domain.LogEntry{Time: value.Time, Level: level, Message: message}, nil
}

func (c *Client) Traffic(ctx context.Context) (domain.Traffic, error) {
	return decodeOne[trafficWire](ctx, c, http.MethodGet, []string{"traffic"}, url.Values{}, nil, mapTraffic)
}

func (c *Client) StreamTraffic(ctx context.Context, consume func(domain.Traffic) error) error {
	return decodeStream[trafficWire](ctx, c, []string{"traffic"}, url.Values{}, mapTraffic, consume)
}

func (c *Client) Memory(ctx context.Context) (Memory, error) {
	return decodeOne[Memory](ctx, c, http.MethodGet, []string{"memory"}, url.Values{}, nil,
		func(value Memory) (Memory, error) { return value, nil })
}

func (c *Client) StreamMemory(ctx context.Context, consume func(Memory) error) error {
	return decodeStream[Memory](ctx, c, []string{"memory"}, url.Values{},
		func(value Memory) (Memory, error) { return value, nil }, consume)
}

func logQuery(level string) url.Values {
	query := url.Values{}
	if level != "" {
		query.Set("level", level)
	}
	return query
}

// Logs returns the next log entry from Mihomo's streaming /logs endpoint.
func (c *Client) Logs(ctx context.Context, level string) (domain.LogEntry, error) {
	return decodeOne[logWire](ctx, c, http.MethodGet, []string{"logs"}, logQuery(level), nil, mapLog)
}

// Log is an alias for Logs.
func (c *Client) Log(ctx context.Context, level string) (domain.LogEntry, error) {
	return c.Logs(ctx, level)
}

func (c *Client) StreamLogs(ctx context.Context, level string, consume func(domain.LogEntry) error) error {
	return decodeStream[logWire](ctx, c, []string{"logs"}, logQuery(level), mapLog, consume)
}
