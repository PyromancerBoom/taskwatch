package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"taskwatch/internal/core"
)

// brand is the application wordmark shown in the header. It stays contiguous
// with the workspace and status that follow it so the header reads as one line.
const brand = "TASKWATCH"

const footerHint = "↑/↓ or j/k move  •  r refresh  •  Ctrl+C close (apps keep running)"

type palette struct {
	dark    bool
	text    lipgloss.Style
	muted   lipgloss.Style
	focus   lipgloss.Style
	healthy lipgloss.Style
	warning lipgloss.Style
	failure lipgloss.Style
}

func newPalette(dark bool) palette {
	colors := struct {
		text, muted, focus, healthy, warning, failure string
	}{
		text: "#1F2937", muted: "#64748B", focus: "#0891B2",
		healthy: "#15803D", warning: "#B45309", failure: "#DC2626",
	}
	if dark {
		colors = struct {
			text, muted, focus, healthy, warning, failure string
		}{
			text: "#E5E7EB", muted: "#94A3B8", focus: "#22D3EE",
			healthy: "#4ADE80", warning: "#FBBF24", failure: "#F87171",
		}
	}
	return palette{
		dark:    dark,
		text:    lipgloss.NewStyle().Foreground(lipgloss.Color(colors.text)),
		muted:   lipgloss.NewStyle().Foreground(lipgloss.Color(colors.muted)),
		focus:   lipgloss.NewStyle().Foreground(lipgloss.Color(colors.focus)).Bold(true),
		healthy: lipgloss.NewStyle().Foreground(lipgloss.Color(colors.healthy)),
		warning: lipgloss.NewStyle().Foreground(lipgloss.Color(colors.warning)),
		failure: lipgloss.NewStyle().Foreground(lipgloss.Color(colors.failure)),
	}
}

func (m Model) View() tea.View {
	view := tea.NewView(m.render())
	view.AltScreen = true
	view.WindowTitle = "TaskWatch"
	return view
}

func (m Model) render() string {
	width := m.width
	if width < 1 {
		width = 80
	}
	title := m.palette.focus
	if m.startupErr != nil {
		lines := renderWrapped(title, brand, width)
		lines = append(lines, "")
		lines = append(lines, renderWrapped(m.palette.failure, "Unable to start TaskWatch", width)...)
		lines = append(lines, renderWrapped(m.palette.failure, m.startupErr.Error(), width)...)
		lines = append(lines, "")
		lines = append(lines, renderWrapped(m.palette.muted, "Ctrl+C close (apps keep running)", width)...)
		return strings.Join(lines, "\n")
	}
	if !m.started {
		return strings.Join([]string{
			title.Render(clip(brand, width)),
			"",
			m.palette.muted.Render(clip("Starting TaskWatch…", width)),
		}, "\n")
	}

	status := "Updated " + m.snapshot.RefreshedAt.Format("15:04:05")
	if m.refreshing {
		status = "Refreshing…"
	}
	header := fmt.Sprintf("%s  %s  %s", brand, m.snapshot.Workspace, status)
	lines := renderWrapped(title, header, width)
	lines = append(lines, m.summaryLines(width)...)
	lines = append(lines, "")

	if len(m.snapshot.Warnings) > 0 {
		suffix := ""
		if more := len(m.snapshot.Warnings) - 1; more > 0 {
			suffix = fmt.Sprintf(" +%d more", more)
		}
		const prefix = "Warning: "
		messageWidth := width - lipgloss.Width(prefix) - lipgloss.Width(suffix)
		warning := prefix + clip(m.snapshot.Warnings[0].Message, messageWidth) + suffix
		lines = append(lines, m.palette.warning.Render(warning), "")
	}

	if len(m.snapshot.Sessions) == 0 {
		lines = append(lines, renderWrapped(m.palette.muted, "No sessions found. Watching for Codex Desktop. Refreshing every 2 seconds.", width)...)
	} else {
		switch {
		case width >= 110:
			lines = append(lines, m.palette.muted.Render(clip("  "+fullHeading(), width)))
			for _, session := range m.snapshot.Sessions {
				lines = append(lines, m.renderRow(session, fullRow(session, m.now()), width))
			}
		case width >= 80:
			lines = append(lines, m.palette.muted.Render(clip("  "+mediumHeading(), width)))
			for _, session := range m.snapshot.Sessions {
				lines = append(lines, m.renderRow(session, mediumRow(session, m.now()), width))
				lines = append(lines, m.palette.muted.Render(clip("    "+folder(session), width)))
			}
		default:
			for _, session := range m.snapshot.Sessions {
				first, second := narrowRows(session, m.now())
				lines = append(lines, m.renderRow(session, first, width))
				lines = append(lines, m.palette.muted.Render(clip(second, width)))
			}
		}
	}

	lines = append(lines, "")
	lines = append(lines, renderWrapped(m.palette.muted, footerHint, width)...)
	return strings.Join(lines, "\n")
}

func renderWrapped(style lipgloss.Style, value string, width int) []string {
	lines := wrapDisplayWidth(value, width)
	for i := range lines {
		lines[i] = style.Render(lines[i])
	}
	return lines
}

func wrapDisplayWidth(value string, width int) []string {
	if width <= 0 {
		return []string{""}
	}

	var lines []string
	for _, paragraph := range strings.Split(value, "\n") {
		if paragraph == "" {
			lines = append(lines, "")
			continue
		}

		var line strings.Builder
		lineWidth := 0
		for _, r := range paragraph {
			runeWidth := lipgloss.Width(string(r))
			if lineWidth > 0 && lineWidth+runeWidth > width {
				lines = append(lines, line.String())
				line.Reset()
				lineWidth = 0
			}
			line.WriteRune(r)
			lineWidth += runeWidth
		}
		lines = append(lines, line.String())
	}
	return lines
}

func fullHeading() string {
	return fmt.Sprintf("  %-13s %-8s %-12s %-9s %-12s %-10s %-5s %s", "APP", "TRACKING", "STATUS", "PROCESSES", "CPU", "WS MEMORY", "AGE", "FOLDER")
}

func fullRow(session core.Session, now time.Time) string {
	return fmt.Sprintf("  %-13s %-8s %-12s %-9d %-12s %-10s %-5s %s",
		toolLabel(session.Tool), trackingLabel(session.Confidence), string(session.State), len(session.Processes),
		cpuCell(session), memoryLabel(session.Resources), ageLabel(session.StartedAt, now), folder(session))
}

func mediumHeading() string {
	return fmt.Sprintf("  %-13s %-8s %-12s %-4s %-12s %-9s %-4s", "APP", "TRACKING", "STATUS", "PROC", "CPU", "WS MEMORY", "AGE")
}

func mediumRow(session core.Session, now time.Time) string {
	return fmt.Sprintf("  %-13s %-8s %-12s %-4d %-12s %-9s %-4s",
		toolLabel(session.Tool), trackingLabel(session.Confidence), string(session.State), len(session.Processes),
		cpuCell(session), memoryLabel(session.Resources), ageLabel(session.StartedAt, now))
}

func narrowRows(session core.Session, now time.Time) (string, string) {
	first := fmt.Sprintf("  %s  %s", toolLabel(session.Tool), string(session.State))
	second := fmt.Sprintf("  %s %dp %s %s %s %s", trackingLabel(session.Confidence), len(session.Processes),
		cpuLabel(session), memoryLabel(session.Resources), ageLabel(session.StartedAt, now), folder(session))
	return first, second
}

// renderRow draws one session line: a selection marker, a status dot colored by
// lifecycle state, then the row content. The dot carries the state color for
// every row while healthy row text stays in the default foreground, so color
// still signals attention rather than painting the whole table.
func (m Model) renderRow(session core.Session, line string, width int) string {
	line = clip(line, width)
	if strings.HasPrefix(line, "  ") {
		line = strings.TrimPrefix(line, "  ")
	}
	style := m.stateStyle(session.State)
	if width < 8 {
		return style.Render(clip(line, width))
	}
	available := width - 4
	content := clip(line, available)
	dot := m.dotStyle(session.State).Render("●") + " "

	if session.ID == m.selectedID {
		return m.palette.focus.Render("▌") + " " + dot + style.Bold(true).Render(content)
	}
	return "  " + dot + style.Render(content)
}

// dotStyle colors the per-row status dot for every lifecycle state, including
// the healthy and exited ones that keep default or muted row text.
func (m Model) dotStyle(state core.State) lipgloss.Style {
	switch state {
	case core.StateRunning:
		return m.palette.healthy
	case core.StateStale, core.StateLeftovers:
		return m.palette.warning
	case core.StateInaccessible:
		return m.palette.failure
	default:
		return m.palette.muted
	}
}

// summaryLines renders a one-line overview strip: session count, per-state
// counts with colored dots, aggregate CPU and memory, and a CPU sparkline.
// The colored version is only emitted when the plain text fits the width;
// otherwise a muted clipped fallback is used because clip is not ANSI-safe.
func (m Model) summaryLines(width int) []string {
	sessions := m.snapshot.Sessions
	if len(sessions) == 0 {
		return nil
	}

	type piece struct{ plain, styled string }
	countLabel := fmt.Sprintf("%d sessions", len(sessions))
	if len(sessions) == 1 {
		countLabel = "1 session"
	}
	pieces := []piece{{countLabel, m.palette.text.Render(countLabel)}}

	counts := make(map[core.State]int, len(sessions))
	for _, session := range sessions {
		counts[session.State]++
	}
	for _, state := range []core.State{core.StateRunning, core.StateStale, core.StateLeftovers, core.StateInaccessible, core.StateExited} {
		if n := counts[state]; n > 0 {
			text := fmt.Sprintf("● %d %s", n, string(state))
			pieces = append(pieces, piece{text, m.dotStyle(state).Render(text)})
		}
	}

	cpuTotal, cpuSampled := 0.0, false
	var memTotal core.ResourceUsage
	for _, session := range sessions {
		if session.Resources.CPUAvailable {
			cpuTotal += session.Resources.CPUPercent
			cpuSampled = true
		}
		if session.Resources.MemoryAvailable {
			memTotal.MemoryBytes += session.Resources.MemoryBytes
			memTotal.MemoryAvailable = true
		}
	}
	var totals []string
	if cpuSampled {
		totals = append(totals, fmt.Sprintf("CPU %.1f%%", cpuTotal))
	}
	if memTotal.MemoryAvailable {
		totals = append(totals, "MEM "+memoryLabel(memTotal))
	}
	if len(totals) > 0 {
		text := strings.Join(totals, " · ")
		pieces = append(pieces, piece{text, m.palette.muted.Render(text)})
	}
	if spark := sparkline(m.cpuHistory); spark != "" {
		pieces = append(pieces, piece{spark, m.palette.focus.Render(spark)})
	}

	const separator = "   "
	plainParts := make([]string, len(pieces))
	styledParts := make([]string, len(pieces))
	for i, p := range pieces {
		plainParts[i] = p.plain
		styledParts[i] = p.styled
	}
	plain := strings.Join(plainParts, separator)
	if lipgloss.Width(plain) <= width {
		return []string{strings.Join(styledParts, separator)}
	}
	return []string{m.palette.muted.Render(clip(plain, width))}
}

// cpuGauge draws a five-cell usage bar for a CPU percentage. Any non-zero
// usage lights at least one cell so activity is never rendered as idle.
func cpuGauge(percent float64) string {
	const cells = 5
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	filled := int(percent/100*cells + 0.5)
	if percent > 0 && filled == 0 {
		filled = 1
	}
	return strings.Repeat("▰", filled) + strings.Repeat("▱", cells-filled)
}

// cpuCell pairs the CPU gauge with the numeric label when a sample exists;
// unavailable and sampling placeholders pass through unchanged.
func cpuCell(session core.Session) string {
	if session.Resources.CPUAvailable {
		return cpuGauge(session.Resources.CPUPercent) + " " + cpuLabel(session)
	}
	return cpuLabel(session)
}

// sparkline maps the CPU history onto block glyphs, normalized to the highest
// sample. It stays hidden until there are at least two samples to compare.
func sparkline(values []float64) string {
	if len(values) < 2 {
		return ""
	}
	glyphs := []rune("▁▂▃▄▅▆▇█")
	max := 0.0
	for _, value := range values {
		if value > max {
			max = value
		}
	}
	if max <= 0 {
		max = 1
	}
	var line strings.Builder
	for _, value := range values {
		if value < 0 {
			value = 0
		}
		index := int(value / max * float64(len(glyphs)-1))
		if index >= len(glyphs) {
			index = len(glyphs) - 1
		}
		line.WriteRune(glyphs[index])
	}
	return line.String()
}

// stateStyle keeps healthy sessions in the default foreground and reserves color
// for states that warrant attention, so anomalies stand out instead of a wall
// of color.
func (m Model) stateStyle(state core.State) lipgloss.Style {
	switch state {
	case core.StateStale, core.StateLeftovers:
		return m.palette.warning
	case core.StateInaccessible, core.StateExited:
		return m.palette.failure
	default:
		return m.palette.text
	}
}

func toolLabel(tool core.Tool) string {
	switch tool {
	case core.ToolClaudeCLI:
		return "Claude"
	case core.ToolCodexCLI:
		return "Codex CLI"
	case core.ToolCodexDesktop:
		return "Codex Desktop"
	default:
		return string(tool)
	}
}

func trackingLabel(confidence core.Confidence) string {
	switch confidence {
	case core.ConfidenceSupervised:
		return "managed"
	case core.ConfidenceObserved:
		return "observed"
	default:
		return string(confidence)
	}
}

func cpuLabel(session core.Session) string {
	if session.Resources.CPUAvailable {
		return fmt.Sprintf("%.1f%%", session.Resources.CPUPercent)
	}
	if len(session.Processes) > 0 && (session.State == core.StateRunning || session.State == core.StateLeftovers) {
		return "sampling…"
	}
	return "—"
}

func memoryLabel(resources core.ResourceUsage) string {
	if !resources.MemoryAvailable {
		return "—"
	}
	const unit = 1024
	bytes := resources.MemoryBytes
	switch {
	case bytes < unit:
		return fmt.Sprintf("%d B", bytes)
	case bytes < unit*unit:
		return fmt.Sprintf("%.1f KB", float64(bytes)/unit)
	case bytes < unit*unit*unit:
		return fmt.Sprintf("%.1f MB", float64(bytes)/(unit*unit))
	default:
		return fmt.Sprintf("%.1f GB", float64(bytes)/(unit*unit*unit))
	}
}

func ageLabel(startedAt, now time.Time) string {
	if startedAt.IsZero() {
		return "—"
	}
	age := now.Sub(startedAt)
	if age < 0 {
		age = 0
	}
	switch {
	case age < time.Minute:
		return fmt.Sprintf("%ds", int(age/time.Second))
	case age < time.Hour:
		return fmt.Sprintf("%dm", int(age/time.Minute))
	case age < 24*time.Hour:
		return fmt.Sprintf("%dh", int(age/time.Hour))
	default:
		return fmt.Sprintf("%dd", int(age/(24*time.Hour)))
	}
}

func folder(session core.Session) string {
	if session.Workspace == "" {
		return "—"
	}
	return session.Workspace
}

func clip(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(value) <= width {
		return value
	}
	if width == 1 {
		return "…"
	}
	var result strings.Builder
	for _, r := range value {
		candidate := result.String() + string(r)
		if lipgloss.Width(candidate)+1 > width {
			break
		}
		result.WriteRune(r)
	}
	return result.String() + "…"
}
