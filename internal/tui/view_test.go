package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"taskwatch/internal/app"
	"taskwatch/internal/core"
)

var viewNow = time.Date(2026, 7, 15, 3, 4, 5, 0, time.Local)

func testModel(width int, sessions []core.Session) Model {
	model := NewModel(&stubCoordinator{}, DefaultRefreshInterval)
	model.width = width
	model.started = true
	model.now = func() time.Time { return viewNow }
	model.snapshot = app.Snapshot{
		Workspace:   `C:\repo\taskwatch`,
		Sessions:    sessions,
		RefreshedAt: viewNow,
	}
	if len(sessions) > 0 {
		model.selectedID = sessions[0].ID
	}
	return model
}

func detailedSession() core.Session {
	return core.Session{
		ID:         "codex",
		Tool:       core.ToolCodexCLI,
		Confidence: core.ConfidenceSupervised,
		Workspace:  `C:\repo`,
		StartedAt:  viewNow.Add(-2 * time.Hour),
		Processes:  []core.Process{{PID: 1}, {PID: 2}},
		State:      core.StateRunning,
		Resources: core.ResourceUsage{
			CPUPercent:      12.3,
			MemoryBytes:     1572864,
			CPUAvailable:    true,
			MemoryAvailable: true,
		},
	}
}

func TestFullRenderingShowsHeaderEveryColumnAndFooter(t *testing.T) {
	content := testModel(130, []core.Session{detailedSession()}).View().Content
	for _, want := range []string{
		"TASKWATCH", `C:\repo\taskwatch`, "Updated 03:04:05",
		"APP", "TRACKING", "STATUS", "PROCESSES", "CPU", "MEMORY", "AGE", "FOLDER",
		"Codex CLI", "managed", "running", "2", "12.3%", "1.5 MB", "2h", `C:\repo`,
		"↑/↓ or j/k move", "r refresh", "Ctrl+C close (apps keep running)",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("full view missing %q:\n%s", want, content)
		}
	}
	if strings.Contains(content, "q quit") {
		t.Fatalf("footer advertises unsupported q key:\n%s", content)
	}
}

func TestMediumAndNarrowRenderingKeepRequiredFieldsAndFolder(t *testing.T) {
	for _, width := range []int{90, 60} {
		content := testModel(width, []core.Session{detailedSession()}).View().Content
		for _, want := range []string{"Codex CLI", "managed", "running", "2", "12.3%", "1.5 MB", "2h", `C:\repo`} {
			if !strings.Contains(content, want) {
				t.Errorf("width %d missing %q:\n%s", width, want, content)
			}
		}
		assertWithinWidth(t, content, width)
	}
}

func TestRenderingNeverOverflowsWidthsAtLeastForty(t *testing.T) {
	session := detailedSession()
	session.Workspace = `C:\a very long workspace folder\with\many\nested\segments\that must truncate safely`
	for _, width := range []int{40, 55, 79, 80, 109, 110, 130} {
		model := testModel(width, []core.Session{session})
		model.snapshot.Workspace = session.Workspace
		assertWithinWidth(t, model.View().Content, width)
	}
}

func TestNarrowEmptyCopyWrapsWithoutLosingText(t *testing.T) {
	const width = 40
	const emptyCopy = "No sessions found. Watching for Codex Desktop. Refreshing every 2 seconds."
	content := testModel(width, nil).View().Content

	assertWithinWidth(t, content, width)
	if !strings.Contains(flattenRendered(content), emptyCopy) {
		t.Fatalf("width-%d empty view lost exact copy:\n%s", width, content)
	}
}

func TestNarrowStartupErrorWrapsWithoutLosingLongPath(t *testing.T) {
	const width = 40
	startupErr := errors.New(`load sessions: open C:\Users\someone\AppData\Local\TaskWatch\sessions\workspace-with-a-very-long-name\sessions.json: access denied`)
	model := testModel(width, nil)
	model.started = false
	model.startupErr = startupErr
	content := model.View().Content

	assertWithinWidth(t, content, width)
	if !strings.Contains(flattenRendered(content), startupErr.Error()) {
		t.Fatalf("width-%d startup error lost text or path:\n%s", width, content)
	}
}

func TestNarrowHeaderWrapsWithoutLosingWorkspaceOrUpdatedField(t *testing.T) {
	const width = 40
	workspace := `C:\Users\someone\source\repos\workspace-with-a-very-long-unbroken-name\taskwatch`
	model := testModel(width, nil)
	model.snapshot.Workspace = workspace
	content := model.View().Content

	assertWithinWidth(t, content, width)
	wantHeader := "TASKWATCH  " + workspace + "  Updated 03:04:05"
	if !strings.Contains(flattenRendered(content), wantHeader) {
		t.Fatalf("width-%d header lost workspace or update field:\n%s", width, content)
	}
}

func TestNarrowFooterWrapsWithoutLosingWording(t *testing.T) {
	const width = 40
	const footer = "↑/↓ or j/k move  •  r refresh  •  Ctrl+C close (apps keep running)"
	content := testModel(width, []core.Session{detailedSession()}).View().Content

	assertWithinWidth(t, content, width)
	if !strings.Contains(flattenRendered(content), footer) {
		t.Fatalf("width-%d footer lost wording:\n%s", width, content)
	}
}

func TestLabelsSelectionWarningSummaryAndRefreshingMarker(t *testing.T) {
	tools := []core.Tool{core.ToolClaudeCLI, core.ToolCodexCLI, core.ToolCodexDesktop, core.ToolCodexCLI, core.ToolCodexCLI}
	states := []core.State{core.StateRunning, core.StateExited, core.StateLeftovers, core.StateInaccessible, core.StateStale}
	sessions := make([]core.Session, len(states))
	for i := range sessions {
		sessions[i] = core.Session{
			ID:         fmt.Sprintf("session-%d", i),
			Tool:       tools[i],
			Confidence: core.ConfidenceObserved,
			Workspace:  fmt.Sprintf(`C:\r%d`, i),
			StartedAt:  viewNow,
			State:      states[i],
		}
	}
	model := testModel(130, sessions)
	model.refreshing = true
	model.snapshot.Warnings = []app.Warning{{Message: "first warning"}, {Message: "second warning"}, {Message: "third warning"}}
	content := model.View().Content

	for _, want := range []string{"Claude", "Codex CLI", "Codex Desktop", "observed", "running", "exited", "leftovers", "inaccessible", "stale", "▌", "first warning +2 more", "Refreshing…"} {
		if !strings.Contains(content, want) {
			t.Errorf("view missing %q:\n%s", want, content)
		}
	}
}

func TestNarrowLongWarningKeepsMoreSuffixVisible(t *testing.T) {
	const width = 40
	model := testModel(width, nil)
	model.snapshot.Warnings = []app.Warning{
		{Message: "the first warning message is deliberately much longer than the available narrow line"},
		{Message: "second warning"},
		{Message: "third warning"},
	}
	content := model.View().Content

	assertWithinWidth(t, content, width)
	for _, line := range strings.Split(content, "\n") {
		plain := flattenRendered(line)
		if strings.Contains(plain, "+2 more") {
			if !strings.HasPrefix(plain, "Warning: ") {
				t.Fatalf("warning suffix appeared outside warning line: %q", plain)
			}
			return
		}
	}
	t.Fatalf("width-%d warning lost required +2 more suffix:\n%s", width, content)
}

func TestEmptyAndBlockingErrorCopy(t *testing.T) {
	empty := testModel(100, nil).View().Content
	const emptyCopy = "No sessions found. Watching for Codex Desktop. Refreshing every 2 seconds."
	if !strings.Contains(empty, emptyCopy) {
		t.Fatalf("empty view missing exact copy:\n%s", empty)
	}

	model := testModel(100, nil)
	model.started = false
	model.startupErr = errors.New(`load sessions: open C:\private\sessions.json: access denied`)
	blocked := model.View().Content
	if !strings.Contains(blocked, model.startupErr.Error()) || strings.Contains(blocked, "r refresh") || !strings.Contains(blocked, "Ctrl+C close (apps keep running)") {
		t.Fatalf("blocking error view has wrong content:\n%s", blocked)
	}
}

func TestCPUAndMemoryFormatting(t *testing.T) {
	base := detailedSession()
	base.Workspace = `C:\r`
	cases := []struct {
		name      string
		state     core.State
		processes int
		resources core.ResourceUsage
		wantCPU   string
		wantMem   string
	}{
		{name: "sampling", state: core.StateRunning, processes: 1, wantCPU: "sampling…", wantMem: "—"},
		{name: "stale unavailable", state: core.StateStale, processes: 1, wantCPU: "—", wantMem: "—"},
		{name: "available bytes", state: core.StateRunning, resources: core.ResourceUsage{CPUAvailable: true, CPUPercent: 8.5, MemoryAvailable: true, MemoryBytes: 512}, wantCPU: "8.5%", wantMem: "512 B"},
		{name: "kilobytes", state: core.StateRunning, resources: core.ResourceUsage{MemoryAvailable: true, MemoryBytes: 1536}, wantCPU: "—", wantMem: "1.5 KB"},
		{name: "megabytes", state: core.StateRunning, resources: core.ResourceUsage{MemoryAvailable: true, MemoryBytes: 1572864}, wantCPU: "—", wantMem: "1.5 MB"},
		{name: "gigabytes", state: core.StateRunning, resources: core.ResourceUsage{MemoryAvailable: true, MemoryBytes: 1610612736}, wantCPU: "—", wantMem: "1.5 GB"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			session := base
			session.ID = tc.name
			session.State = tc.state
			session.Processes = make([]core.Process, tc.processes)
			session.Resources = tc.resources
			content := testModel(130, []core.Session{session}).View().Content
			if !strings.Contains(content, tc.wantCPU) || !strings.Contains(content, tc.wantMem) {
				t.Fatalf("resource view wants CPU %q memory %q:\n%s", tc.wantCPU, tc.wantMem, content)
			}
		})
	}
}

func TestCompactAgeUnits(t *testing.T) {
	ages := []struct {
		duration time.Duration
		want     string
	}{{45 * time.Second, "45s"}, {5 * time.Minute, "5m"}, {2 * time.Hour, "2h"}, {72 * time.Hour, "3d"}}
	for i, age := range ages {
		session := detailedSession()
		session.ID = fmt.Sprintf("age-%d", i)
		session.StartedAt = viewNow.Add(-age.duration)
		content := testModel(130, []core.Session{session}).View().Content
		if !strings.Contains(content, age.want) {
			t.Errorf("age %s missing %q:\n%s", age.duration, age.want, content)
		}
	}
}

func TestPaletteDefinesAllSemanticRoles(t *testing.T) {
	for _, palette := range []palette{newPalette(true), newPalette(false)} {
		for name, style := range map[string]lipgloss.Style{
			"text": palette.text, "muted": palette.muted, "focus": palette.focus,
			"healthy": palette.healthy, "warning": palette.warning, "error": palette.failure,
		} {
			if style.GetForeground() == nil {
				t.Errorf("%s role has no foreground", name)
			}
		}
	}
}

func assertWithinWidth(t *testing.T, content string, width int) {
	t.Helper()
	for lineNo, line := range strings.Split(content, "\n") {
		if got := lipgloss.Width(line); got > width {
			t.Errorf("line %d width = %d, want <= %d: %q", lineNo+1, got, width, line)
		}
	}
}

func flattenRendered(content string) string {
	var result strings.Builder
	escapeState := 0
	for _, r := range content {
		switch escapeState {
		case 1:
			if r == '[' {
				escapeState = 2
			} else {
				escapeState = 0
			}
			continue
		case 2:
			if r >= 0x40 && r <= 0x7e {
				escapeState = 0
			}
			continue
		}
		if r == '\x1b' {
			escapeState = 1
			continue
		}
		if r != '\n' && r != '\r' {
			result.WriteRune(r)
		}
	}
	return result.String()
}
