package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"taskwatch/internal/app"
	"taskwatch/internal/core"
)

const DefaultRefreshInterval = 2 * time.Second

type Coordinator interface {
	Start() (app.Snapshot, error)
	Refresh() app.Snapshot
}

type startupMsg struct {
	snapshot app.Snapshot
	err      error
}

type refreshMsg struct {
	snapshot app.Snapshot
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

	snapshot     app.Snapshot
	selectedID   string
	startupErr   error
	started      bool
	refreshing   bool
	generation   uint64
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
		if !m.started || m.startupErr != nil || m.refreshing || msg.generation != m.generation {
			return m, nil
		}
		return m.startRefresh()
	case refreshMsg:
		m.snapshot = msg.snapshot
		m.refreshing = false
		m.reconcileSelection()
		m.recordCPU()
		return m.scheduleTick()
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		if m.startupErr != nil {
			return m, nil
		}
		switch msg.String() {
		case "r":
			if !m.started || m.refreshing {
				return m, nil
			}
			m.generation++
			return m.startRefresh()
		case "up", "k":
			m.moveSelection(-1)
		case "down", "j":
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

func (m Model) scheduleTick() (tea.Model, tea.Cmd) {
	m.generation++
	generation := m.generation
	return m, m.tick(m.interval, func(time.Time) tea.Msg {
		return tickMsg{generation: generation}
	})
}

func (m Model) startRefresh() (tea.Model, tea.Cmd) {
	m.refreshing = true
	return m, func() tea.Msg {
		return refreshMsg{snapshot: m.coordinator.Refresh()}
	}
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
	if _, hasSelection := m.selectedSession(); hasSelection {
		detailLines = detailPanelLineCount()
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
