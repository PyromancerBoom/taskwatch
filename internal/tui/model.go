package tui

import (
	"errors"
	"sort"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"

	"github.com/PyromancerBoom/taskwatch/internal/app"
	"github.com/PyromancerBoom/taskwatch/internal/core"
)

const DefaultRefreshInterval = 2 * time.Second

type Coordinator interface {
	Start() (app.Snapshot, error)
	Refresh() app.Snapshot
	Inspect(string) (app.ProcessTree, error)
	Launch(core.Tool, string) (app.LaunchResult, error)
}

type screenState uint8

const (
	screenDashboard screenState = iota
	screenInspection
	screenWorkspacePicker
	screenWorkspaceEditor
)

type workspaceChoice struct {
	path  string
	label string // annotation shown in parentheses, e.g. "last used", "current dir"
	other bool   // the "enter a different folder" entry
}

type startupMsg struct {
	snapshot app.Snapshot
	err      error
}

type refreshMsg struct {
	requestID           uint64
	userAction          bool
	snapshot            app.Snapshot
	inspectionSessionID string
	inspectionRequestID uint64
	inspectionTree      app.ProcessTree
	inspectionErr       error
}

type inspectMsg struct {
	sessionID     string
	requestID     uint64
	loadRequestID uint64
	tree          app.ProcessTree
	err           error
}

type launchMsg struct {
	requestID uint64
	result    app.LaunchResult
	err       error
}

type tickMsg struct {
	generation uint64
}

type tickFunc func(time.Duration, func(time.Time) tea.Msg) tea.Cmd

type Model struct {
	coordinator Coordinator
	interval    time.Duration
	tick        tickFunc
	now         func() time.Time

	snapshot         app.Snapshot
	selectedID       string
	startupErr       error
	started          bool
	refreshing       bool
	generation       uint64
	refreshRequestID uint64
	screen           screenState

	inspectionSessionID     string
	inspectionTree          app.ProcessTree
	inspectionErr           error
	inspectionPending       bool
	inspectionLoadInFlight  bool
	inspectionLoadRequestID uint64
	inspectionScrollOffset  int
	inspectionRequestID     uint64
	inspectionRaw           bool

	workspaceChoices     []workspaceChoice
	workspacePickerIndex int
	workspaceDraft       string
	workspaceCursor      int
	workspaceErr         error

	actionErr       error
	launchPending   bool
	launchTool      core.Tool
	launchRequestID uint64

	width        int
	height       int
	scrollOffset int // first visible session index, measured in sessions (not lines)
	palette      palette
	cpuHistory   []float64
}

func NewModel(coordinator Coordinator, interval time.Duration) Model {
	if interval <= 0 {
		interval = DefaultRefreshInterval
	}
	return Model{
		coordinator: coordinator,
		interval:    interval,
		tick:        tea.Tick,
		now:         time.Now,
		width:       80,
		palette:     newPalette(true),
	}
}

func (m Model) Init() tea.Cmd {
	start := func() tea.Msg {
		snapshot, err := m.coordinator.Start()
		return startupMsg{snapshot: snapshot, err: err}
	}
	return tea.Batch(start, tea.RequestBackgroundColor)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		m.palette = newPalette(msg.IsDark())
		return m, nil
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.clampScrollOffset()
		m.clampInspectionScrollOffset()
		m.ensureSelectionVisible()
		return m, nil
	case startupMsg:
		if msg.err != nil {
			m.startupErr = msg.err
			m.started = false
			return m, nil
		}
		m.snapshot = msg.snapshot
		m.started = true
		m.startupErr = nil
		m.reconcileSelection()
		m.recordCPU()
		return m.scheduleTick()
	case tickMsg:
		if !m.started || m.startupErr != nil || m.refreshing || m.launchPending || m.inspectionLoadInFlight || m.screen == screenWorkspacePicker || m.screen == screenWorkspaceEditor || msg.generation != m.generation {
			return m, nil
		}
		return m.startRefresh(false)
	case refreshMsg:
		if msg.requestID != 0 && (!m.refreshing || msg.requestID != m.refreshRequestID) {
			return m, nil
		}
		m.snapshot = msg.snapshot
		m.refreshing = false
		if msg.userAction && (msg.inspectionSessionID == "" || msg.inspectionErr == nil) {
			m.actionErr = nil
		}
		m.reconcileSelection()
		m.recordCPU()
		if m.screen == screenInspection && msg.inspectionRequestID == m.inspectionRequestID && msg.inspectionSessionID == m.inspectionSessionID {
			m.applyInspectionResult(msg.inspectionTree, msg.inspectionErr, true)
		}
		return m.scheduleTick()
	case inspectMsg:
		matchedLoad := m.inspectionLoadInFlight && msg.loadRequestID == m.inspectionLoadRequestID
		if matchedLoad {
			m.inspectionLoadInFlight = false
		}
		if m.screen != screenInspection || msg.requestID != m.inspectionRequestID || msg.sessionID != m.inspectionSessionID {
			if matchedLoad {
				return m.scheduleTick()
			}
			return m, nil
		}
		m.applyInspectionResult(msg.tree, msg.err, false)
		if msg.err == nil {
			m.actionErr = nil
		}
		if matchedLoad {
			return m.scheduleTick()
		}
		return m, nil
	case launchMsg:
		if !m.launchPending || msg.requestID != m.launchRequestID {
			return m, nil
		}
		m.launchPending = false
		if msg.result.SessionID != "" {
			// A process was started (msg.err may still be a non-fatal
			// persistence warning); install the returned snapshot, leave the
			// picker for the dashboard, and select the new session.
			m.snapshot = msg.result.Snapshot
			m.reconcileSelection()
			m.selectedID = msg.result.SessionID
			m.ensureSelectionVisible()
			m.recordCPU()
			m.actionErr = msg.err
			m.leaveWorkspaceFlow()
			return m.scheduleTick()
		}
		// No process was started (resolve or launch failed): the coordinator
		// state is unchanged, so stay in the picker/editor with the error and
		// the typed draft preserved.
		m.workspaceErr = msg.err
		return m, nil
	case tea.PasteMsg:
		if m.screen == screenWorkspaceEditor && !m.launchPending {
			m.insertWorkspaceText(msg.Content)
		}
		return m, nil
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		if m.startupErr != nil {
			return m, nil
		}
		if m.launchPending {
			return m, nil
		}
		if m.screen == screenWorkspaceEditor {
			switch msg.String() {
			case "esc":
				m.screen = screenWorkspacePicker
				m.workspaceDraft = ""
				m.workspaceCursor = 0
				m.workspaceErr = nil
			case "enter":
				return m.startLaunch(m.workspaceDraft)
			case "left":
				if m.workspaceCursor > 0 {
					m.workspaceCursor--
				}
			case "right":
				if m.workspaceCursor < len([]rune(m.workspaceDraft)) {
					m.workspaceCursor++
				}
			case "home":
				m.workspaceCursor = 0
			case "end":
				m.workspaceCursor = len([]rune(m.workspaceDraft))
			case "backspace":
				m.workspaceBackspace()
			case "delete":
				m.workspaceDelete()
			case "ctrl+u":
				m.workspaceDraft = ""
				m.workspaceCursor = 0
			default:
				m.insertWorkspaceText(msg.Text)
			}
			return m, nil
		}
		if m.screen == screenWorkspacePicker {
			switch msg.String() {
			case "esc":
				m.leaveWorkspaceFlow()
				return m.scheduleTick()
			case "up":
				m.moveWorkspacePicker(-1)
			case "down":
				m.moveWorkspacePicker(1)
			case "pgup":
				m.moveWorkspacePicker(-m.workspacePickerPageSize())
			case "pgdown":
				m.moveWorkspacePicker(m.workspacePickerPageSize())
			case "enter":
				if len(m.workspaceChoices) == 0 {
					return m, nil
				}
				choice := m.workspaceChoices[m.workspacePickerIndex]
				if choice.other {
					m.screen = screenWorkspaceEditor
					m.workspaceDraft = ""
					m.workspaceCursor = 0
					m.workspaceErr = nil
					return m, nil
				}
				return m.startLaunch(choice.path)
			}
			return m, nil
		}
		if m.screen == screenInspection {
			switch msg.String() {
			case "t":
				if m.inspectionPending {
					return m, nil
				}
				m.inspectionRaw = !m.inspectionRaw
				m.inspectionScrollOffset = 0
			case "enter":
				if m.inspectionPending {
					return m, nil
				}
				m.leaveInspection()
			case "esc":
				m.leaveInspection()
			case "r":
				if !m.started || m.refreshing || m.inspectionPending {
					return m, nil
				}
				m.generation++
				return m.startRefresh(true)
			case "up":
				m.moveInspectionScroll(-1)
			case "down":
				m.moveInspectionScroll(1)
			case "pgup":
				m.moveInspectionScroll(-m.inspectionPageSize())
			case "pgdown":
				m.moveInspectionScroll(m.inspectionPageSize())
			}
			return m, nil
		}
		if m.inspectionLoadInFlight {
			switch msg.String() {
			case "enter", "r", "c", "x":
				return m, nil
			}
		}
		switch msg.String() {
		case "esc":
			m.actionErr = nil
		case "enter":
			if m.selectedID == "" || m.refreshing {
				return m, nil
			}
			m.screen = screenInspection
			m.inspectionSessionID = m.selectedID
			m.inspectionTree = app.ProcessTree{}
			m.inspectionErr = nil
			m.inspectionPending = true
			m.inspectionLoadInFlight = true
			m.inspectionScrollOffset = 0
			m.inspectionRaw = false
			m.inspectionRequestID++
			m.inspectionLoadRequestID++
			sessionID := m.inspectionSessionID
			requestID := m.inspectionRequestID
			loadRequestID := m.inspectionLoadRequestID
			return m, func() tea.Msg {
				tree, err := m.coordinator.Inspect(sessionID)
				return inspectMsg{sessionID: sessionID, requestID: requestID, loadRequestID: loadRequestID, tree: tree, err: err}
			}
		case "r":
			if !m.started || m.refreshing {
				return m, nil
			}
			m.generation++
			return m.startRefresh(true)
		case "c":
			if !m.started || m.refreshing {
				return m, nil
			}
			return m.openLaunchPicker(core.ToolClaudeCLI)
		case "x":
			if !m.started || m.refreshing {
				return m, nil
			}
			return m.openLaunchPicker(core.ToolCodexCLI)
		case "up":
			m.moveSelection(-1)
		case "down":
			m.moveSelection(1)
		case "pgup":
			m.moveSelection(-m.pageSize())
		case "pgdown":
			m.moveSelection(m.pageSize())
		}
		return m, nil
	default:
		return m, nil
	}
}

// workspaceChoices builds the launch picker's folder list: the persisted
// recent workspaces first (most-recent first, so index 0 is the last used),
// then the current directory, then the workspaces of live sessions in
// case-insensitive order, and finally the custom-folder entry. Paths already
// listed are skipped case-insensitively so nothing appears twice.
func workspaceChoices(snapshot app.Snapshot) []workspaceChoice {
	var choices []workspaceChoice
	seen := make(map[string]bool)
	add := func(path, label string) {
		if strings.TrimSpace(path) == "" {
			return
		}
		key := strings.ToLower(path)
		if seen[key] {
			return
		}
		seen[key] = true
		choices = append(choices, workspaceChoice{path: path, label: label})
	}

	for i, path := range snapshot.RecentWorkspaces {
		label := ""
		if i == 0 {
			label = "last used"
		}
		add(path, label)
	}
	add(snapshot.CurrentDir, "current dir")

	sessionPaths := make([]string, 0, len(snapshot.Sessions))
	for _, session := range snapshot.Sessions {
		if session.State == core.StateExited {
			continue
		}
		if session.Workspace != "" {
			sessionPaths = append(sessionPaths, session.Workspace)
		}
	}
	sort.SliceStable(sessionPaths, func(i, j int) bool {
		left := strings.ToLower(sessionPaths[i])
		right := strings.ToLower(sessionPaths[j])
		if left == right {
			return sessionPaths[i] < sessionPaths[j]
		}
		return left < right
	})
	for _, path := range sessionPaths {
		add(path, "")
	}

	return append(choices, workspaceChoice{other: true})
}

// openLaunchPicker enters the workspace picker for a pending launch of tool.
// It bumps the refresh generation/request so any pending or in-flight refresh
// tick is ignored while the picker is open.
func (m Model) openLaunchPicker(tool core.Tool) (tea.Model, tea.Cmd) {
	m.generation++
	m.refreshRequestID++
	m.launchTool = tool
	m.screen = screenWorkspacePicker
	m.workspaceChoices = workspaceChoices(m.snapshot)
	m.workspacePickerIndex = 0
	m.workspaceDraft = ""
	m.workspaceCursor = 0
	m.workspaceErr = nil
	return m, nil
}

func (m *Model) leaveWorkspaceFlow() {
	m.screen = screenDashboard
	m.workspaceChoices = nil
	m.workspacePickerIndex = 0
	m.workspaceDraft = ""
	m.workspaceCursor = 0
	m.workspaceErr = nil
}

func (m *Model) moveWorkspacePicker(delta int) {
	if len(m.workspaceChoices) == 0 {
		m.workspacePickerIndex = 0
		return
	}
	m.workspacePickerIndex += delta
	if m.workspacePickerIndex < 0 {
		m.workspacePickerIndex = 0
	}
	if m.workspacePickerIndex >= len(m.workspaceChoices) {
		m.workspacePickerIndex = len(m.workspaceChoices) - 1
	}
}

func (m Model) workspacePickerPageSize() int {
	height := m.height
	if height < 1 {
		height = 24
	}
	size := height - len(m.headerLineTexts(m.width)) - 2
	if size < 1 {
		return 1
	}
	return size
}

func (m *Model) insertWorkspaceText(value string) {
	var printable []rune
	for _, r := range value {
		if r == '\r' || r == '\n' || unicode.IsControl(r) {
			continue
		}
		printable = append(printable, r)
	}
	if len(printable) == 0 {
		return
	}
	runes := []rune(m.workspaceDraft)
	cursor := min(max(m.workspaceCursor, 0), len(runes))
	updated := make([]rune, 0, len(runes)+len(printable))
	updated = append(updated, runes[:cursor]...)
	updated = append(updated, printable...)
	updated = append(updated, runes[cursor:]...)
	m.workspaceDraft = string(updated)
	m.workspaceCursor = cursor + len(printable)
}

func (m *Model) workspaceBackspace() {
	runes := []rune(m.workspaceDraft)
	if m.workspaceCursor <= 0 || len(runes) == 0 {
		return
	}
	cursor := min(m.workspaceCursor, len(runes))
	runes = append(runes[:cursor-1], runes[cursor:]...)
	m.workspaceDraft = string(runes)
	m.workspaceCursor = cursor - 1
}

func (m *Model) workspaceDelete() {
	runes := []rune(m.workspaceDraft)
	cursor := min(max(m.workspaceCursor, 0), len(runes))
	if cursor >= len(runes) {
		return
	}
	runes = append(runes[:cursor], runes[cursor+1:]...)
	m.workspaceDraft = string(runes)
}

func (m Model) scheduleTick() (tea.Model, tea.Cmd) {
	m.generation++
	generation := m.generation
	return m, m.tick(m.interval, func(time.Time) tea.Msg {
		return tickMsg{generation: generation}
	})
}

func (m Model) startRefresh(userAction bool) (tea.Model, tea.Cmd) {
	m.refreshing = true
	m.refreshRequestID++
	refreshID := m.refreshRequestID
	if m.screen == screenInspection {
		m.inspectionRequestID++
		inspectionID := m.inspectionRequestID
		sessionID := m.inspectionSessionID
		return m, func() tea.Msg {
			snapshot := m.coordinator.Refresh()
			tree, err := m.coordinator.Inspect(sessionID)
			return refreshMsg{
				requestID: refreshID, userAction: userAction,
				snapshot: snapshot, inspectionSessionID: sessionID, inspectionRequestID: inspectionID,
				inspectionTree: tree, inspectionErr: err,
			}
		}
	}
	return m, func() tea.Msg {
		return refreshMsg{requestID: refreshID, userAction: userAction, snapshot: m.coordinator.Refresh()}
	}
}

// startLaunch launches the pending tool (m.launchTool) in the given workspace
// folder. It is invoked from the workspace picker or the custom-folder editor;
// the launchMsg handler decides whether to leave the flow (a process started)
// or stay with an error (resolve or launch failed).
func (m Model) startLaunch(path string) (tea.Model, tea.Cmd) {
	m.launchPending = true
	m.launchRequestID++
	m.generation++
	m.refreshRequestID++
	m.workspaceErr = nil
	requestID := m.launchRequestID
	tool := m.launchTool
	return m, func() tea.Msg {
		result, err := m.coordinator.Launch(tool, path)
		return launchMsg{requestID: requestID, result: result, err: err}
	}
}

func (m *Model) leaveInspection() {
	m.inspectionRequestID++
	m.screen = screenDashboard
	m.inspectionSessionID = ""
	m.inspectionTree = app.ProcessTree{}
	m.inspectionErr = nil
	m.inspectionPending = false
	m.inspectionScrollOffset = 0
	m.inspectionRaw = false
}

func (m *Model) applyInspectionResult(tree app.ProcessTree, err error, refreshing bool) {
	m.inspectionPending = false
	m.inspectionErr = err
	switch {
	case err == nil:
		m.inspectionTree = tree
	case errors.Is(err, app.ErrSessionNotFound):
		m.inspectionTree.Rows = nil
		m.inspectionTree.CompactRows = nil
	case !refreshing:
		m.inspectionTree = tree
	}
	m.clampInspectionScrollOffset()
}

// inspectionPageSize reports how many document lines PgUp/PgDn moves by: the
// viewport size minus one line per "N more" indicator that would be visible
// from the current scroll position (above once scrolled down, below unless
// already at the end).
func (m Model) inspectionPageSize() int {
	total := m.inspectionRowCount()
	start := min(max(m.inspectionScrollOffset, 0), total)
	size := m.inspectionRowViewportLineCount()
	if start > 0 {
		size--
	}
	if total-start > size {
		size--
	}
	return max(1, min(size, total-start))
}

func (m *Model) moveInspectionScroll(delta int) {
	m.inspectionScrollOffset += delta
	m.clampInspectionScrollOffset()
}

// clampInspectionScrollOffset keeps the offset within [0, maxOffset] so the
// document's final line always remains reachable, reserving one line for the
// below-indicator except when the offset has scrolled past it.
func (m *Model) clampInspectionScrollOffset() {
	total := m.inspectionRowCount()
	viewport := m.inspectionRowViewportLineCount()
	maxOffset := max(0, total-max(1, viewport-1))
	m.inspectionScrollOffset = min(max(m.inspectionScrollOffset, 0), maxOffset)
}

// inspectionRowCount is always the length of the unified scrollable document:
// session context, assessment, activity, resource breakdown, and process
// tree rows combined. There is no longer a separate row-pagination path.
func (m Model) inspectionRowCount() int {
	return len(m.inspectionDocumentLines(m.width))
}

func inspectionRowsPerProcess(width int) int {
	if width < 1 {
		width = 80
	}
	switch {
	case width >= 110:
		return 1
	case width >= 60:
		return 2
	case width >= 40:
		return 3
	default:
		return 4
	}
}

func (m Model) inspectionRowViewportLineCount() int {
	return m.effectiveInspectionRowViewportLineCount(m.inspectionViewportLineCount())
}

// effectiveInspectionRowViewportLineCount reserves the visible error line
// that precedes a retained process tree. Page sizing, clamping, and rendering
// all use this same budget so navigation cannot stop before the final row.
func (m Model) effectiveInspectionRowViewportLineCount(viewportLines int) int {
	if m.inspectionErr != nil && !errors.Is(m.inspectionErr, app.ErrSessionNotFound) && len(m.inspectionTree.Rows) > 0 {
		viewportLines--
	}
	if viewportLines < 0 {
		return 0
	}
	return viewportLines
}

// inspectionViewportLineCount is the height budget left for the scrollable
// document after the fixed header and the mandatory one-line footer. Session
// context, section headings, and process-tree headings are now part of the
// document itself, so no extra fixed-line reservation is needed here.
func (m Model) inspectionViewportLineCount() int {
	width := m.width
	if width < 1 {
		width = 80
	}
	height := m.height
	if height < 1 {
		height = 24
	}
	headerLines := len(truncateHeaderLines(m.headerLines(width), height))
	viewport := height - headerLines - 1 // footer
	if viewport < 0 {
		return 0
	}
	return viewport
}

// recordCPU appends the total CPU usage across sessions to the sparkline
// history. Unsampled snapshots are skipped so a temporary collection gap is
// not misrepresented as genuine zero utilization.
func (m *Model) recordCPU() {
	total := 0.0
	sampled := false
	for _, session := range m.snapshot.Sessions {
		if len(session.Processes) == 0 {
			continue
		}
		if session.State != core.StateRunning && session.State != core.StateLeftovers {
			return
		}
		if !session.Resources.CPUAvailable {
			return
		}
		total += session.Resources.CPUPercent
		sampled = true
	}
	if !sampled {
		return
	}
	const maxSamples = 16
	m.cpuHistory = append(m.cpuHistory, total)
	if len(m.cpuHistory) > maxSamples {
		m.cpuHistory = m.cpuHistory[len(m.cpuHistory)-maxSamples:]
	}
}

func (m *Model) reconcileSelection() {
	if len(m.snapshot.Sessions) == 0 {
		m.selectedID = ""
		m.scrollOffset = 0
		return
	}
	found := false
	for _, session := range m.snapshot.Sessions {
		if session.ID == m.selectedID {
			found = true
			break
		}
	}
	if !found {
		m.selectedID = m.snapshot.Sessions[0].ID
	}
	m.clampScrollOffset()
	m.ensureSelectionVisible()
}

// layoutPlanForViewport recomputes the same budget render() uses, so scroll
// bookkeeping (page size, auto-scroll) stays consistent with what is actually
// drawn at the model's current width and height.
func (m Model) layoutPlanForViewport() layoutPlan {
	width := m.width
	if width < 1 {
		width = 80
	}
	height := m.height
	if height < 1 {
		height = 24
	}
	headerLines := len(truncateHeaderLines(m.headerLines(width), height))
	hasSessions := len(m.snapshot.Sessions) > 0
	headingEligible := width >= 80

	detailLines := 0
	if selected, hasSelection := m.selectedSession(); hasSelection {
		detailLines = detailPanelLineCountFor(selected)
	}
	return planLayout(width, height, headerLines, len(m.snapshot.Warnings) > 0, headingEligible, hasSessions, detailLines)
}

// pageSize reports how many complete sessions are rendered in the current
// window after overflow indicators take their share of the height budget.
// Paging by that capacity cannot skip a session that was never visible.
func (m Model) pageSize() int {
	plan := m.layoutPlanForViewport()
	start, end, _, _ := sessionWindow(len(m.snapshot.Sessions), m.scrollOffset, plan.viewportLines, plan.rowsPerSession)
	size := end - start
	if size < 1 {
		size = 1
	}
	return size
}

// clampScrollOffset keeps the offset within [0, total-1] (or 0 when empty)
// after the session list changes shape.
func (m *Model) clampScrollOffset() {
	total := len(m.snapshot.Sessions)
	if total == 0 {
		m.scrollOffset = 0
		return
	}
	if m.scrollOffset < 0 {
		m.scrollOffset = 0
	}
	if m.scrollOffset > total-1 {
		m.scrollOffset = total - 1
	}
}

// ensureSelectionVisible scrolls the minimum amount necessary so the selected
// session's full row (both lines in 2-line modes) is inside the viewport.
func (m *Model) ensureSelectionVisible() {
	total := len(m.snapshot.Sessions)
	if total == 0 {
		return
	}
	index := m.selectedIndex()
	plan := m.layoutPlanForViewport()

	if index < m.scrollOffset {
		m.scrollOffset = index
	}
	start, end, _, _ := sessionWindow(total, m.scrollOffset, plan.viewportLines, plan.rowsPerSession)
	for index >= end && start < total {
		m.scrollOffset++
		start, end, _, _ = sessionWindow(total, m.scrollOffset, plan.viewportLines, plan.rowsPerSession)
		if m.scrollOffset >= total {
			break
		}
	}
	for m.scrollOffset > 0 {
		candidate := m.scrollOffset - 1
		candidateStart, candidateEnd, _, _ := sessionWindow(total, candidate, plan.viewportLines, plan.rowsPerSession)
		if index < candidateStart || index >= candidateEnd {
			break
		}
		m.scrollOffset = candidate
	}
	m.clampScrollOffset()
}

func (m Model) selectedIndex() int {
	for i, session := range m.snapshot.Sessions {
		if session.ID == m.selectedID {
			return i
		}
	}
	return 0
}

func (m *Model) moveSelection(delta int) {
	if len(m.snapshot.Sessions) == 0 {
		m.selectedID = ""
		m.scrollOffset = 0
		return
	}
	index := m.selectedIndex()
	index += delta
	if index < 0 {
		index = 0
	}
	if index >= len(m.snapshot.Sessions) {
		index = len(m.snapshot.Sessions) - 1
	}
	m.selectedID = m.snapshot.Sessions[index].ID
	m.ensureSelectionVisible()
}
