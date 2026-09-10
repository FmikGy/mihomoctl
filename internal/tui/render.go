package tui

import (
	"fmt"
	"image/color"
	"math"
	"os"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"mihomoctl/internal/domain"
)

type palette struct {
	text, muted, accent, onAccent, good, warn, bad, surface, surfaceAlt, border color.Color
}

func colors() palette {
	if colorsDisabled() {
		return palette{}
	}
	return palette{
		text:       lipgloss.Color("252"),
		muted:      lipgloss.Color("244"),
		accent:     lipgloss.Color("45"),
		onAccent:   lipgloss.Color("16"),
		good:       lipgloss.Color("42"),
		warn:       lipgloss.Color("214"),
		bad:        lipgloss.Color("203"),
		surface:    lipgloss.Color("235"),
		surfaceAlt: lipgloss.Color("237"),
		border:     lipgloss.Color("239"),
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
		return "正在载入 mihomoctl..."
	}
	if m.width < 60 || m.height < 16 {
		return centeredBlock("终端空间不足\n至少需要 60 × 16", m.width, m.height)
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
	} else if m.picker != pickerNone {
		content = m.renderOverlay(m.renderPicker())
	}
	return fitBlock(content, m.width, m.height)
}

func (m Model) renderHeader() string {
	c := colors()
	brand := lipgloss.NewStyle().Bold(true).Foreground(c.text).Render("MIHOMOCTL")
	state, stateColor := "已停止", c.muted
	if m.status.Service.Active {
		state, stateColor = "运行中", c.good
	}
	if _, unavailable := m.errors[errorStatus]; unavailable && !serviceStatusAvailable(m.status.Service) {
		state, stateColor = "不可用", c.bad
	}
	status := lipgloss.NewStyle().Foreground(stateColor).Render("● " + state)
	profileValueWidth := max(4, m.width-lipgloss.Width(brand)-lipgloss.Width(status)-12)
	profile := lipgloss.NewStyle().Foreground(c.muted).Render("配置 ") +
		lipgloss.NewStyle().Foreground(c.text).Render(truncate(empty(m.status.ActiveProfile, "未接管"), profileValueWidth))
	first := joinSides(brand+"  "+status, profile, m.width-2)

	modePrefix := "模式 "
	if m.width < 90 {
		modePrefix = ""
	}
	mode := lipgloss.NewStyle().Foreground(c.muted).Render(modePrefix) +
		lipgloss.NewStyle().Foreground(c.text).Render(modeLabel(m.status.Mode))
	tunColor := c.muted
	if m.status.TUN {
		tunColor = c.good
	}
	tun := lipgloss.NewStyle().Foreground(tunColor).Render("TUN " + compactOnOff(m.status.TUN))
	left := mode + "  " + tun
	if m.width >= 90 {
		left += "  " + lipgloss.NewStyle().Foreground(c.muted).Render("端口 "+portLabel(m.status.MixedPort))
	}
	rates := lipgloss.NewStyle().Foreground(c.accent).Render("↓ "+formatRate(m.status.Traffic.Down)) + "  " +
		lipgloss.NewStyle().Foreground(c.good).Render("↑ "+formatRate(m.status.Traffic.Up))
	second := joinSides(left, rates, m.width-2)

	lineStyle := lipgloss.NewStyle().Background(c.surface)
	return lineStyle.Render(" "+fitLine(first, m.width-2)+" ") + "\n" +
		lineStyle.Render(" "+fitLine(second, m.width-2)+" ")
}

func (m Model) renderTabs() string {
	c := colors()
	parts := make([]string, 0, len(pageNames))
	for i, name := range pageNames {
		cellWidth := m.width / len(pageNames)
		if i < m.width%len(pageNames) {
			cellWidth++
		}
		label := centerText(name, cellWidth)
		style := lipgloss.NewStyle().Foreground(c.muted)
		if page(i) == m.page {
			style = style.Foreground(c.accent).Background(c.surfaceAlt).Bold(true)
			if colorsDisabled() {
				style = style.Reverse(true)
			}
		}
		parts = append(parts, style.Render(label))
	}
	rule := lipgloss.NewStyle().Foreground(c.border).Render(strings.Repeat("─", m.width))
	return fitLine(strings.Join(parts, ""), m.width) + "\n" + rule
}

func (m Model) renderBody() string {
	bodyHeight := max(0, m.height-5)
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
	innerHeight := max(0, height-2)
	chartHeight := max(2, innerHeight-6)
	rows := []string{
		titleLine("实时速度", trafficWindowLabel(m.trafficHistory), contentWidth),
	}
	if chartHeight < 6 {
		graphWidth := max(8, contentWidth-39)
		down, up, downPeak, upPeak := trafficChartSeries(m.trafficHistory, graphWidth)
		rows = append(rows,
			trafficChartLine("↓ 下载", m.status.Traffic.Down, downPeak, trafficSparkline(down, graphWidth, downPeak), c.accent, contentWidth),
			trafficChartLine("↑ 上传", m.status.Traffic.Up, upPeak, trafficSparkline(up, graphWidth, upPeak), c.good, contentWidth),
		)
		for len(rows) < chartHeight+1 {
			rows = append(rows, "")
		}
	} else {
		const axisWidth = 9
		plotWidth := max(8, contentWidth-axisWidth-2)
		down, up, downPeak, upPeak := trafficChartSeries(m.trafficHistory, plotWidth)
		downHeight := chartHeight / 2
		upHeight := chartHeight - downHeight
		rows = append(rows, trafficAreaChartRows("↓ 下载", m.status.Traffic.Down, downPeak, down, contentWidth, downHeight, c.accent)...)
		rows = append(rows, trafficAreaChartRows("↑ 上传", m.status.Traffic.Up, upPeak, up, contentWidth, upHeight, c.good)...)
	}
	rows = append(rows,
		overviewColumns(contentWidth,
			overviewMetric("累计下载", formatBytes(m.status.Traffic.DownTotal)),
			overviewMetric("累计上传", formatBytes(m.status.Traffic.UpTotal))),
		overviewColumns(contentWidth,
			overviewMetric("活动连接", fmt.Sprintf("%d", m.status.ConnectionCount)),
			overviewMetric("内存", formatBytes(m.status.Memory))),
		titleLine("运行状态", serviceDetail(m.status.Service), contentWidth),
		overviewColumns(contentWidth,
			overviewMetric("活动配置", empty(m.status.ActiveProfile, "未接管")),
			overviewMetric("核心版本", empty(m.status.CoreVersion, "未连接"))),
		overviewColumns(contentWidth,
			overviewMetric("模式", modeLabel(m.status.Mode)),
			overviewMetric("TUN", onOff(m.status.TUN)),
			overviewMetric("混合端口", portLabel(m.status.MixedPort))),
	)
	return renderPage(rows, m.width, height)
}

const trafficLevels = "▁▂▃▄▅▆▇█"

func trafficChartSeries(history []trafficSample, width int) (down, up []int64, downPeak, upPeak int64) {
	if width <= 0 || len(history) == 0 {
		return nil, nil, 0, 0
	}

	first, last := history[0].at, history[len(history)-1].at
	if first.IsZero() || !last.After(first) {
		if len(history) > width {
			history = history[len(history)-width:]
		}
		down = make([]int64, len(history))
		up = make([]int64, len(history))
		for index, sample := range history {
			down[index] = nonNegativeTraffic(sample.down)
			up[index] = nonNegativeTraffic(sample.up)
			downPeak = max64(downPeak, down[index])
			upPeak = max64(upPeak, up[index])
		}
		return down, up, downPeak, upPeak
	}
	for _, sample := range history {
		downPeak = max64(downPeak, nonNegativeTraffic(sample.down))
		upPeak = max64(upPeak, nonNegativeTraffic(sample.up))
	}

	// Map samples onto elapsed time rather than append order. Repeated manual
	// refreshes in the same interval therefore do not stretch the graph.
	down = make([]int64, width)
	up = make([]int64, width)
	span := last.Sub(first)
	sampleIndex := 0
	for column := range width {
		target := first
		if width > 1 {
			target = first.Add(time.Duration(column) * span / time.Duration(width-1))
		}
		for sampleIndex+1 < len(history) && !history[sampleIndex+1].at.After(target) {
			sampleIndex++
		}
		down[column] = nonNegativeTraffic(history[sampleIndex].down)
		up[column] = nonNegativeTraffic(history[sampleIndex].up)
	}
	return down, up, downPeak, upPeak
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

func trafficChartLine(label string, rate, peak int64, graph string, chartColor color.Color, width int) string {
	c := colors()
	labelText := lipgloss.NewStyle().Bold(true).Foreground(chartColor).Render(padRight(label, 7))
	rateText := lipgloss.NewStyle().Foreground(c.text).Render(padLeft(formatRate(rate), 11))
	peakText := lipgloss.NewStyle().Foreground(c.muted).Render("  峰 " + padLeft(formatRate(peak), 11) + "  ")
	available := max(0, width-lipgloss.Width(labelText)-lipgloss.Width(rateText)-lipgloss.Width(peakText))
	chart := ansi.Truncate(graph, available, "")
	chart = padRight(chart, available)
	return labelText + rateText + peakText + lipgloss.NewStyle().Foreground(chartColor).Render(chart)
}

func trafficAreaChartRows(label string, rate, peak int64, values []int64, width, height int, chartColor color.Color) []string {
	if height <= 0 {
		return nil
	}
	c := colors()
	header := joinSides(
		lipgloss.NewStyle().Bold(true).Foreground(chartColor).Render(label),
		lipgloss.NewStyle().Foreground(c.text).Render("当前 "+formatRate(rate))+"  "+
			lipgloss.NewStyle().Foreground(c.muted).Render("峰 "+formatRate(peak)),
		width,
	)
	if height == 1 {
		return []string{header}
	}

	const axisWidth = 9
	plotHeight := height - 1
	plotWidth := max(0, width-axisWidth-2)
	plot := trafficAreaPlot(values, plotWidth, plotHeight, peak)
	rows := make([]string, 0, height)
	rows = append(rows, header)
	for row := range plotHeight {
		axisLabel := ""
		tick := "│"
		switch {
		case row == 0:
			axisLabel = formatCompactRate(peak)
			tick = "┤"
		case row == plotHeight-1:
			axisLabel = "0"
			tick = "┤"
		case peak > 0 && plotHeight >= 5 && row == plotHeight/2:
			axisLabel = formatCompactRate(peak / 2)
			tick = "┤"
		}
		axis := lipgloss.NewStyle().Foreground(c.muted).Render(padLeft(axisLabel, axisWidth) + " " + tick)
		rows = append(rows, axis+lipgloss.NewStyle().Foreground(chartColor).Render(plot[row]))
	}
	return rows
}

func trafficAreaPlot(values []int64, width, height int, peak int64) []string {
	width, height = max(0, width), max(0, height)
	if height == 0 {
		return nil
	}
	grid := make([][]rune, height)
	for row := range grid {
		grid[row] = []rune(strings.Repeat(" ", width))
	}
	if width == 0 || len(values) == 0 {
		rows := make([]string, height)
		for row := range grid {
			rows[row] = string(grid[row])
		}
		return rows
	}
	if len(values) > width {
		values = values[len(values)-width:]
	}
	levels := []rune(trafficLevels)
	start := width - len(values)
	maximumUnits := height * len(levels)
	for column, value := range values {
		value = nonNegativeTraffic(value)
		filled := 1
		if peak > 0 && value > 0 {
			filled = int(math.Ceil(float64(value) / float64(peak) * float64(maximumUnits)))
			filled = max(1, min(filled, maximumUnits))
		}
		for row := range height {
			units := filled - (height-row-1)*len(levels)
			if units <= 0 {
				continue
			}
			grid[row][start+column] = levels[min(units, len(levels))-1]
		}
	}
	rows := make([]string, height)
	for row := range grid {
		rows[row] = string(grid[row])
	}
	return rows
}

func formatCompactRate(bytes int64) string {
	if bytes <= 0 {
		return "0 B/s"
	}
	return formatCompactBytes(bytes) + "/s"
}

func trafficWindowLabel(history []trafficSample) string {
	if len(history) < 2 || history[0].at.IsZero() || !history[len(history)-1].at.After(history[0].at) {
		return "最近样本"
	}
	duration := history[len(history)-1].at.Sub(history[0].at).Round(time.Second)
	if duration < time.Minute {
		return fmt.Sprintf("最近 %d 秒", max(1, int(duration/time.Second)))
	}
	return fmt.Sprintf("最近 %.1f 分钟", duration.Minutes())
}

func serviceDetail(service domain.ServiceStatus) string {
	if service.PID > 0 {
		return fmt.Sprintf("PID %d", service.PID)
	}
	return empty(service.State, "Mihomo")
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
	groups := m.currentGroupViews()
	if len(groups) == 0 {
		detail := "启动 Mihomo 或添加有效配置"
		if m.filter != "" && len(m.groups) > 0 {
			detail = "没有匹配当前筛选条件的策略组"
		}
		return emptyState("没有可用策略组", detail, m.width, height)
	}
	groupIndex := clamp(m.groupCursor, 0, len(groups)-1)
	groupView := groups[groupIndex]
	group := m.groups[groupView.groupIndex]
	nodes := groupView.proxies
	nodeStart, nodeEnd := viewportBounds(len(nodes), m.proxyCursor, m.proxyOffset, m.proxyListCapacity())
	rowWidth := max(1, m.width-4)
	position := fmt.Sprintf("组 %s  节点 %s", listPosition(groupIndex, len(groups)), listPosition(m.proxyCursor, len(nodes)))
	lines := []string{
		titleLine("策略组 · "+group.Name, position, rowWidth),
		m.proxyGroupViewSelector(groups, groupIndex, rowWidth),
		proxyTableHeader(rowWidth),
	}
	if len(nodes) == 0 {
		lines = append(lines, mutedLine("没有匹配节点", rowWidth))
	}
	for i := nodeStart; i < nodeEnd; i++ {
		proxyIndex := nodes[i].proxyIndex
		lines = append(lines, m.proxyTableRowAt(group, group.Proxies[proxyIndex], proxyIndex, i == m.proxyCursor, rowWidth))
	}
	return renderPage(lines, m.width, height)
}

type groupSelectorLayout struct {
	capacity  int
	cellWidth int
}

func proxyGroupSelectorLayout(rowWidth, total int) groupSelectorLayout {
	const (
		navigationWidth = 4
		preferredWidth  = 14
		maximumWidth    = 18
		gapWidth        = 1
	)
	available := max(1, rowWidth-navigationWidth)
	capacity := min(max(1, total), max(1, (available+gapWidth)/(preferredWidth+gapWidth)))
	cellWidth := max(1, (available-gapWidth*(capacity-1))/capacity)
	cellWidth = min(cellWidth, maximumWidth)
	return groupSelectorLayout{capacity: capacity, cellWidth: cellWidth}
}

func (m Model) proxyGroupSelector(groups []domain.ProxyGroup, selected, rowWidth int) string {
	return m.proxyGroupSelectorByName(len(groups), selected, rowWidth, func(index int) string { return groups[index].Name })
}

func (m Model) proxyGroupViewSelector(groups []proxyGroupView, selected, rowWidth int) string {
	return m.proxyGroupSelectorByName(len(groups), selected, rowWidth, func(index int) string {
		return m.groups[groups[index].groupIndex].Name
	})
}

func (m Model) proxyGroupSelectorByName(total, selected, rowWidth int, nameAt func(int) string) string {
	rowWidth = max(0, rowWidth)
	if rowWidth == 0 || total == 0 {
		return ""
	}
	layout := proxyGroupSelectorLayout(rowWidth, total)
	start, end := viewportBounds(total, selected, m.groupOffset, layout.capacity)
	parts := make([]string, 0, end-start)
	for index := start; index < end; index++ {
		current := index == selected
		marker := "  "
		if current {
			marker = "> "
		}
		nameWidth := max(1, layout.cellWidth-lipgloss.Width(marker))
		label := marker + stableTerminalCell(empty(nameAt(index), "--"), nameWidth)
		style := lipgloss.NewStyle().Foreground(colors().muted)
		if current {
			style = lipgloss.NewStyle().Bold(true).Foreground(colors().onAccent).Background(colors().accent)
			if colorsDisabled() {
				style = style.Reverse(true)
			}
		}
		parts = append(parts, style.Render(label))
	}

	left, right := "[ ", " ]"
	if start > 0 {
		left = "< "
	}
	if end < total {
		right = " >"
	}
	contentWidth := max(0, rowWidth-lipgloss.Width(left)-lipgloss.Width(right))
	content := padDualWidth(strings.Join(parts, " "), contentWidth)
	return padDualWidth(lipgloss.NewStyle().Foreground(colors().muted).Render(left)+
		content+lipgloss.NewStyle().Foreground(colors().muted).Render(right), rowWidth)
}

// Bubble Tea supports both grapheme and legacy wcwidth renderers. Reserving
// the larger width keeps an emoji-bearing group label on one physical line.
func stableTerminalCell(value string, width int) string {
	width = max(0, width)
	value = safeText(value)
	if dualWidth(value) <= width {
		return padDualWidth(value, width)
	}

	const tail = "~"
	available := max(0, width-dualWidth(tail))
	var result strings.Builder
	graphemeWidth, wcWidth := 0, 0
	for len(value) > 0 {
		cluster, clusterGraphemeWidth := ansi.FirstGraphemeCluster(value, ansi.GraphemeWidth)
		_, clusterWCWidth := ansi.FirstGraphemeCluster(cluster, ansi.WcWidth)
		if graphemeWidth+clusterGraphemeWidth > available || wcWidth+clusterWCWidth > available {
			break
		}
		result.WriteString(cluster)
		graphemeWidth += clusterGraphemeWidth
		wcWidth += clusterWCWidth
		value = value[len(cluster):]
	}
	if width > 0 {
		result.WriteString(tail)
	}
	return padDualWidth(result.String(), width)
}

func dualWidth(value string) int {
	return max(ansi.StringWidth(value), ansi.StringWidthWc(value))
}

func padDualWidth(value string, width int) string {
	return value + strings.Repeat(" ", max(0, width-dualWidth(value)))
}

func (m Model) renderProfiles(height int) string {
	if len(m.profiles) == 0 {
		return emptyState("还没有配置", "按 a 添加订阅、本地文件或节点链接", m.width, height)
	}
	rowWidth := max(1, m.width-4)
	nameWidth, widths := profileColumnWidths(rowWidth - 2)
	lines := []string{
		titleLine("配置", listPosition(m.profileCursor, len(m.profiles)), rowWidth),
		tableHeader([]string{"", "名称", "类型", "更新时间", "额度", "到期"}, widths, rowWidth),
	}
	start, end := viewportBounds(len(m.profiles), m.profileCursor, m.profileOffset, m.listCapacity())
	for i := start; i < end; i++ {
		profile := m.profiles[i]
		state := "·"
		if profile.Active {
			state = "◆"
		}
		updated := "--"
		if !profile.LastUpdated.IsZero() {
			updated = profile.LastUpdated.Local().Format("01-02 15:04")
		}
		kind := map[domain.ProfileKind]string{domain.ProfileLocal: "本地", domain.ProfileRemote: "订阅", domain.ProfileURI: "节点"}[profile.Kind]
		if kind == "" {
			kind = "其他"
		}
		quota, quotaColor := profileQuota(profile.Subscription)
		expiry, expiryColor := profileExpiry(profile.Subscription.Expire)
		cells := []tableCell{
			{text: state, width: widths[0], foreground: colors().accent, bold: profile.Active},
			{text: profile.Name, width: nameWidth},
			{text: kind, width: widths[2]},
			{text: updated, width: widths[3]},
			{text: quota, width: widths[4], alignment: tableAlignRight, foreground: quotaColor},
			{text: expiry, width: widths[5], alignment: tableAlignRight, foreground: expiryColor},
		}
		lines = append(lines, tableDataRow(i == m.profileCursor, cells, rowWidth))
	}
	return renderPage(lines, m.width, height)
}

func (m Model) renderConnections(height int) string {
	connections := m.currentConnectionViews()
	if len(connections) == 0 {
		detail := "连接建立后会自动显示"
		if m.filter != "" && len(m.connections) > 0 {
			detail = "没有匹配当前筛选条件的连接"
		}
		return emptyState("当前没有活动连接", detail, m.width, height)
	}
	rowWidth := max(1, m.width-4)
	targetWidth, processWidth, ruleWidth := connectionColumnWidths(rowWidth - 2)
	widths := []int{targetWidth, processWidth, 11, ruleWidth}
	lines := []string{
		titleLine("活动连接", listPosition(m.connectionCursor, len(connections)), rowWidth),
		tableHeader([]string{"目标", "进程 / 网络", "流量", "规则"}, widths, rowWidth),
	}
	start, end := viewportBounds(len(connections), m.connectionCursor, m.connectionOffset, m.listCapacity())
	for i := start; i < end; i++ {
		connection := m.connections[connections[i].connectionIndex]
		target := connection.Host
		if target == "" {
			target = connection.Destination
		}
		process := empty(connection.Process, connection.Network)
		cells := []tableCell{
			{text: target, width: targetWidth},
			{text: process, width: processWidth},
			{text: formatBytes(connection.Upload + connection.Download), width: 11, alignment: tableAlignRight},
			{text: empty(connection.Rule, "--"), width: ruleWidth},
		}
		lines = append(lines, tableDataRow(i == m.connectionCursor, cells, rowWidth))
	}
	return renderPage(lines, m.width, height)
}

func (m Model) renderLogs(height int) string {
	c := colors()
	logCount := m.logViewLen()
	state := strings.ToUpper(empty(m.logLevel, "info"))
	if !m.status.Service.Active {
		state += "  服务已停止"
	} else if m.logConnecting {
		state += "  连接中"
	} else if m.logReconnectPending {
		state += "  重连中"
	}
	if m.logPaused {
		state += "  已暂停"
		if m.logUnread > 0 {
			state += fmt.Sprintf("  +%d 未读", m.logUnread)
		}
	}
	innerWidth := max(1, m.width-4)
	visible := max(1, height-4)
	end := clamp(logCount-m.logOffset, 0, logCount)
	start := max(0, end-visible)
	position := "等待数据"
	if logCount > 0 {
		position = fmt.Sprintf("%d-%d/%d", start+1, end, logCount)
	}
	lines := []string{
		titleLine("实时日志", state+"  "+position, innerWidth),
		lipgloss.NewStyle().Foreground(c.muted).Render(padRight("  时间     级别   消息", innerWidth)),
	}
	for index := start; index < end; index++ {
		entry := m.logViewEntry(index)
		style := lipgloss.NewStyle().Foreground(c.text)
		switch strings.ToLower(entry.Level) {
		case "error":
			style = style.Foreground(c.bad)
		case "warning", "warn":
			style = style.Foreground(c.warn)
		case "debug":
			style = style.Foreground(c.muted)
		}
		prefix := padRight(entry.Time, 8) + " " + padRight(strings.ToUpper(entry.Level), 5)
		messageWidth := max(1, innerWidth-16)
		lines = append(lines, style.Render(prefix+"  "+padRight(entry.Message, messageWidth)))
	}
	if logCount == 0 {
		message := "等待日志..."
		if !m.status.Service.Active {
			message = "Mihomo 服务未运行"
		} else if m.filter != "" && len(m.logs) > 0 {
			message = "没有匹配日志"
		}
		lines = append(lines, lipgloss.NewStyle().Foreground(c.muted).Render(message))
	}
	return renderPage(lines, m.width, height)
}

func (m Model) renderSettings(height int) string {
	rows := m.settingRows()
	rowWidth := max(1, m.width-4)
	meta := listPosition(m.settingCursor, len(rows)) + "  Enter 修改"
	lines := []string{titleLine("设置", meta, rowWidth)}
	start, end := viewportBounds(len(rows), m.settingCursor, m.settingOffset, m.settingsCapacity())
	group := ""
	for i := start; i < end; i++ {
		row := rows[i]
		if row.group != group {
			group = row.group
			lines = append(lines, lipgloss.NewStyle().Foreground(colors().muted).Render(safeText(group)))
		}
		lines = append(lines, selectedRow(i == m.settingCursor, row.name, row.value, rowWidth))
	}
	return renderPage(lines, m.width, height)
}

type settingViewRow struct {
	id          settingID
	group       string
	name, value string
}

func (m Model) settingRows() []settingViewRow {
	known := m.status.ConfigAvailable
	schedule := "未知"
	if m.scheduleOK {
		schedule = toggleLabel(m.schedule.Enabled)
	}
	mode, tun, port := "未知", "未知", "未知"
	allowLAN, ipv6, logLevel := "未知", "未知", "未知"
	if known {
		mode = modeLabel(m.status.Mode)
		tun = toggleLabel(m.status.TUN)
		port = portLabel(m.status.MixedPort)
		allowLAN = toggleLabel(m.status.AllowLAN)
		ipv6 = toggleLabel(m.status.IPv6)
		logLevel = strings.ToUpper(empty(m.status.LogLevel, "info"))
	}
	service, startup := "未知", "未知"
	if m.serviceStatusKnown() {
		service = toggleLabel(m.status.Service.Active)
		startup = toggleLabel(m.status.Service.Enabled)
	}
	return []settingViewRow{
		{id: settingService, group: "服务与运行", name: "Mihomo 服务", value: service},
		{id: settingStartup, group: "服务与运行", name: "开机启动", value: startup},
		{id: settingMode, group: "服务与运行", name: "运行模式", value: mode},
		{id: settingTUN, group: "服务与运行", name: "TUN 透明代理", value: tun},
		{id: settingSchedule, group: "服务与运行", name: "订阅定时更新", value: schedule},
		{id: settingMixedPort, group: "监听与网络", name: "混合端口", value: port},
		{id: settingAllowLAN, group: "监听与网络", name: "允许局域网", value: allowLAN},
		{id: settingIPv6, group: "监听与网络", name: "IPv6", value: ipv6},
		{id: settingLogLevel, group: "日志", name: "日志级别", value: logLevel},
	}
}

func (m Model) renderFooter() string {
	c := colors()
	message := ""
	messageStyle := lipgloss.NewStyle().Foreground(c.muted)
	if m.quitPending {
		message = "正在取消并完成恢复…"
	} else if m.authorizing {
		message = "等待 sudo 授权…"
	} else if m.privilegedOperation {
		message = "正在执行管理员操作…"
	} else if m.loading {
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
		line := " " + fitLine(hints, contentWidth) + " "
		return lipgloss.NewStyle().Foreground(c.muted).Render(fitLine(line, m.width))
	}

	essentials := fitFooterHints(nil, contentWidth)
	messageWidth := max(0, contentWidth-lipgloss.Width(essentials)-2)
	message = ansi.Truncate(message, messageWidth, "…")
	gap := max(0, contentWidth-lipgloss.Width(message)-lipgloss.Width(essentials))
	line := " " + messageStyle.Render(message) + strings.Repeat(" ", gap) +
		lipgloss.NewStyle().Foreground(c.muted).Render(essentials) + " "
	return fitLine(line, m.width)
}

func (m Model) pageHints() []string {
	switch m.page {
	case pageOverview:
		return []string{"Enter启停", "r刷新", "Tab切页"}
	case pageProxies:
		return []string{"↑↓选", "←→组", "Enter切换", "t测速", "/筛选"}
	case pageProfiles:
		return []string{"↑↓选", "a添加", "u更新", "d删除", "Enter激活"}
	case pageConnections:
		return []string{"↑↓选", "Enter关闭", "x全部", "/筛选"}
	case pageLogs:
		pause := "Space暂停"
		if m.logPaused {
			pause = "Space继续"
		}
		return []string{pause, "/筛选", "r刷新", "PgUp/PgDn浏览", "Home/End首尾"}
	case pageSettings:
		return []string{"↑↓选", "Enter修改", "r刷新"}
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
	contentLines := strings.Split(content, "\n")
	contentWidth := 0
	for _, line := range contentLines {
		contentWidth = max(contentWidth, lipgloss.Width(line))
	}
	boxWidth := min(max(44, contentWidth+6), max(8, m.width-4))
	boxHeight := min(max(5, len(contentLines)+4), max(5, m.height-2))
	innerWidth := max(0, boxWidth-6)
	innerHeight := max(0, boxHeight-4)
	content = fitBlock(content, innerWidth, innerHeight)

	c := colors()
	borderStyle := lipgloss.NewStyle().Foreground(c.accent)
	fillStyle := lipgloss.NewStyle().Foreground(c.text).Background(c.surface)
	boxLines := []string{borderStyle.Render("╭" + strings.Repeat("─", max(0, boxWidth-2)) + "╮")}
	blank := fillStyle.Render(strings.Repeat(" ", max(0, boxWidth-2)))
	boxLines = append(boxLines, borderStyle.Render("│")+blank+borderStyle.Render("│"))
	for _, line := range strings.Split(content, "\n") {
		inside := fillStyle.Render("  " + fitLine(line, innerWidth) + "  ")
		boxLines = append(boxLines, borderStyle.Render("│")+inside+borderStyle.Render("│"))
	}
	boxLines = append(boxLines,
		borderStyle.Render("│")+blank+borderStyle.Render("│"),
		borderStyle.Render("╰"+strings.Repeat("─", max(0, boxWidth-2))+"╯"),
	)
	return centeredBlock(strings.Join(boxLines, "\n"), m.width, m.height)
}

func (m Model) renderInput() string {
	title := "筛选"
	if m.inMode == inputProfile {
		title = "添加配置"
	} else if m.inMode == inputMixedPort {
		title = "修改混合端口"
	}
	inputWidth := m.inputFieldWidth()
	inputView := m.input.View()
	field := lipgloss.NewStyle().Foreground(colors().text).Background(colors().surfaceAlt).
		Render(" " + fitLine(inputView, max(1, inputWidth-2)) + " ")
	lines := []string{sectionTitle(title), "", field}
	if m.inputError != "" {
		lines = append(lines, "", lipgloss.NewStyle().Foreground(colors().bad).Render(safeText(m.inputError)))
	}
	lines = append(lines, "", "Enter 确认  ·  Esc 取消")
	return strings.Join(lines, "\n")
}

func (m Model) renderPicker() string {
	title := "选择设置"
	switch m.picker {
	case pickerTUN:
		title = "TUN 透明代理"
	case pickerAllowLAN:
		title = "允许局域网"
	case pickerIPv6:
		title = "IPv6"
	case pickerLogLevel:
		title = "日志级别"
	}
	lines := []string{sectionTitle(title), ""}
	for index, option := range m.pickerOptions() {
		lines = append(lines, selectedRow(index == m.pickerCursor, option.label, "", 28))
	}
	lines = append(lines, "", "↑↓ 选择  ·  Enter 确认  ·  Esc 取消")
	return strings.Join(lines, "\n")
}

func (m Model) confirmText() string {
	message := "确认执行此操作？"
	switch m.confirm {
	case confirmRemoveProfile:
		message = "删除配置？"
	case confirmCloseConnection:
		message = "关闭连接？"
	case confirmCloseAll:
		message = "关闭全部活动连接？"
	case confirmStopService:
		message = "停止 Mihomo 服务？"
	case confirmEnableLAN:
		message = "开启局域网访问？\n代理端口将对同一局域网开放。\n控制器仍仅限本机。"
	}
	if m.confirmTarget.label != "" && m.confirm != confirmCloseAll && m.confirm != confirmStopService {
		message += "\n" + lipgloss.NewStyle().Foreground(colors().muted).Render(
			truncate(m.confirmTarget.label, max(12, m.width-20)),
		)
	}
	return sectionTitle("确认") + "\n\n" + message + "\n\ny / Enter 确认  ·  n / Esc 取消"
}

func (m Model) helpText() string {
	lines := []string{sectionTitle("快捷键"), ""}
	if m.width < 90 {
		lines = append(lines,
			"Tab / Shift+Tab / h l 切页  ↑↓ / jk 选择",
			"Home / End 首尾    PgUp / PgDn 翻页",
			"Enter 执行 / 修改  r 刷新",
			"/ 筛选             t 测速",
			"节点页 ←→ / [ ] 切组  其他页 ←→ 切页",
			"a / u / d 配置",
			"Space 暂停日志     x 关闭全部连接",
			"? / Esc 关闭帮助   q 退出",
		)
		return strings.Join(lines, "\n")
	}
	lines = append(lines,
		"Tab / Shift+Tab / h l  切换页面          Home / End     移到首尾",
		"j k / ↑ ↓     移动选择          PgUp / PgDn     翻页浏览",
		"Enter          执行或修改当前项  r               刷新",
		"/              筛选              t               测试策略组延迟",
		"节点页 ← → / [ ]  切换策略组        其他页 ← →      切换页面",
		"a / u / d       添加、更新、删除配置",
		"Space          暂停或继续日志    x               关闭全部连接",
		"? / Esc        关闭帮助          q               退出",
	)
	return strings.Join(lines, "\n")
}

func sectionTitle(text string) string {
	c := colors()
	return lipgloss.NewStyle().Bold(true).Foreground(c.accent).Render(safeText(text))
}

func titleLine(title, meta string, width int) string {
	title = sectionTitle(title)
	if meta == "" {
		return fitLine(title, width)
	}
	meta = lipgloss.NewStyle().Foreground(colors().muted).Render(safeText(meta))
	return joinSides(title, meta, width)
}

func mutedLine(text string, width int) string {
	text = padRight(text, width)
	return lipgloss.NewStyle().Foreground(colors().muted).Render(text)
}

type tableCell struct {
	text       string
	width      int
	alignment  tableAlignment
	foreground color.Color
	bold       bool
}

type tableAlignment uint8

const (
	tableAlignLeft tableAlignment = iota
	tableAlignCenter
	tableAlignRight
)

func tableHeader(labels []string, widths []int, rowWidth int) string {
	cells := make([]tableCell, 0, len(widths))
	for index, width := range widths {
		label := ""
		if index < len(labels) {
			label = labels[index]
		}
		cells = append(cells, tableCell{text: label, width: width})
	}
	return tableHeaderCells(cells, rowWidth)
}

func tableHeaderCells(cells []tableCell, rowWidth int) string {
	parts := make([]string, 0, len(cells))
	for _, cell := range cells {
		parts = append(parts, alignTableCell(cell.text, cell.width, cell.alignment))
	}
	content := strings.Join(parts, " ")
	line := "  " + fitLine(content, max(0, rowWidth-2))
	return lipgloss.NewStyle().Foreground(colors().muted).Render(fitLine(line, rowWidth))
}

func alignTableCell(value string, width int, alignment tableAlignment) string {
	switch alignment {
	case tableAlignCenter:
		return centerText(value, width)
	case tableAlignRight:
		return padLeft(value, width)
	default:
		return padRight(value, width)
	}
}

func tableDataRow(selected bool, cells []tableCell, rowWidth int) string {
	plainParts := make([]string, 0, len(cells))
	styledParts := make([]string, 0, len(cells))
	for _, cell := range cells {
		value := safeText(cell.text)
		value = alignTableCell(value, cell.width, cell.alignment)
		plainParts = append(plainParts, value)
		style := lipgloss.NewStyle()
		if cell.foreground != nil {
			style = style.Foreground(cell.foreground)
		}
		if cell.bold {
			style = style.Bold(true)
		}
		styledParts = append(styledParts, style.Render(value))
	}
	return selectableRow(selected, strings.Join(plainParts, " "), strings.Join(styledParts, " "), rowWidth)
}

func selectableRow(selected bool, plain, styled string, width int) string {
	width = max(0, width)
	contentWidth := max(0, width-2)
	plain = fitLine(safeText(plain), contentWidth)
	if selected {
		return nodeHighlightStyle(width).Render("> " + plain)
	}
	styled = fitLine(styled, contentWidth)
	return "  " + styled
}

func profileColumnWidths(contentWidth int) (int, []int) {
	const (
		stateWidth   = 1
		kindWidth    = 4
		updatedWidth = 11
		quotaWidth   = 12
		expiryWidth  = 11
		gapCount     = 5
	)
	nameWidth := max(4, contentWidth-stateWidth-kindWidth-updatedWidth-quotaWidth-expiryWidth-gapCount)
	return nameWidth, []int{stateWidth, nameWidth, kindWidth, updatedWidth, quotaWidth, expiryWidth}
}

func profileQuota(info domain.SubscriptionInfo) (string, color.Color) {
	c := colors()
	used := nonNegativeTraffic(info.Upload) + nonNegativeTraffic(info.Download)
	if info.Total <= 0 {
		if used == 0 {
			return "--", c.muted
		}
		return "已用 " + formatCompactBytes(used), c.muted
	}
	total := nonNegativeTraffic(info.Total)
	text := formatCompactBytes(used) + "/" + formatCompactBytes(total)
	ratio := float64(used) / float64(max64(1, total))
	switch {
	case ratio >= 1:
		return text, c.bad
	case ratio >= 0.8:
		return text, c.warn
	default:
		return text, c.good
	}
}

func profileExpiry(expiry time.Time) (string, color.Color) {
	c := colors()
	if expiry.IsZero() {
		return "--", c.muted
	}
	expiry = expiry.Local()
	remaining := time.Until(expiry)
	if remaining < 0 {
		return "已过期", c.bad
	}
	if remaining < 7*24*time.Hour {
		return expiry.Format("01-02 15:04"), c.warn
	}
	return expiry.Format("2006-01-02"), c.text
}

func connectionColumnWidths(contentWidth int) (target, process, rule int) {
	const trafficWidth = 11
	dynamic := max(3, contentWidth-trafficWidth-3)
	target = max(1, dynamic*45/100)
	process = max(1, dynamic*28/100)
	rule = max(1, dynamic-target-process)
	return target, process, rule
}

func proxyColumnWidths(contentWidth int) []int {
	const (
		activeWidth = 1
		aliveWidth  = 4
		testWidth   = 10
		typeWidth   = 10
		gapCount    = 4
	)
	nameWidth := max(4, contentWidth-activeWidth-aliveWidth-testWidth-typeWidth-gapCount)
	return []int{activeWidth, aliveWidth, testWidth, typeWidth, nameWidth}
}

func proxyTableHeader(rowWidth int) string {
	widths := proxyColumnWidths(max(0, rowWidth-2))
	return tableHeaderCells([]tableCell{
		{text: "", width: widths[0], alignment: tableAlignCenter},
		{text: "在线", width: widths[1], alignment: tableAlignCenter},
		{text: "测速", width: widths[2]},
		{text: "类型", width: widths[3]},
		{text: "节点", width: widths[4]},
	}, rowWidth)
}

func (m Model) proxyTableRowAt(group domain.ProxyGroup, proxy domain.Proxy, sourceIndex int, selected bool, rowWidth int) string {
	c := colors()
	active := ""
	if group.Now == proxy.Name {
		active = "*"
	}
	alive, aliveColor := "否", c.bad
	if proxy.Alive {
		alive, aliveColor = "是", c.good
	}
	testState, delay := m.proxyDisplayState(group.Name, group, sourceIndex)
	test, testColor := proxyTestLabel(testState, delay)
	widths := proxyColumnWidths(max(0, rowWidth-2))
	cells := []tableCell{
		{text: active, width: widths[0], alignment: tableAlignCenter, foreground: c.accent, bold: active != ""},
		{text: alive, width: widths[1], alignment: tableAlignCenter, foreground: aliveColor},
		{text: test, width: widths[2], foreground: testColor},
		{text: empty(proxy.Type, "--"), width: widths[3]},
		{text: proxy.Name, width: widths[4]},
	}
	return tableDataRow(selected, cells, rowWidth)
}

func proxyTestLabel(state proxyTestState, delay uint16) (string, color.Color) {
	c := colors()
	switch state {
	case proxyTestSuccess:
		if delay == 0 {
			return "成功", c.good
		}
		return fmt.Sprintf("%d ms", delay), c.good
	case proxyTestTimeout:
		return "超时", c.warn
	case proxyTestFailed:
		return "失败", c.bad
	default:
		if delay > 0 {
			return fmt.Sprintf("核心 %dms", delay), c.muted
		}
		return "未测试", c.muted
	}
}

func listPosition(cursor, total int) string {
	if total <= 0 {
		return "0/0"
	}
	return fmt.Sprintf("%d/%d", clamp(cursor, 0, total-1)+1, total)
}

func boolPointer(value bool) *bool { return &value }

func scheduleToggle(known, enabled bool) *bool {
	if !known {
		return nil
	}
	return boolPointer(enabled)
}

func toggleLabel(enabled bool) string {
	if enabled {
		return "● 开启"
	}
	return "○ 关闭"
}

func selectedRow(selected bool, name, detail string, width int) string {
	name, detail = safeText(name), safeText(detail)
	contentWidth := max(0, width-2)
	detailWidth := min(max(8, lipgloss.Width(detail)), max(8, contentWidth/2))
	nameWidth := max(1, contentWidth-detailWidth-2)
	line := padRight(name, nameWidth) + "  " + padLeft(detail, detailWidth)
	return selectableRow(selected, line, line, width)
}

func selectedNodeRow(selected bool, name, detail string, width int) string {
	name, detail = safeText(name), safeText(detail)
	contentWidth := max(0, width-2)
	detailWidth := min(max(8, lipgloss.Width(detail)), max(8, contentWidth/2))
	nameWidth := max(1, contentWidth-detailWidth-2)
	line := padRight(name, nameWidth) + "  " + padLeft(detail, detailWidth)
	return highlightNodeRow(selected, line, width)
}

func highlightNodeRow(selected bool, line string, width int) string {
	width = max(0, width)
	contentWidth := max(0, width-2)
	line = fitLine(line, contentWidth)
	if !selected {
		return "  " + line
	}
	return nodeHighlightStyle(width).Render("> " + line)
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
	details = fitLine(safeText(details), detailWidth)
	health := "-"
	if alive {
		health = "+"
	}
	return nodeHighlightStyle(width).Render("> " + health + details)
}

func padRight(value string, width int) string {
	value = truncate(value, width)
	return value + strings.Repeat(" ", max(0, width-lipgloss.Width(value)))
}

func padLeft(value string, width int) string {
	value = truncate(value, width)
	return strings.Repeat(" ", max(0, width-lipgloss.Width(value))) + value
}

func emptyState(title, detail string, dimensions ...int) string {
	width, height := 60, 10
	if len(dimensions) >= 2 {
		width, height = dimensions[0], dimensions[1]
	}
	c := colors()
	content := lipgloss.NewStyle().Bold(true).Foreground(c.text).Render(safeText(title)) + "\n" +
		lipgloss.NewStyle().Foreground(c.muted).Render(safeText(detail))
	return centeredBlock(content, width, height)
}

func fitBlock(value string, width, height int) string {
	width, height = max(0, width), max(0, height)
	if height == 0 {
		return ""
	}
	lines := strings.Split(value, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for i, line := range lines {
		lines[i] = fitLine(line, width)
	}
	for len(lines) < height {
		lines = append(lines, strings.Repeat(" ", width))
	}
	return strings.Join(lines, "\n")
}

func fitLine(value string, width int) string {
	width = max(0, width)
	value = ansi.Truncate(value, width, "")
	return value + strings.Repeat(" ", max(0, width-lipgloss.Width(value)))
}

func centeredBlock(value string, width, height int) string {
	width, height = max(0, width), max(0, height)
	if height == 0 {
		return ""
	}
	lines := strings.Split(value, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	blockWidth := 0
	for _, line := range lines {
		blockWidth = max(blockWidth, min(width, lipgloss.Width(line)))
	}
	top := max(0, (height-len(lines))/2)
	result := make([]string, 0, height)
	for range top {
		result = append(result, strings.Repeat(" ", width))
	}
	left := max(0, (width-blockWidth)/2)
	for _, line := range lines {
		line = fitLine(line, blockWidth)
		result = append(result, fitLine(strings.Repeat(" ", left)+line, width))
	}
	for len(result) < height {
		result = append(result, strings.Repeat(" ", width))
	}
	return strings.Join(result, "\n")
}

func renderPage(rows []string, width, height int) string {
	width, height = max(0, width), max(0, height)
	if height == 0 {
		return ""
	}
	if width < 4 || height < 2 {
		return fitBlock(strings.Join(rows, "\n"), width, height)
	}
	innerWidth, innerHeight := width-4, height-2
	content := fitBlock(strings.Join(rows, "\n"), innerWidth, innerHeight)
	result := make([]string, 0, height)
	result = append(result, strings.Repeat(" ", width))
	for _, line := range strings.Split(content, "\n") {
		result = append(result, "  "+fitLine(line, innerWidth)+"  ")
	}
	result = append(result, strings.Repeat(" ", width))
	return strings.Join(result[:height], "\n")
}

func renderSplitPage(left, right []string, leftWidth, rightWidth, width, height int) string {
	width, height = max(0, width), max(0, height)
	if height == 0 {
		return ""
	}
	if width < 5 || height < 2 {
		return fitBlock(strings.Join(right, "\n"), width, height)
	}
	innerHeight := height - 2
	leftBlock := strings.Split(fitBlock(strings.Join(left, "\n"), leftWidth, innerHeight), "\n")
	rightBlock := strings.Split(fitBlock(strings.Join(right, "\n"), rightWidth, innerHeight), "\n")
	divider := lipgloss.NewStyle().Foreground(colors().border).Render("│")
	result := make([]string, 0, height)
	result = append(result, strings.Repeat(" ", width))
	for index := range innerHeight {
		line := "  " + leftBlock[index] + divider + rightBlock[index] + "  "
		result = append(result, fitLine(line, width))
	}
	result = append(result, strings.Repeat(" ", width))
	return strings.Join(result[:height], "\n")
}

func joinSides(left, right string, width int) string {
	width = max(0, width)
	if width == 0 {
		return ""
	}
	left = ansi.Truncate(left, width, "")
	if right == "" {
		return fitLine(left, width)
	}
	leftWidth, rightWidth := lipgloss.Width(left), lipgloss.Width(right)
	if leftWidth+rightWidth+2 > width {
		reservedRight := min(rightWidth, max(8, width/3))
		right = ansi.Truncate(right, reservedRight, "…")
		left = ansi.Truncate(left, max(0, width-lipgloss.Width(right)-2), "…")
		leftWidth, rightWidth = lipgloss.Width(left), lipgloss.Width(right)
	}
	if leftWidth+rightWidth > width {
		return fitLine(left, width)
	}
	gap := max(0, width-leftWidth-rightWidth)
	return fitLine(left+strings.Repeat(" ", gap)+right, width)
}

func centerText(value string, width int) string {
	value = truncate(value, width)
	remaining := max(0, width-lipgloss.Width(value))
	left := remaining / 2
	return strings.Repeat(" ", left) + value + strings.Repeat(" ", remaining-left)
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
	clean := true
	for _, character := range value {
		if character == '\x1b' || unicode.IsControl(character) {
			clean = false
			break
		}
	}
	if clean {
		return value
	}
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

func compactOnOff(enabled bool) string {
	if enabled {
		return "开"
	}
	return "关"
}

func portLabel(port int) string {
	if port <= 0 {
		return "未设置"
	}
	return fmt.Sprintf("%d", port)
}

func formatRate(bytes int64) string { return formatBytes(bytes) + "/s" }

func formatCompactBytes(bytes int64) string {
	if bytes < 0 {
		bytes = 0
	}
	units := []string{"B", "K", "M", "G", "T", "P"}
	value := float64(bytes)
	unit := 0
	for value >= 1024 && unit < len(units)-1 {
		value /= 1024
		unit++
	}
	if unit == 0 {
		return fmt.Sprintf("%d%s", bytes, units[unit])
	}
	if value >= 100 {
		return fmt.Sprintf("%.0f%s", value, units[unit])
	}
	return fmt.Sprintf("%.1f%s", value, units[unit])
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

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
