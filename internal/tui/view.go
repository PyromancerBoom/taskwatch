package tui

import (
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"taskwatch/internal/app"
	"taskwatch/internal/core"
)

// brand is the application wordmark shown in the header. It stays contiguous
// with the workspace and status that follow it so the header reads as one line.
const brand = "TASKWATCH"

const minimumTerminalWidth = 25

// footerHints are tried widest-first; the first one whose plain text fits the
// width is used, and the result is always clipped to exactly one line so the
// layout budget can treat the footer as a constant-size region (see
// planLayout). This trades footer wrapping for legibility of the key hints at
// every width, including the narrowest supported one.
var footerHints = []string{
	"Enter inspect • c/x launch • r refresh • ↑/↓ move • Ctrl+C close",
	"Enter inspect • c/x • r • ↑↓ • ^C",
	"Enter c/x r ↑↓ ^C",
}

var inspectionFooterHints = []string{
	"↑/↓ scroll  •  PgUp/PgDn page  •  r refresh  •  Enter/Esc back  •  Ctrl+C close",
	"↑/↓ scroll • PgUp/PgDn • r refresh • Enter/Esc back • ^C close",
	"↑↓ • r • Enter/Esc • ^C",
}

var workspacePickerFooterHints = []string{
	"↑/↓ move  •  PgUp/PgDn page  •  Enter launch here  •  Esc back  •  Ctrl+C close",
	"↑/↓ • PgUp/PgDn • Enter launch • Esc back • ^C close",
	"↑↓ • Enter • Esc • ^C",
}

var workspaceEditorFooterHints = []string{
	"Enter launch here  •  Esc back  •  Ctrl+C close",
	"Enter launch • Esc back • ^C close",
	"Enter • Esc • ^C",
}

var workspacePendingFooterHints = []string{
	"Launching…  •  Ctrl+C close",
	"Launching… • ^C close",
	"Launching… • ^C",
}

// footerLine picks the widest footer hint that fits on one line, falling back
// to clipping the shortest variant so the footer never wraps and never
// overflows width.
func footerLine(width int) string {
	for _, hint := range footerHints {
		if lipgloss.Width(hint) <= width {
			return hint
		}
	}
	return clip(footerHints[len(footerHints)-1], width)
}

func inspectionFooterLine(width int) string {
	for _, hint := range inspectionFooterHints {
		if lipgloss.Width(hint) <= width {
			return hint
		}
	}
	return clip(inspectionFooterHints[len(inspectionFooterHints)-1], width)
}

func workspacePickerFooterLine(width int) string {
	return fittingFooterLine(workspacePickerFooterHints, width)
}

func workspaceEditorFooterLine(width int, pending bool) string {
	if pending {
		return fittingFooterLine(workspacePendingFooterHints, width)
	}
	return fittingFooterLine(workspaceEditorFooterHints, width)
}

func fittingFooterLine(hints []string, width int) string {
	for _, hint := range hints {
		if lipgloss.Width(hint) <= width {
			return hint
		}
	}
	return clip(hints[len(hints)-1], width)
}

type palette struct {
	dark     bool
	text     lipgloss.Style
	muted    lipgloss.Style
	focus    lipgloss.Style
	healthy  lipgloss.Style
	warning  lipgloss.Style
	failure  lipgloss.Style
	chipText lipgloss.Style // contrasting foreground for text drawn on a colored chip background
	brand    lipgloss.Style // the header brand chip: colored background + contrasting foreground
}

func newPalette(dark bool) palette {
	colors := struct {
		text, muted, focus, healthy, warning, failure, chipText, brandBg string
	}{
		text: "#1F2937", muted: "#64748B", focus: "#0891B2",
		healthy: "#15803D", warning: "#B45309", failure: "#DC2626",
		chipText: "#F8FAFC", brandBg: "#0891B2",
	}
	if dark {
		colors = struct {
			text, muted, focus, healthy, warning, failure, chipText, brandBg string
		}{
			text: "#E5E7EB", muted: "#94A3B8", focus: "#22D3EE",
			healthy: "#4ADE80", warning: "#FBBF24", failure: "#F87171",
			chipText: "#0F172A", brandBg: "#22D3EE",
		}
	}
	return palette{
		dark:     dark,
		text:     lipgloss.NewStyle().Foreground(lipgloss.Color(colors.text)),
		muted:    lipgloss.NewStyle().Foreground(lipgloss.Color(colors.muted)),
		focus:    lipgloss.NewStyle().Foreground(lipgloss.Color(colors.focus)).Bold(true),
		healthy:  lipgloss.NewStyle().Foreground(lipgloss.Color(colors.healthy)),
		warning:  lipgloss.NewStyle().Foreground(lipgloss.Color(colors.warning)),
		failure:  lipgloss.NewStyle().Foreground(lipgloss.Color(colors.failure)),
		chipText: lipgloss.NewStyle().Foreground(lipgloss.Color(colors.chipText)),
		brand:    lipgloss.NewStyle().Foreground(lipgloss.Color(colors.chipText)).Background(lipgloss.Color(colors.brandBg)).Bold(true),
	}
}

// statusChipText returns the header's plain status text: a "LIVE" chip with
// the last refresh time when idle, or a "REFRESHING" chip while a refresh is
// in flight. Kept separate from styling so width bookkeeping (headerLines)
// and rendering (headerLines styled variant) measure the exact same text.
func (m Model) statusChipText() string {
	if m.launchPending {
		label := strings.ToUpper(toolLabel(m.launchTool))
		if m.launchTool == core.ToolClaudeCLI {
			label = "CLAUDE CLI"
		}
		return "LAUNCHING " + label
	}
	if m.refreshing {
		return "↻ REFRESHING"
	}
	return "● LIVE · " + m.snapshot.RefreshedAt.Format("15:04:05")
}

func (m Model) statusChipStyle() lipgloss.Style {
	if m.refreshing || m.launchPending {
		return m.palette.focus
	}
	return m.palette.healthy
}

// headerLineTexts builds the header's plain-text content, one string per
// line, before any styling is applied. Shared by render() (which styles each
// segment) and the model's viewport bookkeeping (which only needs the line
// count), so both agree on how many lines the header actually costs.
//
// The header carries only the brand chip and the status: there is no global
// "active workspace" anymore — a workspace is chosen per launch — so nothing
// in the header implies a mutable launch target. The brand and status share
// one line when they fit; otherwise they drop to two lines.
func (m Model) headerLineTexts(width int) []string {
	chip := " " + brand + " "
	status := m.statusChipText()

	oneLine := chip + "  " + status
	if lipgloss.Width(oneLine) <= width {
		return []string{oneLine}
	}
	return []string{chip, status}
}

// headerLines renders the styled header: the brand as a colored chip and the
// status as a colored chip (healthy when idle, focus-colored while
// refreshing). Plain text is measured and clipped before any style is
// applied, per clip()'s ANSI-safety constraint; a line that still doesn't fit
// after clipping falls back to a muted, fully clipped plain line.
func (m Model) headerLines(width int) []string {
	chipPlain := " " + brand + " "
	statusPlain := m.statusChipText()
	statusStyle := m.statusChipStyle()

	renderLine := func(plainLine string, includeChip, includeStatus bool) string {
		if lipgloss.Width(plainLine) > width {
			return m.palette.muted.Render(clip(plainLine, width))
		}
		if includeChip && includeStatus {
			// One-line layout: chip + "  " + status.
			return m.palette.brand.Render(chipPlain) + m.palette.text.Render("  ") + statusStyle.Render(statusPlain)
		}
		if includeChip {
			return m.palette.brand.Render(chipPlain)
		}
		return statusStyle.Render(statusPlain)
	}

	texts := m.headerLineTexts(width)
	lines := make([]string, len(texts))
	if len(texts) == 1 {
		lines[0] = renderLine(texts[0], true, true)
		return lines
	}
	lines[0] = renderLine(texts[0], true, false)
	lines[1] = renderLine(texts[1], false, true)
	return lines
}

// truncateHeaderLines caps the header at height-1 lines, reserving the last
// line for the footer even if that means dropping trailing wrapped header
// lines at extreme heights. Header and footer are dropped last among
// regions, but neither may push total output past height.
func truncateHeaderLines(lines []string, height int) []string {
	maxHeaderLines := height - 1
	if maxHeaderLines < 0 {
		return nil
	}
	if len(lines) > maxHeaderLines {
		return lines[:maxHeaderLines]
	}
	return lines
}

// rowsPerSession reports how many output lines one session's row costs at a
// given width: the wide table mode is one line/session, everything narrower
// is two (row + folder/detail line).
func rowsPerSession(width int) int {
	if width >= 110 {
		return 1
	}
	return 2
}

// minDetailViewportSessions is the number of sessions' worth of viewport
// lines that must remain after reserving space for the selected-row detail
// panel; below that the panel is hidden entirely rather than squeezing the
// session list down to a sliver.
const minDetailViewportSessions = 3

// layoutPlan is the height budget's output: which optional regions survive
// and how many lines remain for the session viewport. planLayout is the seam
// Stage 2's detail panel extends — add its region cost here rather than
// scattering arithmetic through render().
type layoutPlan struct {
	showSummary    bool
	showHeading    bool
	showWarning    bool
	showDetail     bool
	viewportLines  int
	rowsPerSession int
}

// planLayout decides which optional regions fit inside height, given the
// fixed cost of the header (already wrapped, so its line count is known) and
// the always-present single-line footer. Regions are dropped in a fixed
// priority order when the budget is short: detail panel first, then summary
// strip, then table heading, then warning line. When sessions exist, the
// planner reserves one complete row plus two overflow indicators so
// sessionWindow cannot consume the entire viewport with navigation context.
// Header and footer are never dropped; below ~4 lines of total height the
// viewport itself may still shrink to zero, which render() treats as "show
// nothing" rather than panicking or overflowing.
//
// detailLines is the detail panel's real rendered line count (border
// included) when a session is selected, or 0 when nothing is selected. The
// panel additionally requires minDetailViewportSessions sessions' worth of
// viewport lines to remain after it is reserved; otherwise it is hidden even
// though there would technically be room for the panel alone.
func planLayout(width, height, headerLines int, hasWarning, headingEligible, hasSessions bool, detailLines int) layoutPlan {
	if height < 1 {
		height = 24
	}
	const footerLines = 1

	plan := layoutPlan{
		showSummary:    hasSessions,
		showHeading:    headingEligible && hasSessions,
		showWarning:    hasWarning,
		showDetail:     detailLines > 0 && hasSessions,
		rowsPerSession: rowsPerSession(width),
	}

	regionCost := func() int {
		cost := headerLines + footerLines
		if plan.showSummary {
			cost += 2 // summary line + trailing blank separator
		}
		if plan.showWarning {
			cost += 2 // warning line + trailing blank separator
		}
		if plan.showHeading {
			cost++
		}
		if plan.showDetail {
			cost += detailLines + 1 // panel (border included) + trailing blank separator
		}
		return cost
	}

	minimumViewport := 1
	if hasSessions {
		minimumViewport = plan.rowsPerSession + 2
	}
	detailViewport := minDetailViewportSessions * plan.rowsPerSession

	// Drop optional regions in priority order until the viewport can show a
	// complete selected row with navigation context, or nothing is left to
	// drop. The detail panel goes first: it is the newest, least essential
	// region, and the one most likely to be squeezed out at short heights.
	for (height-regionCost() < minimumViewport || (plan.showDetail && height-regionCost() < detailViewport)) &&
		(plan.showDetail || plan.showSummary || plan.showHeading || plan.showWarning) {
		switch {
		case plan.showDetail:
			plan.showDetail = false
		case plan.showSummary:
			plan.showSummary = false
		case plan.showHeading:
			plan.showHeading = false
		case plan.showWarning:
			plan.showWarning = false
		}
	}

	plan.viewportLines = height - regionCost()
	if plan.viewportLines < 0 {
		plan.viewportLines = 0
	}
	return plan
}

// sessionWindow computes which sessions are visible in a viewport of
// viewportLines, given rowsPerSession and the desired first-visible index
// (offset). It reserves lines for "N more" overflow indicators only when
// they are actually needed, re-fitting the window so the indicators
// themselves never push the total over budget.
func sessionWindow(total, offset, viewportLines, rowsPerSession int) (start, end int, showAbove, showBelow bool) {
	if total == 0 || viewportLines <= 0 || rowsPerSession <= 0 {
		return 0, 0, false, false
	}
	if offset < 0 {
		offset = 0
	}
	if offset > total-1 {
		offset = total - 1
	}

	start = offset
	capacity := viewportLines / rowsPerSession
	if capacity < 1 {
		return start, start, false, false
	}
	end = min(start+capacity, total)
	if start == 0 && end == total {
		return start, end, false, false
	}

	// If every remaining session fits alongside an above indicator, render
	// them all instead of manufacturing below-overflow by reserving two
	// indicator lines.
	if start > 0 {
		capacityWithAbove := (viewportLines - 1) / rowsPerSession
		if capacityWithAbove >= 1 {
			endWithAbove := min(start+capacityWithAbove, total)
			if endWithAbove == total {
				return start, endWithAbove, true, false
			}
		}
	}

	indicatorLines := 0
	if start > 0 && viewportLines >= rowsPerSession+1 {
		showAbove = true
		indicatorLines++
	}
	capacity = (viewportLines - indicatorLines) / rowsPerSession
	end = min(start+capacity, total)

	// A below indicator is added only if one complete row still fits after
	// reserving it. When the viewport is exactly one row tall, the row wins.
	if end < total && viewportLines-indicatorLines >= rowsPerSession+1 {
		showBelow = true
		indicatorLines++
		capacity = (viewportLines - indicatorLines) / rowsPerSession
		end = min(start+capacity, total)
	}
	return start, end, showAbove, showBelow
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
	height := m.height
	if height < 1 {
		height = 24
	}
	if width < minimumTerminalWidth {
		message := fmt.Sprintf("Window too narrow - resize to at least %d columns", minimumTerminalWidth)
		return m.palette.warning.Render(clip(message, width))
	}
	if m.startupErr != nil {
		return capLines(m.renderStartupError(width), height)
	}
	if !m.started {
		return capLines(m.renderLoading(width, height), height)
	}
	if m.screen == screenInspection {
		return m.renderInspection(width, height)
	}
	if m.screen == screenWorkspacePicker {
		return m.renderWorkspacePicker(width, height)
	}
	if m.screen == screenWorkspaceEditor {
		return m.renderWorkspaceEditor(width, height)
	}

	headerLines := truncateHeaderLines(m.headerLines(width), height)

	hasSessions := len(m.snapshot.Sessions) > 0
	headingEligible := width >= 80

	detailLines := 0
	selected, hasSelection := m.selectedSession()
	if hasSelection {
		detailLines = detailPanelLineCount()
	}
	plan := planLayout(width, height, len(headerLines), m.actionErr != nil || len(m.snapshot.Warnings) > 0, headingEligible, hasSessions, detailLines)

	lines := append([]string{}, headerLines...)

	if plan.showSummary {
		lines = append(lines, m.summaryLines(width)...)
		lines = append(lines, "")
	}

	if plan.showWarning {
		if m.actionErr != nil {
			banner := clip("Action failed: "+m.actionErr.Error(), width)
			lines = append(lines, m.palette.failure.Render(banner), "")
		} else {
			suffix := ""
			if more := len(m.snapshot.Warnings) - 1; more > 0 {
				suffix = fmt.Sprintf(" +%d more", more)
			}
			count := len(m.snapshot.Warnings)
			label := fmt.Sprintf("⚠ %d warnings", count)
			if count == 1 {
				label = "⚠ 1 warning"
			}
			prefix := label + ": "
			messageWidth := width - lipgloss.Width(prefix) - lipgloss.Width(suffix)
			warning := prefix + clip(m.snapshot.Warnings[0].Message, messageWidth) + suffix
			lines = append(lines, m.palette.warning.Render(warning), "")
		}
	}

	lines = append(lines, m.renderViewport(width, plan)...)

	if plan.showDetail {
		lines = append(lines, m.renderDetailPanel(width, selected), "")
	}

	lines = append(lines, footerLine(width))
	return strings.Join(lines, "\n")
}

func (m Model) renderWorkspacePicker(width, height int) string {
	header := truncateHeaderLines(m.headerLines(width), height)
	footer := workspacePickerFooterLine(width)
	if m.launchPending {
		footer = workspaceEditorFooterLine(width, true)
	}
	lines := append([]string{}, header...)
	remaining := height - len(lines) - 1
	if remaining < 0 {
		remaining = 0
	}
	if remaining > 0 {
		title := "Launch " + toolLabel(m.launchTool) + " · choose workspace"
		lines = append(lines, m.palette.focus.Render(clip(title, width)))
		remaining--
	}

	statusLines := 0
	if m.launchPending || m.workspaceErr != nil {
		statusLines = 1
	}
	choiceLines := max(remaining-statusLines, 0)
	start, end, showAbove, showBelow := sessionWindow(len(m.workspaceChoices), m.workspacePickerIndex, choiceLines, 1)
	if showAbove {
		lines = append(lines, m.palette.muted.Render(clip(fmt.Sprintf("↑ %d more", start), width)))
	}
	for index := start; index < end; index++ {
		choice := m.workspaceChoices[index]
		label := choice.path
		if choice.other {
			label = "Enter a different folder…"
		} else if choice.label != "" {
			label += "  (" + choice.label + ")"
		}
		prefix := "  "
		style := m.palette.text
		if index == m.workspacePickerIndex {
			prefix = "▌ "
			style = m.palette.focus
		}
		lines = append(lines, style.Render(clip(prefix+label, width)))
	}
	if showBelow {
		lines = append(lines, m.palette.muted.Render(clip(fmt.Sprintf("↓ %d more", len(m.workspaceChoices)-end), width)))
	}
	if m.launchPending && len(lines) < height-1 {
		lines = append(lines, m.palette.focus.Render(clip("Launching…", width)))
	} else if m.workspaceErr != nil && len(lines) < height-1 {
		message := "Unable to launch: " + m.workspaceErr.Error()
		lines = append(lines, m.palette.failure.Render(clip(message, width)))
	}
	if len(lines) >= height && height > 0 {
		lines = lines[:height-1]
	}
	lines = append(lines, footer)
	return strings.Join(lines, "\n")
}

func (m Model) renderWorkspaceEditor(width, height int) string {
	header := truncateHeaderLines(m.headerLines(width), height)
	footer := workspaceEditorFooterLine(width, m.launchPending)
	lines := append([]string{}, header...)
	remaining := height - len(lines) - 1
	if remaining < 0 {
		remaining = 0
	}
	if remaining > 0 {
		title := "Launch " + toolLabel(m.launchTool) + " in folder"
		lines = append(lines, m.palette.focus.Render(clip(title, width)))
		remaining--
	}
	if remaining > 0 {
		prefix := "> "
		inputWidth := max(width-lipgloss.Width(prefix), 1)
		input := workspaceEditorWindow(m.workspaceDraft, m.workspaceCursor, inputWidth)
		lines = append(lines, m.palette.muted.Render(prefix)+m.palette.text.Render(input))
		remaining--
	}
	if remaining > 0 && m.launchPending {
		lines = append(lines, m.palette.focus.Render(clip("Launching…", width)))
		remaining--
	} else if remaining > 0 && m.workspaceErr != nil {
		lines = append(lines, m.palette.failure.Render(clip(m.workspaceErr.Error(), width)))
		remaining--
	}
	if len(lines) >= height && height > 0 {
		lines = lines[:height-1]
	}
	lines = append(lines, footer)
	return strings.Join(lines, "\n")
}

func workspaceEditorWindow(draft string, cursor, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(draft)
	cursor = min(max(cursor, 0), len(runes))
	const cursorGlyph = "│"
	glyphWidth := lipgloss.Width(cursorGlyph)
	if width <= glyphWidth {
		return cursorGlyph
	}

	// Grow the window one rune at a time, re-deriving the ellipsis budget on
	// every step. Sizing the window before knowing whether an ellipsis is
	// needed (as a single up-front pass would) risks reserving no room for
	// it and overflowing width once one gets appended.
	start, end := cursor, cursor
	for {
		leadingWidth, trailingWidth := 0, 0
		if start > 0 {
			leadingWidth = 1
		}
		if end < len(runes) {
			trailingWidth = 1
		}
		budget := width - glyphWidth - leadingWidth - trailingWidth
		contentWidth := lipgloss.Width(string(runes[start:end]))

		if start > 0 && contentWidth+lipgloss.Width(string(runes[start-1])) <= budget {
			start--
			continue
		}
		if end < len(runes) && contentWidth+lipgloss.Width(string(runes[end])) <= budget {
			end++
			continue
		}
		break
	}

	leading, trailing := "", ""
	if start > 0 {
		leading = "…"
	}
	if end < len(runes) {
		trailing = "…"
	}

	// Defensive clamp: only reachable at widths too small to fit a glyph
	// plus both ellipses, which the growth loop above cannot itself resolve.
	for lipgloss.Width(leading)+lipgloss.Width(string(runes[start:cursor]))+glyphWidth+lipgloss.Width(string(runes[cursor:end]))+lipgloss.Width(trailing) > width {
		switch {
		case end > cursor:
			end--
		case start < cursor:
			start++
		case trailing != "":
			trailing = ""
			continue
		case leading != "":
			leading = ""
			continue
		default:
			return cursorGlyph
		}
		if end < len(runes) {
			trailing = "…"
		} else {
			trailing = ""
		}
		if start > 0 {
			leading = "…"
		} else {
			leading = ""
		}
	}
	return leading + string(runes[start:cursor]) + cursorGlyph + string(runes[cursor:end]) + trailing
}

// renderInspection draws a read-only, live process-tree screen. Header,
// context, optional heading, viewport, and footer share one explicit height
// budget so even tiny terminals retain the footer as their final line.
func (m Model) renderInspection(width, height int) string {
	header := truncateHeaderLines(m.headerLines(width), height)
	lines := append([]string{}, header...)
	remaining := height - len(lines) - 1 // the inspection footer is mandatory
	if remaining < 0 {
		remaining = 0
	}

	context := m.inspectionContextLines(width)
	if len(context) > remaining {
		context = context[:remaining]
	}
	lines = append(lines, context...)
	remaining -= len(context)

	if width >= 110 && remaining > 0 {
		lines = append(lines, m.palette.muted.Render(clip(inspectionHeading(), width)))
		remaining--
	}
	lines = append(lines, m.renderInspectionViewport(width, remaining)...)
	lines = append(lines, inspectionFooterLine(width))
	return strings.Join(lines, "\n")
}

func (m Model) inspectionSession() core.Session {
	if m.inspectionTree.Session.ID != "" {
		return m.inspectionTree.Session
	}
	for _, session := range m.snapshot.Sessions {
		if session.ID == m.inspectionSessionID {
			return session
		}
	}
	return core.Session{ID: m.inspectionSessionID}
}

func (m Model) inspectionContextLines(width int) []string {
	session := m.inspectionSession()
	title := "Process Tree"
	metadata := toolLabel(session.Tool) + " · " + trackingLabel(session.Confidence) + " · " + string(session.State)
	firstPlain := title + "  " + metadata
	var first string
	if lipgloss.Width(firstPlain) > width {
		first = m.palette.muted.Render(clip(firstPlain, width))
	} else {
		first = m.palette.focus.Render(title) + m.palette.muted.Render("  "+toolLabel(session.Tool)+" · "+trackingLabel(session.Confidence)+" · ") + m.stateStyle(session.State).Render(string(session.State))
	}

	workspace := folder(session)
	workspacePlain := "Workspace  " + workspace
	var second string
	if lipgloss.Width(workspacePlain) > width {
		second = m.palette.muted.Render(clip(workspacePlain, width))
	} else {
		second = m.palette.muted.Render("Workspace  ") + m.palette.text.Render(workspace)
	}
	return []string{first, second}
}

func inspectionHeading() string {
	return fmt.Sprintf("%-40s %-8s %-8s %-8s %-10s %-10s", "PROCESS", "PID", "PARENT", "START", "CPU", "MEMORY")
}

func (m Model) renderInspectionViewport(width, viewportLines int) []string {
	if viewportLines <= 0 {
		return nil
	}
	rows := m.inspectionTree.Rows
	var body []string

	if m.inspectionPending && len(rows) == 0 {
		return []string{m.palette.focus.Render(clip("Loading process tree…", width))}
	}
	if m.inspectionErr != nil {
		message := "Unable to inspect: " + m.inspectionErr.Error()
		style := m.palette.failure
		if errors.Is(m.inspectionErr, app.ErrSessionNotFound) {
			message = "Session unavailable — it disappeared during refresh."
			style = m.palette.warning
		}
		body = append(body, style.Render(clip(message, width)))
		if errors.Is(m.inspectionErr, app.ErrSessionNotFound) || len(rows) == 0 || len(body) >= viewportLines {
			return body[:min(len(body), viewportLines)]
		}
	}
	if len(rows) == 0 {
		body = append(body, m.palette.muted.Render(clip("No processes remain for this session.", width)))
		return body[:min(len(body), viewportLines)]
	}

	rowBudget := m.effectiveInspectionRowViewportLineCount(viewportLines)
	rowsPerProcess := inspectionRowsPerProcess(width)
	start, end, showAbove, showBelow := sessionWindow(len(rows), m.inspectionScrollOffset, rowBudget, rowsPerProcess)
	if showAbove {
		body = append(body, m.palette.muted.Render(clip(fmt.Sprintf("↑ %d more", start), width)))
	}
	for _, row := range rows[start:end] {
		body = append(body, m.renderInspectionProcess(row, width)...)
	}
	if showBelow {
		body = append(body, m.palette.muted.Render(clip(fmt.Sprintf("↓ %d more", len(rows)-end), width)))
	}
	if len(body) > viewportLines {
		body = body[:viewportLines]
	}
	return body
}

func (m Model) renderInspectionProcess(row core.ProcessTreeRow, width int) []string {
	process := row.Process
	name := process.Name
	if name == "" {
		name = "<unnamed>"
	}

	if width >= 110 {
		const nameWidth = 40
		structure := inspectionStructure(row.Depth, nameWidth)
		nameWidthRemaining := nameWidth - lipgloss.Width(structure)
		nameCell := padDisplayWidth(name, nameWidthRemaining)
		metadata := fmt.Sprintf(" %-8d %-8d %-8s %-10s %-10s",
			process.PID, process.ParentPID, m.processStartLabel(process), processCPULabel(process), processMemoryLabel(process))
		return []string{m.palette.focus.Render(structure) + m.palette.text.Render(nameCell) + m.palette.muted.Render(metadata)}
	}

	structure := inspectionStructure(row.Depth, width)
	nameLine := func(suffix string) string {
		nameWidth := width - lipgloss.Width(structure) - lipgloss.Width(suffix)
		return m.palette.focus.Render(structure) + m.palette.text.Render(clip(name, nameWidth)) + m.palette.muted.Render(suffix)
	}
	identities := fmt.Sprintf("PID %d · PPID %d", process.PID, process.ParentPID)
	started := "Start " + m.processStartLabel(process)
	resources := fmt.Sprintf("CPU %s · Mem %s", processCPULabel(process), processMemoryLabel(process))

	switch inspectionRowsPerProcess(width) {
	case 2:
		pidSuffix := fmt.Sprintf("  PID %d", process.PID)
		metadata := fmt.Sprintf("PPID %d · %s · %s", process.ParentPID, started, resources)
		return []string{nameLine(pidSuffix), m.palette.muted.Render(clip(metadata, width))}
	case 3:
		metadata := started + " · " + resources
		return []string{nameLine(""), m.palette.muted.Render(clip(identities, width)), m.palette.muted.Render(clip(metadata, width))}
	default:
		return []string{
			nameLine(""),
			m.palette.muted.Render(clip(identities, width)),
			m.palette.muted.Render(clip(started, width)),
			m.palette.muted.Render(clip(resources, width)),
		}
	}
}

// inspectionStructure consumes coordinator-supplied Depth directly while
// bounding its display width. The cap preserves visible topology without
// allowing a pathological depth to crowd the process name off narrow rows.
func inspectionStructure(depth, containerWidth int) string {
	if depth <= 0 || containerWidth <= 0 {
		return ""
	}
	const (
		maxDepth        = 4
		minimumNameRoom = 8
		connectorWidth  = 3
	)
	visibleDepth := min(depth, maxDepth)
	depthByWidth := (containerWidth - minimumNameRoom - connectorWidth) / 2
	if depthByWidth < 0 {
		depthByWidth = 0
	}
	visibleDepth = min(visibleDepth, depthByWidth)
	return strings.Repeat("  ", visibleDepth) + "└─ "
}

func padDisplayWidth(value string, width int) string {
	if width <= 0 {
		return ""
	}
	value = clip(value, width)
	return value + strings.Repeat(" ", width-lipgloss.Width(value))
}

func (m Model) processStartLabel(process core.Process) string {
	if process.CreatedAt.IsZero() {
		return "—"
	}
	return process.CreatedAt.In(m.now().Location()).Format("15:04:05")
}

func processCPULabel(process core.Process) string {
	if !process.CPUAvailable {
		return "—"
	}
	return fmt.Sprintf("%.1f%%", process.CPUPercent)
}

func processMemoryLabel(process core.Process) string {
	return memoryLabel(core.ResourceUsage{MemoryBytes: process.MemoryBytes, MemoryAvailable: process.MemoryAvailable})
}

// renderStartupError draws the blocking startup-error screen: a
// failure-colored bordered panel with the error text, then the muted close
// hint as the last line. Unlike the summary/detail panels, this screen has
// no other regions competing for height, so capLines is applied by the
// caller rather than here.
func (m Model) renderStartupError(width int) []string {
	content := detailPanelContentWidth(width)
	body := renderWrapped(m.palette.failure, "Unable to start TaskWatch", content)
	body = append(body, renderWrapped(m.palette.failure, m.startupErr.Error(), content)...)

	panel := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.palette.failure.GetForeground()).
		Padding(0, 1)
	lines := strings.Split(panel.Render(strings.Join(body, "\n")), "\n")
	lines = append(lines, m.palette.muted.Render(clip("Ctrl+C close (apps keep running)", width)))
	return lines
}

// renderLoading draws the startup screen: the brand chip and a muted
// "Starting…" message, padded down a little so it doesn't sit jammed against
// the top-left corner. No timers or animation: this is a static screen.
func (m Model) renderLoading(width, height int) []string {
	brandLine := m.palette.brand.Render(clip(" "+brand+" ", width))
	startingLine := m.palette.muted.Render(clip("Starting TaskWatch…", width))
	lines := []string{
		centerLine(brandLine, width),
		"",
		centerLine(startingLine, width),
	}
	topPad := (height - len(lines)) / 3
	if topPad < 0 {
		topPad = 0
	}
	return append(make([]string, topPad), lines...)
}

// centerLine horizontally pads content that was already clipped before
// styling. Measuring the rendered line is ANSI-aware; only plain spaces are
// added, so no styled string is passed through clip().
func centerLine(line string, width int) string {
	lineWidth := lipgloss.Width(line)
	if lineWidth >= width {
		return line
	}
	return strings.Repeat(" ", (width-lineWidth)/2) + line
}

// capLines hard-caps a rendered screen at height lines. For the startup
// error screen specifically, trimming (when unavoidable at tiny heights)
// removes wrapped error lines from the end rather than the hint line, so the
// close hint is the last thing dropped rather than the first.
func capLines(lines []string, height int) string {
	if height >= 0 && len(lines) > height {
		if height == 0 {
			return ""
		}
		hint := lines[len(lines)-1]
		trimmed := append([]string{}, lines[:height-1]...)
		trimmed = append(trimmed, hint)
		lines = trimmed
	}
	return strings.Join(lines, "\n")
}

// renderViewport draws the table heading (if it survived the budget), the
// visible slice of sessions, and overflow indicators for sessions scrolled
// out of view above or below. The heading costs one line already reserved by
// the plan; everything else is hard-capped at plan.viewportLines so best-effort
// tiny heights never overflow, even though sessionWindow should already fit.
func (m Model) renderViewport(width int, plan layoutPlan) []string {
	var lines []string
	if plan.showHeading {
		switch {
		case width >= 110:
			lines = append(lines, m.palette.muted.Render(clip("  "+fullHeading(), width)))
		case width >= 80:
			lines = append(lines, m.palette.muted.Render(clip("  "+mediumHeading(), width)))
		}
	}

	var body []string
	sessions := m.snapshot.Sessions
	if len(sessions) == 0 {
		body = renderWrapped(m.palette.text, "No sessions yet", width)
		body = append(body, renderWrapped(m.palette.muted, "Watching for Codex Desktop. Refreshing every 2 seconds.", width)...)
	} else {
		start, end, showAbove, showBelow := sessionWindow(len(sessions), m.scrollOffset, plan.viewportLines, plan.rowsPerSession)

		if showAbove {
			body = append(body, m.palette.muted.Render(clip(fmt.Sprintf("↑ %d more", start), width)))
		}
		for _, session := range sessions[start:end] {
			switch {
			case width >= 110:
				body = append(body, m.renderRow(session, fullRow(session, m.now()), width))
			case width >= 80:
				body = append(body, m.renderRow(session, mediumRow(session, m.now()), width))
				body = append(body, m.palette.muted.Render(clip("    "+folder(session), width)))
			default:
				first, second := narrowRows(session, m.now())
				body = append(body, m.renderRow(session, first, width))
				body = append(body, m.palette.muted.Render(clip(second, width)))
			}
		}
		if showBelow {
			body = append(body, m.palette.muted.Render(clip(fmt.Sprintf("↓ %d more", len(sessions)-end), width)))
		}
	}

	if plan.viewportLines >= 0 && len(body) > plan.viewportLines {
		body = body[:plan.viewportLines]
	}
	return append(lines, body...)
}

// detailPanelFieldCount is the number of labeled field lines drawn inside the
// selected-row detail panel, below its title line. Kept as a constant so the
// panel's line cost (see detailPanelLineCount) does not depend on session
// data or width: it is either fully shown or fully hidden, never partially
// wrapped.
const detailPanelFieldCount = 8

// detailPanelLineCount is the detail panel's real rendered line count,
// border included: a top border, a title line, detailPanelFieldCount field
// lines, and a bottom border. It is constant for any width, which lets
// planLayout treat it as a fixed-cost region like the summary strip.
func detailPanelLineCount() int {
	const borderLines = 2
	return borderLines + 1 + detailPanelFieldCount
}

// detailPanelContentWidth returns how many columns are left for panel text
// after the rounded border (2 columns) and one column of horizontal padding
// on each side (2 columns).
func detailPanelContentWidth(width int) int {
	content := width - 4
	if content < 1 {
		content = 1
	}
	return content
}

// selectedSession returns the session matching m.selectedID and whether one
// was found, so the detail panel can be skipped cleanly when there is no
// selection (e.g. an empty session list).
func (m Model) selectedSession() (core.Session, bool) {
	for _, session := range m.snapshot.Sessions {
		if session.ID == m.selectedID {
			return session, true
		}
	}
	return core.Session{}, false
}

// renderDetailPanel draws a bordered panel with labeled fields for the
// currently selected session: tool/tracking, state, full workspace path,
// root PID, started-at (absolute time + age), process count, CPU, and
// memory. Every field's plain text is clipped to detailPanelContentWidth
// before any style is applied, per clip()'s ANSI-safety constraint.
func (m Model) renderDetailPanel(width int, session core.Session) string {
	content := detailPanelContentWidth(width)

	field := func(label, value string) string {
		plain := label + ": " + value
		if lipgloss.Width(plain) > content {
			return m.palette.muted.Render(clip(plain, content))
		}
		return m.palette.muted.Render(clip(label+": ", content)) + m.palette.text.Render(clip(value, content-lipgloss.Width(label+": ")))
	}

	started := session.StartedAt
	startedText := "—"
	if !started.IsZero() {
		now := m.now()
		startedText = started.In(now.Location()).Format("15:04:05") + " (" + ageLabel(started, now) + " ago)"
	}

	lines := []string{
		m.palette.focus.Render(clip("Selected Session", content)),
		field("Tool", toolLabel(session.Tool)+" · "+trackingLabel(session.Confidence)),
		field("State", string(session.State)),
		field("Workspace", folder(session)),
		field("Root PID", fmt.Sprintf("%d", session.Root.PID)),
		field("Started", startedText),
		field("Processes", fmt.Sprintf("%d", len(session.Processes))),
		field("CPU", cpuCell(session)),
		field("Memory", memoryLabel(session.Resources)),
	}

	panel := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.palette.muted.GetForeground()).
		Padding(0, 1)
	return panel.Render(strings.Join(lines, "\n"))
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
