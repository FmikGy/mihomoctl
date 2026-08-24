package mihomo

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"mihomoctl/internal/domain"
)

// ConnectionSnapshot is a point-in-time response from /connections.
type ConnectionSnapshot struct {
	DownloadTotal int64               `json:"download_total"`
	UploadTotal   int64               `json:"upload_total"`
	Memory        int64               `json:"memory"`
	Connections   []domain.Connection `json:"connections"`
}

type connectionResponse struct {
	DownloadTotal int64            `json:"downloadTotal"`
	UploadTotal   int64            `json:"uploadTotal"`
	Memory        int64            `json:"memory"`
	Connections   []connectionWire `json:"connections"`
}

type connectionWire struct {
	ID          string       `json:"id"`
	Metadata    metadataWire `json:"metadata"`
	Upload      int64        `json:"upload"`
	Download    int64        `json:"download"`
	Start       string       `json:"start"`
	Chains      []string     `json:"chains"`
	Rule        string       `json:"rule"`
	RulePayload string       `json:"rulePayload"`
}

type metadataWire struct {
	Network         string       `json:"network"`
	Type            string       `json:"type"`
	SourceIP        string       `json:"sourceIP"`
	DestinationIP   string       `json:"destinationIP"`
	SourcePort      stringNumber `json:"sourcePort"`
	DestinationPort stringNumber `json:"destinationPort"`
	Host            string       `json:"host"`
	Process         string       `json:"process"`
	ProcessPath     string       `json:"processPath"`
}

type stringNumber string

func (value *stringNumber) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*value = ""
		return nil
	}
	var text string
	if len(data) > 0 && data[0] == '"' {
		if err := json.Unmarshal(data, &text); err != nil {
			return err
		}
		*value = stringNumber(text)
		return nil
	}
	var number json.Number
	if err := json.Unmarshal(data, &number); err != nil {
		return err
	}
	*value = stringNumber(number.String())
	return nil
}

func mapConnections(value connectionResponse) (ConnectionSnapshot, error) {
	connections := make([]domain.Connection, 0, len(value.Connections))
	for _, item := range value.Connections {
		start, _ := time.Parse(time.RFC3339Nano, item.Start)
		process := item.Metadata.Process
		if process == "" {
			process = item.Metadata.ProcessPath
		}
		connections = append(connections, domain.Connection{
			ID:          item.ID,
			Network:     item.Metadata.Network,
			Type:        item.Metadata.Type,
			Source:      joinEndpoint(item.Metadata.SourceIP, string(item.Metadata.SourcePort)),
			Destination: joinEndpoint(item.Metadata.DestinationIP, string(item.Metadata.DestinationPort)),
			Host:        item.Metadata.Host,
			Process:     process,
			Rule:        item.Rule,
			RulePayload: item.RulePayload,
			Chains:      append([]string(nil), item.Chains...),
			Upload:      item.Upload,
			Download:    item.Download,
			Start:       start,
		})
	}
	return ConnectionSnapshot{
		DownloadTotal: value.DownloadTotal,
		UploadTotal:   value.UploadTotal,
		Memory:        value.Memory,
		Connections:   connections,
	}, nil
}

func joinEndpoint(host, port string) string {
	if host == "" {
		return port
	}
	if port == "" {
		return host
	}
	return net.JoinHostPort(host, port)
}

// Connections returns the complete response, including aggregate counters.
func (c *Client) Connections(ctx context.Context) (ConnectionSnapshot, error) {
	return decodeOne[connectionResponse](ctx, c, http.MethodGet, []string{"connections"}, url.Values{}, nil, mapConnections)
}

// ListConnections returns only active connection rows.
func (c *Client) ListConnections(ctx context.Context) ([]domain.Connection, error) {
	snapshot, err := c.Connections(ctx)
	if err != nil {
		return nil, err
	}
	return snapshot.Connections, nil
}

func (c *Client) StreamConnections(
	ctx context.Context,
	interval time.Duration,
	consume func(ConnectionSnapshot) error,
) error {
	if interval < 0 {
		return errors.New("连接刷新间隔不能为负数")
	}
	query := url.Values{}
	if interval > 0 {
		milliseconds := (interval + time.Millisecond - 1) / time.Millisecond
		query.Set("interval", strconv.FormatInt(int64(milliseconds), 10))
	}
	return decodeStream[connectionResponse](ctx, c, []string{"connections"}, query, mapConnections, consume)
}

func (c *Client) CloseConnection(ctx context.Context, id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("连接 ID 不能为空")
	}
	return doNoContent(ctx, c, http.MethodDelete, []string{"connections", id}, url.Values{}, nil)
}

func (c *Client) CloseAllConnections(ctx context.Context) error {
	return doNoContent(ctx, c, http.MethodDelete, []string{"connections"}, url.Values{}, nil)
}

// CloseAll is a concise alias for CloseAllConnections.
func (c *Client) CloseAll(ctx context.Context) error {
	return c.CloseAllConnections(ctx)
}
