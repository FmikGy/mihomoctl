package tui

import (
	"fmt"
	"image/color"
	"os"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"mihomoctl/internal/domain"
)

type palette struct {
	text, muted, accent, onAccent, good, warn, bad, surface, border color.Color
}

func colors() palette {
	if colorsDisabled() {
		return palette{}
	}
	return palette{
		text:     lipgloss.Color("252"),
		muted:    lipgloss.Color("245"),
		accent:   lipgloss.Color("81"),
		onAccent: lipgloss.Color("16"),
		good:     lipgloss.Color("42"),
		warn:     lipgloss.Color("214"),
		bad:      lipgloss.Color("203"),
		surface:  lipgloss.Color("236"),
		border:   lipgloss.Color("240"),
	}
}

func colorsDisabled() bool {
	_, disabled := os.LookupEnv("NO_COLOR")
	return disabled
}

func (m Model) View() tea.View {
	view := tea.NewView(m.render())
	view.AltScreen = true
	view.WindowTitle = "mihomoctl"
	return view
}

func (m Model) render() string {
	if m.width == 0 || m.height == 0 {
		return "正在载入 mihomoctl…"
	}
	if m.width < 60 || m.height < 16 {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
			"终端空间不足\n至少需要 60 × 16", lipgloss.WithWhitespaceChars(" "))
	}

	content := strings.Join([]string{
		m.renderHeader(),
		m.renderTabs(),
		m.renderBody(),
		m.renderFooter(),
	}, "\n")
	if m.help {
		content = m.renderOverlay(m.helpText())
	} else if m.confirm != confirmNone {
		content = m.renderOverlay(m.confirmText())
	} else if m.inMode != inputNone {
		content = m.renderOverlay(m.renderInput())
	}
	return fitBlock(content, m.width, m.height)
}

func (m Model) renderHeader() string {
	c := colors()
	brand := lipgloss.NewStyle().Bold(true).Foreground(c.text).Render("mihomoctl")
	state, stateColor := "已停止", c.muted
	if m.status.Service.Active {
		state, stateColor = "运行中", c.good
	}
	if m.err != "" && !m.status.Service.Active {
		state, stateColor = "不可用", c.bad
	}
	status := lipgloss.NewStyle().Foreground(stateColor).Render("● " + state)
	right := fmt.Sprintf("%s  TUN %s  下行 %s", modeLabel(m.status.Mode), onOff(m.status.TUN), formatRate(m.status.Traffic.Down))
	right = lipgloss.NewStyle().Foreground(c.muted).Render(right)
	gap := max(1, m.width-lipgloss.Width(brand)-lipgloss.Width(status)-lipgloss.Width(right)-4)
	return " " + brand + "  " + status + strings.Repeat(" ", gap) + right + " "
}

func (m Model) renderTabs() string {
	c := colors()
	parts := make([]string, 0, len(pageNames))
	for i, name := range pageNames {
		style := lipgloss.NewStyle().Padding(0, 1).Foreground(c.muted)
		if page(i) == m.page {
			style = style.Foreground(c.accent).Bold(true).Underline(true)
		}
		parts = append(parts, style.Render(name))
	}
	return lipgloss.NewStyle().Width(m.width).BorderBottom(true).BorderStyle(lipgloss.NormalBorder()).BorderForeground(c.border).Render(strings.Join(parts, " "))
}

func (m Model) renderBody() string {
	bodyHeight := max(8, m.height-6)
	var body string
	switch m.page {
	case pageOverview:
		body = m.renderOverview(bodyHeight)
	case pageProxies:
		body = m.renderProxies(bodyHeight)
	case pageProfiles:
		body = m.renderProfiles(bodyHeight)
	case pageConnections:
		body = m.renderConnections(bodyHeight)
	case pageLogs:
		body = m.renderLogs(bodyHeight)
	case pageSettings:
		body = m.renderSettings(bodyHeight)
	}
	return fitBlock(body, m.width, bodyHeight)
}

func (m Model) renderOverview(height int) string {
	c := colors()
	contentWidth := max(1, m.width-4)
	graphWidth := min(trafficHistoryLimit, max(1, contentWidth-22))
	down, up, peak := trafficChartSeries(m.trafficHistory, graphWidth)
	rows := []string{
		sectionTitle("实时速度"),
		trafficChartLine("↓ 下载", m.status.Traffic.Down, trafficSparkline(down, graphWidth, peak), c.accent),
		trafficChartLine("↑ 上传", m.status.Traffic.Up, trafficSparkline(up, graphWidth, peak), c.good),
		overviewColumns(contentWidth,
			overviewMetric("累计下载", formatBytes(m.status.Traffic.DownTotal)),
			overviewMetric("累计上传", formatBytes(m.status.Traffic.UpTotal))),
		overviewColumns(contentWidth,
			overviewMetric("活动连接", fmt.Sprintf("%d", m.status.ConnectionCount)),
			overviewMetric("内存", formatBytes(m.status.Memory))),
		sectionTitle("运行状态"),
		overviewColumns(contentWidth,
			overviewMetric("活动配置", empty(m.status.ActiveProfile, "未接管")),
			overviewMetric("核心版本", empty(m.status.CoreVersion, "未连接"))),
		overviewColumns(contentWidth,
			overviewMetric("模式", modeLabel(m.status.Mode)),
			overviewMetric("TUN", onOff(m.status.TUN)),
			overviewMetric("混合端口", portLabel(m.status.MixedPort))),
	}
	return lipgloss.NewStyle().Padding(1, 2).Render(strings.Join(rows, "\n"))
}

const trafficLevels = "▁▂▃▄▅▆▇█"

func trafficChartSeries(history []trafficSample, width int) (down, up []int64, peak int64) {
	if width <= 0 || len(history) == 0 {
		return nil, nil, 0
	}
	if len(history) > width {
		history = history[len(history)-width:]
	}
	down = make([]int64, len(history))
	up = make([]int64, len(history))
	for index, sample := range history {
		down[index] = nonNegativeTraffic(sample.down)
		up[index] = nonNegativeTraffic(sample.up)
		if down[index] > peak {
			peak = down[index]
		}
		if up[index] > peak {
			peak = up[index]
		}
	}
	return down, up, peak
}

func trafficSparkline(values []int64, width int, peak int64) string {
	if width <= 0 {
		return ""
	}
	if len(values) > width {
		values = values[len(values)-width:]
	}
	levels := []rune(trafficLevels)
	result := make([]rune, width)
	missing := width - len(values)
	for index := range missing {
		result[index] = ' '
	}
	for index, value := range values {
		value = nonNegativeTraffic(value)
		level := 0
		if peak > 0 && value > 0 {
			level = int(float64(value) / float64(peak) * float64(len(levels)-1))
			level = max(1, min(level, len(levels)-1))
		}
		result[missing+index] = levels[level]
	}
	return string(result)
}

func trafficChartLine(label string, rate int64, graph string, chartColor color.Color) string {
	labelText := lipgloss.NewStyle().Bold(true).Foreground(chartColor).Render(padRight(label, 8))
	rateText := lipgloss.NewStyle().Foreground(colors().text).Render(padLeft(formatRate(rate), 12))
	chart := lipgloss.NewStyle().Foreground(chartColor).Render(graph)
	return labelText + rateText + "  " + chart
}

func overviewMetric(label, value string) string {
	c := colors()
	return lipgloss.NewStyle().Foreground(c.muted).Render(safeText(label)+" ") +
		lipgloss.NewStyle().Foreground(c.text).Render(safeText(value))
}

func overviewColumns(width int, cells ...string) string {
	if width <= 0 || len(cells) == 0 {
		return ""
	}
	gapWidth := 2
	available := max(0, width-gapWidth*(len(cells)-1))
	baseWidth, remainder := available/len(cells), available%len(cells)
	columns := make([]string, len(cells))
	for index, cell := range cells {
		columnWidth := baseWidth
		if index < remainder {
			columnWidth++
		}
		cell = ansi.Truncate(cell, columnWidth, "…")
		columns[index] = cell + strings.Repeat(" ", max(0, columnWidth-lipgloss.Width(cell)))
	}
	return strings.Join(columns, strings.Repeat(" ", gapWidth))
}

func (m Model) renderProxies(height int) string {
	c := colors()
	groups := m.filteredGroups()
	if len(groups) == 0 {
		return emptyState("没有可用策略组", "启动 Mihomo 或添加有效配置")
	}
	groupIndex := min(m.groupCursor, len(groups)-1)
	group := groups[groupIndex]
	nodes := filteredProxies(group, m.filter)
	nodeStart, nodeEnd := viewportBounds(len(nodes), m.proxyCursor, m.proxyOffset, m.listCapacity())
	if m.width < 90 {
		title := fmt.Sprintf("策略组 %d/%d  ·  %s  ·  %s", groupIndex+1, len(groups), group.Name, group.Now)
		lines := []string{sectionTitle(title)}
		if len(nodes) == 0 {
			lines = append(lines, lipgloss.NewStyle().Foreground(c.muted).Render("没有匹配节点"))
		}
		for i := nodeStart; i < nodeEnd; i++ {
			node := nodes[i]
			latency := "—"
			if node.Delay > 0 {
				latency = fmt.Sprintf("%d ms", node.Delay)
			}
			detail := latency + "  " + node.Type
			lines = append(lines, selectedNodeRow(i == m.proxyCursor, node.Name, detail, m.width-6))
		}
		return lipgloss.NewStyle().Padding(1, 2).Render(strings.Join(lines, "\n"))
	}

	leftWidth := max(24, m.width/3)
	rightWidth := m.width - leftWidth - 5
	left := []string{sectionTitle("策略组")}
	groupStart, groupEnd := viewportBounds(len(groups), groupIndex, m.groupOffset, m.listCapacity())
	for i := groupStart; i < groupEnd; i++ {
		item := groups[i]
		left = append(left, selectedRow(i == groupIndex, item.Name, item.Now, leftWidth-4))
	}
	rightTitle := truncate(group.Name+"  ·  "+group.Now, max(1, rightWidth-2))
	right := []string{sectionTitle(rightTitle)}
	if len(nodes) == 0 {
		right = append(right, lipgloss.NewStyle().Foreground(c.muted).Render("没有匹配节点"))
	}
	for i := nodeStart; i < nodeEnd; i++ {
		node := nodes[i]
		latency := "—"
		if node.Delay > 0 {
			latency = fmt.Sprintf("%d ms", node.Delay)
		}
		nameWidth := max(10, rightWidth-26)
		details := " " + padRight(node.Name, nameWidth) + " " + padLeft(latency, 8) + "  " + truncate(node.Type, 9)
		if i == m.proxyCursor {
			right = append(right, selectedWideNodeRow(node.Alive, details, rightWidth-2))
			continue
		}
		alive := lipgloss.NewStyle().Foreground(c.bad).Render("●")
		if node.Alive {
			alive = lipgloss.NewStyle().Foreground(c.good).Render("●")
		}
		right = append(right, highlightNodeRow(false, alive+details, rightWidth-2))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top,
		lipgloss.NewStyle().Width(leftWidth).Padding(1, 1).Render(strings.Join(left, "\n")),
		lipgloss.NewStyle().Foreground(c.border).Render("│"),
		lipgloss.NewStyle().Width(rightWidth).Padding(1, 1).Render(strings.Join(right, "\n")),
	)
}

func (m Model) renderProfiles(height int) string {
	if len(m.profiles) == 0 {
		return emptyState("还没有配置", "按 a 添加订阅、本地文件或节点链接")
	}
	lines := []string{sectionTitle("配置")}
	start, end := viewportBounds(len(m.profiles), m.profileCursor, m.profileOffset, m.listCapacity())
	for i := start; i < end; i++ {
		profile := m.profiles[i]
		state := ""
		if profile.Active {
			state = "活动"
		}
		updated := "未更新"
		if !profile.LastUpdated.IsZero() {
			updated = profile.LastUpdated.Local().Format("01-02 15:04")
		}
		kind := map[domain.ProfileKind]string{domain.ProfileLocal: "本地", domain.ProfileRemote: "订阅", domain.ProfileURI: "节点"}[profile.Kind]
		detail := fmt.Sprintf("%-4s  %-11s  %s", kind, updated, state)
		lines = append(lines, selectedRow(i == m.profileCursor, profile.Name, detail, m.width-6))
	}
	return lipgloss.NewStyle().Padding(1, 2).Render(strings.Join(lines, "\n"))
}

func (m Model) renderConnections(height int) string {
	connections := m.filteredConnections()
	if len(connections) == 0 {
		return emptyState("当前没有活动连接", "连接建立后会自动显示")
	}
	lines := []string{sectionTitle(fmt.Sprintf("活动连接 · %d", len(connections)))}
	start, end := viewportBounds(len(connections), m.connectionCursor, m.connectionOffset, m.listCapacity())
	for i := start; i < end; i++ {
		connection := connections[i]
		target := connection.Host
		if target == "" {
			target = connection.Destination
		}
		process := empty(connection.Process, connection.Network)
		detail := fmt.Sprintf("%-14s %9s  %s", truncate(process, 14), formatBytes(connection.Upload+connection.Download), truncate(connection.Rule, 16))
		lines = append(lines, selectedRow(i == m.connectionCursor, target, detail, m.width-6))
	}
	return lipgloss.NewStyle().Padding(1, 2).Render(strings.Join(lines, "\n"))
}

func (m Model) renderLogs(height int) string {
	c := colors()
	logs := m.filteredLogs()
	title := "实时日志 · " + strings.ToUpper(m.logLevel)
	if m.logPaused {
		title += " · 已暂停"
	}
	lines := []string{sectionTitle(title)}
	visible := max(1, height-3)
	start := max(0, len(logs)-visible)
	for _, entry := range logs[start:] {
		style := lipgloss.NewStyle().Foreground(c.text)
		switch strings.ToLower(entry.Level) {
		case "error":
			style = style.Foreground(c.bad)
		case "warning", "warn":
			style = style.Foreground(c.warn)
		case "debug":
			style = style.Foreground(c.muted)
		}
		prefix := fmt.Sprintf("%-8s %-5s", safeText(entry.Time), safeText(strings.ToUpper(entry.Level)))
		lines = append(lines, style.Render(prefix+" "+truncate(entry.Message, m.width-22)))
	}
	if len(logs) == 0 {
		message := "等待日志…"
		if m.filter != "" && len(m.logs) > 0 {
			message = "没有匹配日志"
		}
		lines = append(lines, lipgloss.NewStyle().Foreground(c.muted).Render(message))
	}
	return lipgloss.NewStyle().Padding(1, 2).Render(strings.Join(lines, "\n"))
}

func (m Model) renderSettings(height int) string {
	schedule := "未检测"
	if m.scheduleOK {
		schedule = onOff(m.schedule.Enabled)
	}
	rows := []struct{ name, value string }{
		{"Mihomo 服务", activeLabel(m.status.Service.Active)},
		{"开机启动", onOff(m.status.Service.Enabled)},
		{"运行模式", modeLabel(m.status.Mode)},
		{"TUN", onOff(m.status.TUN)},
		{"订阅定时更新", schedule},
	}
	lines := []string{sectionTitle("设置")}
	start, end := viewportBounds(len(rows), m.settingCursor, m.settingOffset, m.settingsCapacity())
	for i := start; i < end; i++ {
		row := rows[i]
		lines = append(lines, selectedRow(i == m.settingCursor, row.name, row.value, m.width-6))
	}
	lines = append(lines, "", sectionTitle("监听"), fmt.Sprintf("混合端口  %s    局域网  %s    IPv6  %s", portLabel(m.status.MixedPort), onOff(m.status.AllowLAN), onOff(m.status.IPv6)))
	return lipgloss.NewStyle().Padding(1, 2).Height(height).Render(strings.Join(lines, "\n"))
}

func (m Model) renderFooter() string {
	c := colors()
	message := ""
	messageStyle := lipgloss.NewStyle().Foreground(c.muted)
	if m.loading {
		message = "正在处理…"
	} else if m.err != "" {
		message = "错误  " + safeText(m.err)
		messageStyle = messageStyle.Foreground(c.bad)
	} else if m.toast != "" {
		message = safeText(m.toast)
		if m.toastWarning {
			messageStyle = messageStyle.Foreground(c.warn)
		} else {
			messageStyle = messageStyle.Foreground(c.good)
		}
	} else if m.filter != "" {
		message = "筛选  " + safeText(m.filter)
	}

	contentWidth := max(0, m.width-2)
	if message == "" {
		hints := fitFooterHints(m.pageHints(), contentWidth)
		return lipgloss.NewStyle().Foreground(c.muted).Width(m.width).Render(" " + hints)
	}

	essentials := fitFooterHints(nil, contentWidth)
	messageWidth := max(0, contentWidth-lipgloss.Width(essentials)-2)
	message = ansi.Truncate(message, messageWidth, "…")
	gap := max(1, contentWidth-lipgloss.Width(message)-lipgloss.Width(essentials))
	line := " " + messageStyle.Render(message) + strings.Repeat(" ", gap) + essentials
	return lipgloss.NewStyle().Foreground(c.muted).Width(m.width).Render(line)
}

func (m Model) pageHints() []string {
	switch m.page {
	case pageOverview:
		return []string{"Enter启停", "r刷新", "Tab切页"}
	case pageProxies:
		return []string{"↑↓选", "[]组", "Enter切换", "t测速", "/筛选"}
	case pageProfiles:
		return []string{"↑↓选", "a添加", "u更新", "d删除", "Enter激活"}
	case pageConnections:
		return []string{"↑↓选", "Enter关闭", "x全部", "/筛选"}
	case pageLogs:
		pause := "Space暂停"
		if m.logPaused {
			pause = "Space继续"
		}
		return []string{pause, "/筛选", "r刷新"}
	case pageSettings:
		return []string{"↑↓选", "Enter切换", "r刷新"}
	default:
		return nil
	}
}

func fitFooterHints(actions []string, width int) string {
	essentials := []string{"?帮助", "q退出"}
	essentialText := strings.Join(essentials, "  ")
	if lipgloss.Width(essentialText) > width {
		return ansi.Truncate(essentialText, width, "")
	}

	selected := make([]string, 0, len(actions)+len(essentials))
	for _, action := range actions {
		candidate := append(append([]string(nil), selected...), action)
		candidate = append(candidate, essentials...)
		if lipgloss.Width(strings.Join(candidate, "  ")) > width {
			break
		}
		selected = append(selected, action)
	}
	selected = append(selected, essentials...)
	return strings.Join(selected, "  ")
}

func (m Model) renderOverlay(content string) string {
	c := colors()
	width := min(max(44, lipgloss.Width(content)+6), m.width-8)
	box := lipgloss.NewStyle().
		Width(width).
		Padding(1, 2).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(c.accent).
		Foreground(c.text).
		Background(c.surface).
		Render(content)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box,
		lipgloss.WithWhitespaceChars(" "), lipgloss.WithWhitespaceStyle(lipgloss.NewStyle().Background(c.surface)))
}

func (m Model) renderInput() string {
	title := "筛选"
	if m.inMode == inputProfile {
		title = "添加配置"
	}
	return sectionTitle(title) + "\n\n" + m.input.View() + "\n\nEnter 确认  ·  Esc 取消"
}

func (m Model) confirmText() string {
	message := "确认执行此操作？"
	switch m.confirm {
	case confirmRemoveProfile:
		message = "删除配置 “" + safeText(m.profiles[m.profileCursor].Name) + "”？"
	case confirmCloseConnection:
		message = "关闭选中的连接？"
	case confirmCloseAll:
		message = "关闭全部活动连接？"
	case confirmStopService:
		message = "停止 Mihomo 服务？"
	}
	return sectionTitle("确认") + "\n\n" + message + "\n\ny / Enter 确认  ·  n / Esc 取消"
}

func (m Model) helpText() string {
	lines := []string{
		sectionTitle("快捷键"), "",
		"Tab / ← →     切换页面",
		"j k / ↑ ↓     移动选择",
		"Enter          执行当前操作",
		"r              刷新",
		"/              筛选",
		"t              测试策略组延迟",
		"[ ]            切换策略组",
		"a / u / d      添加、更新、删除配置",
		"space / x      暂停日志 / 关闭全部连接",
		"q              退出",
	}
	return strings.Join(lines, "\n")
}

func sectionTitle(text string) string {
	c := colors()
	return lipgloss.NewStyle().Bold(true).Foreground(c.accent).Render(safeText(text))
}

func selectedRow(selected bool, name, detail string, width int) string {
	c := colors()
	name, detail = safeText(name), safeText(detail)
	detailWidth := min(max(8, lipgloss.Width(detail)), max(8, width/2))
	nameWidth := max(8, width-detailWidth-4)
	line := padRight(name, nameWidth) + "  " + padLeft(detail, detailWidth)
	if selected {
		return lipgloss.NewStyle().Foreground(c.accent).Bold(true).Render("› " + line)
	}
	return "  " + line
}

func selectedNodeRow(selected bool, name, detail string, width int) string {
	name, detail = safeText(name), safeText(detail)
	detailWidth := min(max(8, lipgloss.Width(detail)), max(8, width/2))
	nameWidth := max(8, width-detailWidth-4)
	line := padRight(name, nameWidth) + "  " + padLeft(detail, detailWidth)
	return highlightNodeRow(selected, line, width)
}

func highlightNodeRow(selected bool, line string, width int) string {
	width = max(0, width)
	contentWidth := max(0, width-2)
	line = ansi.Truncate(line, contentWidth, "")
	line += strings.Repeat(" ", max(0, contentWidth-lipgloss.Width(line)))
	if !selected {
		return "  " + line
	}
	return nodeHighlightStyle(width).Render("› " + line)
}

func nodeHighlightStyle(width int) lipgloss.Style {
	style := lipgloss.NewStyle().Bold(true).Width(width)
	if colorsDisabled() {
		return style.Reverse(true)
	}
	c := colors()
	return style.Foreground(c.onAccent).Background(c.accent)
}

func selectedWideNodeRow(alive bool, details string, width int) string {
	width = max(0, width)
	detailWidth := max(0, width-3)
	details = ansi.Truncate(details, detailWidth, "")
	details += strings.Repeat(" ", max(0, detailWidth-lipgloss.Width(details)))
	health := "○"
	if alive {
		health = "●"
	}
	return nodeHighlightStyle(width).Render("› " + health + details)
}

func padRight(value string, width int) string {
	value = truncate(value, width)
	return value + strings.Repeat(" ", max(0, width-lipgloss.Width(value)))
}

func padLeft(value string, width int) string {
	value = truncate(value, width)
	return strings.Repeat(" ", max(0, width-lipgloss.Width(value))) + value
}

func emptyState(title, detail string) string {
	c := colors()
	content := lipgloss.NewStyle().Bold(true).Foreground(c.text).Render(title) + "\n" + lipgloss.NewStyle().Foreground(c.muted).Render(detail)
	return lipgloss.Place(60, 10, lipgloss.Center, lipgloss.Center, content)
}

func fitBlock(value string, width, height int) string {
	lines := strings.Split(value, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, width, "")
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func truncate(value string, width int) string {
	if width <= 0 {
		return ""
	}
	return ansi.Truncate(safeText(value), width, "…")
}

func empty(value, fallback string) string {
	if value == "" {
		return safeText(fallback)
	}
	return safeText(value)
}

func safeText(value string) string {
	value = ansi.Strip(value)
	return strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return ' '
		}
		return character
	}, value)
}

func activeLabel(active bool) string {
	if active {
		return "运行中"
	}
	return "已停止"
}

func onOff(enabled bool) string {
	if enabled {
		return "开启"
	}
	return "关闭"
}

func portLabel(port int) string {
	if port <= 0 {
		return "未设置"
	}
	return fmt.Sprintf("%d", port)
}

func formatRate(bytes int64) string { return formatBytes(bytes) + "/s" }

func formatBytes(bytes int64) string {
	if bytes < 0 {
		bytes = 0
	}
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	value := float64(bytes)
	unit := 0
	for value >= 1024 && unit < len(units)-1 {
		value /= 1024
		unit++
	}
	if unit == 0 {
		return fmt.Sprintf("%d %s", bytes, units[unit])
	}
	return fmt.Sprintf("%.1f %s", value, units[unit])
}
