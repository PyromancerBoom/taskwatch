package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"taskwatch/internal/core"
)

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
	if m.startupErr != nil {
		lines := renderWrapped(m.palette.focus, "TASKWATCH", width)
		lines = append(lines, "")
		lines = append(lines, renderWrapped(m.palette.failure, "Unable to start TaskWatch", width)...)
		lines = append(lines, renderWrapped(m.palette.failure, m.startupErr.Error(), width)...)
		lines = append(lines, "")
		lines = append(lines, renderWrapped(m.palette.muted, "Ctrl+C close (apps keep running)", width)...)
		return strings.Join(lines, "\n")
	}
	if !m.started {
		return strings.Join([]string{
			m.palette.focus.Render(clip("TASKWATCH", width)),
			"",
			m.palette.muted.Render(clip("Starting TaskWatch…", width)),
		}, "\n")
	}

	status := "Updated " + m.snapshot.RefreshedAt.Format("15:04:05")
	if m.refreshing {
		status = "Refreshing…"
	}
	header := fmt.Sprintf("TASKWATCH  %s  %s", m.snapshot.Workspace, status)
	lines := renderWrapped(m.palette.focus, header, width)
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
			lines = append(lines, m.palette.muted.Render(clip(fullHeading(), width)))
			for _, session := range m.snapshot.Sessions {
				lines = append(lines, m.renderRow(session, fullRow(session, m.now()), width))
			}
		case width >= 80:
			lines = append(lines, m.palette.muted.Render(clip(mediumHeading(), width)))
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
	lines = append(lines, renderWrapped(m.palette.muted, "↑/↓ or j/k move  •  r refresh  •  Ctrl+C close (apps keep running)", width)...)
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
	return fmt.Sprintf("  %-13s %-8s %-12s %-9s %-11s %-10s %-5s %s", "APP", "TRACKING", "STATUS", "PROCESSES", "CPU", "MEMORY", "AGE", "FOLDER")
}

func fullRow(session core.Session, now time.Time) string {
	return fmt.Sprintf("  %-13s %-8s %-12s %-9d %-11s %-10s %-5s %s",
		toolLabel(session.Tool), trackingLabel(session.Confidence), string(session.State), len(session.Processes),
		cpuLabel(session), memoryLabel(session.Resources), ageLabel(session.StartedAt, now), folder(session))
}

func mediumHeading() string {
	return fmt.Sprintf("  %-13s %-8s %-12s %-4s %-10s %-9s %-4s", "APP", "TRACKING", "STATUS", "PROC", "CPU", "MEMORY", "AGE")
}

func mediumRow(session core.Session, now time.Time) string {
	return fmt.Sprintf("  %-13s %-8s %-12s %-4d %-10s %-9s %-4s",
		toolLabel(session.Tool), trackingLabel(session.Confidence), string(session.State), len(session.Processes),
		cpuLabel(session), memoryLabel(session.Resources), ageLabel(session.StartedAt, now))
}

func narrowRows(session core.Session, now time.Time) (string, string) {
	first := fmt.Sprintf("  %s  %s", toolLabel(session.Tool), string(session.State))
	second := fmt.Sprintf("  %s %dp %s %s %s %s", trackingLabel(session.Confidence), len(session.Processes),
		cpuLabel(session), memoryLabel(session.Resources), ageLabel(session.StartedAt, now), folder(session))
	return first, second
}

func (m Model) renderRow(session core.Session, line string, width int) string {
	line = clip(line, width)
	marker := "  "
	if session.ID == m.selectedID {
		marker = m.palette.focus.Render("▌") + " "
	}
	if strings.HasPrefix(line, "  ") {
		line = strings.TrimPrefix(line, "  ")
	}
	available := width - 2
	if available < 0 {
		available = 0
	}
	return marker + m.stateStyle(session.State).Render(clip(line, available))
}

func (m Model) stateStyle(state core.State) lipgloss.Style {
	switch state {
	case core.StateRunning:
		return m.palette.healthy
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
		return "found"
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
