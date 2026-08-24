package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/x/ansi"

	"mihomoctl/internal/domain"
)

type envelope struct {
	SchemaVersion int `json:"schema_version"`
	Data          any `json:"data"`
}

func writeJSON(writer io.Writer, data any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(envelope{SchemaVersion: 1, Data: data})
}

func writeTable(writer io.Writer, headers []string, rows [][]string) error {
	columnCount := len(headers)
	for _, row := range rows {
		if len(row) > columnCount {
			columnCount = len(row)
		}
	}
	widths := make([]int, columnCount)
	allRows := make([][]string, 0, len(rows)+1)
	if len(headers) > 0 {
		allRows = append(allRows, headers)
	}
	allRows = append(allRows, rows...)
	for _, row := range allRows {
		for column, cell := range row {
			cell = cleanCell(cell)
			if width := ansi.StringWidth(cell); width > widths[column] {
				widths[column] = width
			}
		}
	}
	for _, row := range allRows {
		for column := range columnCount {
			cell := ""
			if column < len(row) {
				cell = cleanCell(row[column])
			}
			if _, err := io.WriteString(writer, cell); err != nil {
				return err
			}
			if column < columnCount-1 {
				padding := widths[column] - ansi.StringWidth(cell) + 2
				if _, err := io.WriteString(writer, strings.Repeat(" ", padding)); err != nil {
					return err
				}
			}
		}
		if _, err := io.WriteString(writer, "\n"); err != nil {
			return err
		}
	}
	return nil
}

func cleanCell(value string) string {
	value = ansi.Strip(value)
	return strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return ' '
		}
		return character
	}, value)
}

func (a *application) writeResult(data any, message string) error {
	if a.output == "json" {
		return writeJSON(a.stdout, data)
	}
	_, err := fmt.Fprintln(a.stdout, cleanCell(message))
	return err
}

func (a *application) writeStatus(status domain.RuntimeStatus) error {
	if a.output == "json" {
		return writeJSON(a.stdout, status)
	}
	rows := [][]string{
		{"服务", status.Service.State},
		{"运行", yesNo(status.Service.Active)},
		{"开机启动", yesNo(status.Service.Enabled)},
		{"核心版本", empty(status.CoreVersion, "-")},
		{"活动配置", empty(status.ActiveProfile, "-")},
		{"模式", empty(string(status.Mode), "-")},
		{"TUN", onOff(status.TUN)},
		{"Mixed Port", intOrDash(status.MixedPort)},
		{"连接", strconv.Itoa(status.ConnectionCount)},
		{"上传速率", formatBytes(status.Traffic.Up) + "/s"},
		{"下载速率", formatBytes(status.Traffic.Down) + "/s"},
		{"内存", formatBytes(status.Memory)},
	}
	return writeTable(a.stdout, []string{"项目", "状态"}, rows)
}

func (a *application) writeConfig(status domain.RuntimeStatus) error {
	data := struct {
		Mode      domain.Mode `json:"mode"`
		TUN       bool        `json:"tun"`
		MixedPort int         `json:"mixed_port"`
		AllowLAN  bool        `json:"allow_lan"`
		IPv6      bool        `json:"ipv6"`
		LogLevel  string      `json:"log_level"`
	}{status.Mode, status.TUN, status.MixedPort, status.AllowLAN, status.IPv6, status.LogLevel}
	if a.output == "json" {
		return writeJSON(a.stdout, data)
	}
	return writeTable(a.stdout, []string{"配置项", "值"}, [][]string{
		{"mode", string(status.Mode)},
		{"tun", strconv.FormatBool(status.TUN)},
		{"mixed-port", strconv.Itoa(status.MixedPort)},
		{"allow-lan", strconv.FormatBool(status.AllowLAN)},
		{"ipv6", strconv.FormatBool(status.IPv6)},
		{"log-level", status.LogLevel},
	})
}

func (a *application) writeGroups(groups []domain.ProxyGroup) error {
	if a.output == "json" {
		return writeJSON(a.stdout, groups)
	}
	rows := make([][]string, 0)
	for _, group := range groups {
		for _, proxy := range group.Proxies {
			selected := ""
			if proxy.Name == group.Now {
				selected = "*"
			}
			delay := "-"
			if proxy.Delay > 0 {
				delay = strconv.Itoa(int(proxy.Delay)) + " ms"
			}
			rows = append(rows, []string{group.Name, selected, proxy.Name, proxy.Type, yesNo(proxy.Alive), delay})
		}
	}
	return writeTable(a.stdout, []string{"策略组", "", "节点", "类型", "可用", "延迟"}, rows)
}

func (a *application) writeDelays(group string, delays map[string]uint16) error {
	data := struct {
		Group  string            `json:"group"`
		Delays map[string]uint16 `json:"delays"`
	}{Group: group, Delays: delays}
	if a.output == "json" {
		return writeJSON(a.stdout, data)
	}
	rows := make([][]string, 0, len(delays))
	for _, name := range sortedKeys(delays) {
		delay := delays[name]
		value := "超时"
		if delay > 0 {
			value = strconv.Itoa(int(delay)) + " ms"
		}
		rows = append(rows, []string{name, value})
	}
	return writeTable(a.stdout, []string{"节点", "延迟"}, rows)
}

func (a *application) writeProfiles(profiles []domain.Profile) error {
	views := make([]profileView, 0, len(profiles))
	for _, profile := range profiles {
		views = append(views, profileView{
			ID:             profile.ID,
			Name:           profile.Name,
			Kind:           profile.Kind,
			Active:         profile.Active,
			UpdateInterval: profile.UpdateInterval,
			LastUpdated:    profile.LastUpdated,
			Subscription:   profile.Subscription,
		})
	}
	if a.output == "json" {
		return writeJSON(a.stdout, views)
	}
	rows := make([][]string, 0, len(profiles))
	for _, profile := range profiles {
		updated := "-"
		if !profile.LastUpdated.IsZero() {
			updated = profile.LastUpdated.Local().Format("2006-01-02 15:04")
		}
		active := ""
		if profile.Active {
			active = "*"
		}
		rows = append(rows, []string{active, profile.Name, string(profile.Kind), profile.UpdateInterval.String(), updated})
	}
	return writeTable(a.stdout, []string{"", "名称", "类型", "更新间隔", "上次更新"}, rows)
}

type profileView struct {
	ID             string                  `json:"id"`
	Name           string                  `json:"name"`
	Kind           domain.ProfileKind      `json:"kind"`
	Active         bool                    `json:"active"`
	UpdateInterval time.Duration           `json:"update_interval"`
	LastUpdated    time.Time               `json:"last_updated,omitempty"`
	Subscription   domain.SubscriptionInfo `json:"subscription,omitempty"`
}

func (a *application) writeConnections(connections []domain.Connection) error {
	if a.output == "json" {
		return writeJSON(a.stdout, connections)
	}
	rows := make([][]string, 0, len(connections))
	for _, connection := range connections {
		target := connection.Host
		if target == "" {
			target = connection.Destination
		}
		rows = append(rows, []string{
			shortID(connection.ID), empty(connection.Process, "-"), empty(target, "-"),
			empty(connection.Rule, "-"), formatBytes(connection.Upload), formatBytes(connection.Download),
		})
	}
	return writeTable(a.stdout, []string{"ID", "进程", "目标", "规则", "上传", "下载"}, rows)
}

func (a *application) writeLog(entry domain.LogEntry) error {
	if a.output == "json" {
		return writeJSON(a.stdout, entry)
	}
	return writeTable(a.stdout, nil, [][]string{{entry.Time, strings.ToUpper(entry.Level), entry.Message}})
}

func (a *application) writeScheduleStatus(status domain.ScheduleStatus) error {
	if a.output == "json" {
		return writeJSON(a.stdout, status)
	}
	return writeTable(a.stdout, []string{"启用", "状态"}, [][]string{{yesNo(status.Enabled), status.State}})
}

func (a *application) writeDoctor(checks []domain.DoctorCheck) error {
	if a.output == "json" {
		return writeJSON(a.stdout, checks)
	}
	rows := make([][]string, 0, len(checks))
	for _, check := range checks {
		state := "失败"
		if check.OK {
			state = "正常"
		}
		if check.Fixed {
			state = "已修复"
		}
		rows = append(rows, []string{check.Name, state, check.Message})
	}
	return writeTable(a.stdout, []string{"检查", "结果", "详情"}, rows)
}

func yesNo(value bool) string {
	if value {
		return "是"
	}
	return "否"
}

func onOff(value bool) string {
	if value {
		return "on"
	}
	return "off"
}

func empty(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func intOrDash(value int) string {
	if value == 0 {
		return "-"
	}
	return strconv.Itoa(value)
}

func shortID(value string) string {
	const size = 12
	if len(value) <= size {
		return value
	}
	return value[:size]
}

func formatBytes(value int64) string {
	if value < 0 {
		return "-"
	}
	const unit = int64(1024)
	if value < unit {
		return strconv.FormatInt(value, 10) + " B"
	}
	divisor, exponent := unit, 0
	for quotient := value / unit; quotient >= unit && exponent < 4; quotient /= unit {
		divisor *= unit
		exponent++
	}
	return fmt.Sprintf("%.1f %ciB", float64(value)/float64(divisor), "KMGTPE"[exponent])
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.Local().Format("2006-01-02 15:04")
}
