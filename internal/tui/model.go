package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"taskwatch/internal/app"
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

	snapshot   app.Snapshot
	selectedID string
	startupErr error
	started    bool
	refreshing bool
	generation uint64
	width      int
	palette    palette
	cpuHistory []float64
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
// history. Nothing is recorded until at least one session reports a sample, so
// the sparkline does not start with a meaningless flat zero.
func (m *Model) recordCPU() {
	total := 0.0
	sampled := false
	for _, session := range m.snapshot.Sessions {
		if session.Resources.CPUAvailable {
			total += session.Resources.CPUPercent
			sampled = true
		}
	}
	if !sampled && len(m.cpuHistory) == 0 {
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
		return
	}
	for _, session := range m.snapshot.Sessions {
		if session.ID == m.selectedID {
			return
		}
	}
	m.selectedID = m.snapshot.Sessions[0].ID
}

func (m *Model) moveSelection(delta int) {
	if len(m.snapshot.Sessions) == 0 {
		m.selectedID = ""
		return
	}
	index := 0
	for i, session := range m.snapshot.Sessions {
		if session.ID == m.selectedID {
			index = i
			break
		}
	}
	index += delta
	if index < 0 {
		index = 0
	}
	if index >= len(m.snapshot.Sessions) {
		index = len(m.snapshot.Sessions) - 1
	}
	m.selectedID = m.snapshot.Sessions[index].ID
}
