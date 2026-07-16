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
		"TASKWATCH", `C:\repo\taskwatch`, "● LIVE · 03:04:05",
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

func TestBelowMinimumWidthShowsResizeMessageInEveryState(t *testing.T) {
	states := []struct {
		name  string
		model Model
	}{
		{name: "loading", model: func() Model { m := testModel(20, nil); m.started = false; return m }()},
		{name: "error", model: func() Model {
			m := testModel(20, nil)
			m.started = false
			m.startupErr = errors.New("blocked")
			return m
		}()},
		{name: "empty", model: testModel(20, nil)},
		{name: "dashboard", model: testModel(20, []core.Session{detailedSession()})},
	}

	for _, tc := range states {
		t.Run(tc.name, func(t *testing.T) {
			tc.model.height = 3
			content := tc.model.View().Content
			plain := flattenRendered(content)

			assertWithinWidth(t, content, 20)
			if len(strings.Split(content, "\n")) > tc.model.height {
				t.Fatalf("fallback exceeded height %d:\n%s", tc.model.height, content)
			}
			if !strings.Contains(plain, "Window too narrow") {
				t.Fatalf("fallback missing resize message:\n%s", content)
			}
			if strings.Contains(plain, "Warning:") || strings.Contains(plain, "TASKWATCH") {
				t.Fatalf("full dashboard rendered below minimum width:\n%s", content)
			}
		})
	}
}

// TestNarrowEmptyCopyWrapsWithoutLosingText was adapted for the friendlier
// empty-state copy: a short headline plus a muted body line. The exact
// wording changed, but the two facts it must convey (watches for Codex
// Desktop, refreshes every 2 seconds) are still asserted verbatim.
func TestNarrowEmptyCopyWrapsWithoutLosingText(t *testing.T) {
	const width = 40
	content := testModel(width, nil).View().Content

	assertWithinWidth(t, content, width)
	plain := flattenRendered(content)
	if !strings.Contains(plain, "No sessions yet") {
		t.Fatalf("width-%d empty view lost headline:\n%s", width, content)
	}
	if !strings.Contains(plain, "Watching for Codex Desktop. Refreshing every 2 seconds.") {
		t.Fatalf("width-%d empty view lost body copy:\n%s", width, content)
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
	if !strings.Contains(stripPanelBorder(content), startupErr.Error()) {
		t.Fatalf("width-%d startup error lost text or path:\n%s", width, content)
	}
}

// stripPanelBorder reconstructs the logical text of a bordered panel from
// its rendered lines: it strips ANSI escapes, then for each line strips the
// rounded-border side glyphs ("│ " / " │") along with the trailing padding
// spaces used to fill the line out to the box width, before concatenating
// lines with no separator. This lets wrapped text inside a panel be checked
// as one contiguous string, the same way plain (unbordered) wrapped text
// already reconstructs under flattenRendered.
func stripPanelBorder(content string) string {
	var result strings.Builder
	for _, line := range strings.Split(content, "\n") {
		plain := flattenRendered(line)
		plain = strings.TrimPrefix(plain, "│ ")
		plain = strings.TrimSuffix(plain, " │")
		if plain == "╭" || plain == "╮" || plain == "╰" || plain == "╯" {
			continue
		}
		trimmed := strings.TrimRight(plain, " ")
		if isBorderRule(trimmed) {
			continue
		}
		result.WriteString(trimmed)
	}
	return result.String()
}

// isBorderRule reports whether a (border-glyph-stripped) line is just a
// horizontal border rule (top/bottom of a panel) with nothing else on it.
func isBorderRule(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r != '╭' && r != '╮' && r != '╰' && r != '╯' && r != '─' {
			return false
		}
	}
	return true
}

// TestNarrowHeaderKeepsWorkspaceAndStatusDiscoverable was adapted for the
// brand-chip/status-chip header: at very narrow widths with a long unbroken
// workspace path, the header now clips (rather than wraps) the workspace so
// the status chip and its refresh time stay on their own line instead of
// being pushed out. Both the workspace's recognizable prefix and the refresh
// time remain visible, satisfying "discoverable" even though the full
// unbroken path is not guaranteed at width 40 (see TestNarrowStartupErrorWrapsWithoutLosingLongPath
// for the case where the full text must survive).
func TestNarrowHeaderKeepsWorkspaceAndStatusDiscoverable(t *testing.T) {
	const width = 40
	workspace := `C:\Users\someone\source\repos\workspace-with-a-very-long-unbroken-name\taskwatch`
	model := testModel(width, nil)
	model.snapshot.Workspace = workspace
	content := model.View().Content

	assertWithinWidth(t, content, width)
	plain := flattenRendered(content)
	if !strings.Contains(plain, "TASKWATCH") {
		t.Fatalf("width-%d header lost brand chip:\n%s", width, content)
	}
	if styledBrand := model.palette.brand.Render(" " + brand + " "); !strings.Contains(content, styledBrand) {
		t.Fatalf("width-%d header lost brand chip styling:\n%s", width, content)
	}
	if !strings.Contains(plain, `C:\Users\someone`) {
		t.Fatalf("width-%d header lost workspace prefix:\n%s", width, content)
	}
	if !strings.Contains(plain, "LIVE") || !strings.Contains(plain, "03:04:05") {
		t.Fatalf("width-%d header lost status/refresh time:\n%s", width, content)
	}
}

// TestHeaderShowsBrandChipAndStatusChip covers the header's brand chip (a
// styled background band, distinct from plain text) and its status chip:
// "LIVE" with the refresh timestamp while idle, "REFRESHING" while a refresh
// is in flight.
func TestHeaderShowsBrandChipAndStatusChip(t *testing.T) {
	idle := testModel(130, []core.Session{detailedSession()})
	idleContent := idle.View().Content
	if !strings.Contains(idleContent, "TASKWATCH") {
		t.Fatalf("idle header missing brand chip:\n%s", idleContent)
	}
	if !strings.Contains(flattenRendered(idleContent), "● LIVE · 03:04:05") {
		t.Fatalf("idle header missing LIVE status chip with refresh time:\n%s", idleContent)
	}

	brandStyled := idle.palette.brand.Render(" " + brand + " ")
	if !strings.Contains(idleContent, brandStyled) {
		t.Fatalf("brand chip is not styled with the brand palette role:\n%s", idleContent)
	}

	refreshing := idle
	refreshing.refreshing = true
	refreshingContent := refreshing.View().Content
	if !strings.Contains(flattenRendered(refreshingContent), "↻ REFRESHING") {
		t.Fatalf("refreshing header missing REFRESHING status chip:\n%s", refreshingContent)
	}
	if strings.Contains(flattenRendered(refreshingContent), "LIVE") {
		t.Fatalf("refreshing header should not also show LIVE:\n%s", refreshingContent)
	}
}

// TestNarrowFooterWrapsWithoutLosingWording was renamed from a wrapping
// expectation: the footer is now clipped to a single line at every width (see
// footerLine in view.go), so narrow widths get a shortened-but-legible
// variant instead of a second wrapped line. This keeps footer budget math a
// constant (always exactly one line), which the height-aware layout budget
// depends on.
func TestNarrowFooterStaysOneLineWithShortenedButLegibleWording(t *testing.T) {
	const width = 40
	content := testModel(width, []core.Session{detailedSession()}).View().Content

	assertWithinWidth(t, content, width)
	plain := flattenRendered(content)
	for _, want := range []string{"move", "r refresh", "close"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("width-%d footer lost shortened wording %q:\n%s", width, want, content)
		}
	}
	lines := strings.Split(content, "\n")
	footerLineCount := 0
	for _, line := range lines {
		if strings.Contains(flattenRendered(line), "refresh") {
			footerLineCount++
		}
	}
	if footerLineCount != 1 {
		t.Fatalf("width-%d footer spans %d lines, want exactly 1", width, footerLineCount)
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

	for _, want := range []string{"Claude", "Codex CLI", "Codex Desktop", "observed", "running", "exited", "leftovers", "inaccessible", "stale", "▌", "first warning +2 more", "REFRESHING"} {
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
			if !strings.HasPrefix(plain, "⚠ 3 warnings: ") {
				t.Fatalf("warning suffix appeared outside warning line: %q", plain)
			}
			return
		}
	}
	t.Fatalf("width-%d warning lost required +2 more suffix:\n%s", width, content)
}

func TestEmptyAndBlockingErrorCopy(t *testing.T) {
	empty := testModel(100, nil).View().Content
	if !strings.Contains(empty, "No sessions yet") || !strings.Contains(empty, "Watching for Codex Desktop. Refreshing every 2 seconds.") {
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

// TestLoadingScreenShowsBrandAndStartingCopy covers the static loading
// screen: brand chip plus the "Starting…" message, both present and
// width-safe.
func TestLoadingScreenShowsBrandAndStartingCopy(t *testing.T) {
	model := testModel(60, nil)
	model.started = false
	content := model.View().Content

	assertWithinWidth(t, content, 60)
	if !strings.Contains(content, "TASKWATCH") {
		t.Fatalf("loading screen missing brand:\n%s", content)
	}
	if !strings.Contains(content, "Starting TaskWatch…") {
		t.Fatalf("loading screen missing starting copy:\n%s", content)
	}

	wants := map[string]int{
		"TASKWATCH":           (60-lipgloss.Width(" "+brand+" "))/2 + 1,
		"Starting TaskWatch…": (60 - lipgloss.Width("Starting TaskWatch…")) / 2,
	}
	for text, wantColumn := range wants {
		for _, line := range strings.Split(content, "\n") {
			plain := flattenRendered(line)
			if column := strings.Index(plain, text); column >= 0 {
				if column != wantColumn {
					t.Fatalf("loading text %q starts at column %d, want centered at %d:\n%s", text, column, wantColumn, content)
				}
				break
			}
		}
	}
}

// TestLoadingErrorAndEmptyScreensRespectHeightBudget covers the height cap
// on the three screens that previously returned early without capping
// (loading, blocking startup error, and the started-but-empty state): none
// may exceed the given height or width, regardless of how small the
// terminal is.
func TestLoadingErrorAndEmptyScreensRespectHeightBudget(t *testing.T) {
	heights := []int{1, 3, 8, 24}
	widths := []int{40, 90, 130}

	for _, height := range heights {
		for _, width := range widths {
			t.Run(fmt.Sprintf("h%d_w%d", height, width), func(t *testing.T) {
				loading := testModel(width, nil)
				loading.started = false
				loading.height = height
				loadingContent := loading.View().Content
				assertWithinWidth(t, loadingContent, width)
				if got := len(strings.Split(loadingContent, "\n")); got > height {
					t.Fatalf("loading screen rendered %d lines, want <= height %d:\n%s", got, height, loadingContent)
				}

				errModel := testModel(width, nil)
				errModel.started = false
				errModel.height = height
				errModel.startupErr = errors.New(`load sessions: open C:\Users\someone\AppData\Local\TaskWatch\sessions\workspace-with-a-very-long-name\sessions.json: access denied`)
				errContent := errModel.View().Content
				assertWithinWidth(t, errContent, width)
				errLines := strings.Split(errContent, "\n")
				if got := len(errLines); got > height {
					t.Fatalf("error screen rendered %d lines, want <= height %d:\n%s", got, height, errContent)
				}
				if height >= 4 && !strings.Contains(flattenRendered(errLines[len(errLines)-1]), "Ctrl+C close") {
					t.Fatalf("error screen dropped the close hint before the error text at height %d:\n%s", height, errContent)
				}

				emptyModel := testModel(width, nil)
				emptyModel.height = height
				emptyContent := emptyModel.View().Content
				assertWithinWidth(t, emptyContent, width)
				if got := len(strings.Split(emptyContent, "\n")); got > height {
					t.Fatalf("empty screen rendered %d lines, want <= height %d:\n%s", got, height, emptyContent)
				}
			})
		}
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
			"chipText": palette.chipText, "brand": palette.brand,
		} {
			if style.GetForeground() == nil {
				t.Errorf("%s role has no foreground", name)
			}
		}
		if palette.brand.GetBackground() == nil {
			t.Error("brand role has no background (it must render as a colored chip)")
		}
	}
}

func manyDistinctSessions(n int) []core.Session {
	tools := []core.Tool{core.ToolClaudeCLI, core.ToolCodexCLI, core.ToolCodexDesktop}
	sessions := make([]core.Session, n)
	for i := range sessions {
		s := detailedSession()
		s.ID = fmt.Sprintf("session-%02d", i)
		s.Tool = tools[i%len(tools)]
		sessions[i] = s
	}
	return sessions
}

func TestLayoutPlanReservesACompleteSelectedRowWithOverflowIndicators(t *testing.T) {
	tests := []struct {
		name        string
		width       int
		headerLines int
	}{
		{name: "narrow", width: 40, headerLines: 2},
		{name: "medium", width: 90, headerLines: 1},
		{name: "wide", width: 130, headerLines: 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plan := planLayout(tc.width, 8, tc.headerLines, true, tc.width >= 80, true, detailPanelLineCount())
			wantMinimum := plan.rowsPerSession + 2
			if plan.viewportLines < wantMinimum {
				t.Fatalf("viewportLines = %d, want at least %d for one selected row and two overflow indicators; plan = %+v", plan.viewportLines, wantMinimum, plan)
			}
		})
	}
}

func TestSessionWindowPrioritizesACompleteRowBeforeOverflowIndicators(t *testing.T) {
	tests := []struct {
		name                    string
		total, offset, viewport int
		rows                    int
		wantStart, wantEnd      int
		wantAbove, wantBelow    bool
	}{
		{name: "wide exact row", total: 3, viewport: 1, rows: 1, wantEnd: 1},
		{name: "medium exact row", total: 3, viewport: 2, rows: 2, wantEnd: 1},
		{name: "top with below indicator", total: 3, viewport: 2, rows: 1, wantEnd: 1, wantBelow: true},
		{name: "middle with both indicators", total: 4, offset: 1, viewport: 3, rows: 1, wantStart: 1, wantEnd: 2, wantAbove: true, wantBelow: true},
		{name: "bottom with above indicator", total: 3, offset: 2, viewport: 2, rows: 1, wantStart: 2, wantEnd: 3, wantAbove: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			start, end, above, below := sessionWindow(tc.total, tc.offset, tc.viewport, tc.rows)
			if start != tc.wantStart || end != tc.wantEnd || above != tc.wantAbove || below != tc.wantBelow {
				t.Fatalf("sessionWindow() = (%d, %d, %v, %v), want (%d, %d, %v, %v)", start, end, above, below, tc.wantStart, tc.wantEnd, tc.wantAbove, tc.wantBelow)
			}
		})
	}
}

func TestTinyUsableViewportKeepsSelectedRowAheadOfOverflowIndicator(t *testing.T) {
	tests := []struct {
		name          string
		width, height int
	}{
		{name: "wide one-line row", width: 130, height: 3},
		{name: "medium two-line row", width: 90, height: 4},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			model := testModel(tc.width, manyDistinctSessions(3))
			model.height = tc.height
			content := model.View().Content

			assertWithinWidth(t, content, tc.width)
			if got := len(strings.Split(content, "\n")); got > tc.height {
				t.Fatalf("rendered %d lines, want <= %d:\n%s", got, tc.height, content)
			}
			if !strings.Contains(content, "▌") {
				t.Fatalf("selected row disappeared despite a complete row fitting:\n%s", content)
			}
		})
	}
}

func TestTinyHeightMatrixUsesBestEffortWithoutOverflow(t *testing.T) {
	for height := 1; height <= 7; height++ {
		for _, width := range []int{40, 90, 130} {
			t.Run(fmt.Sprintf("h%d_w%d", height, width), func(t *testing.T) {
				model := testModel(width, manyDistinctSessions(5))
				model.height = height
				content := model.View().Content

				assertWithinWidth(t, content, width)
				lines := strings.Split(content, "\n")
				if len(lines) > height {
					t.Fatalf("rendered %d lines, want <= %d:\n%s", len(lines), height, content)
				}
				if !strings.Contains(flattenRendered(lines[len(lines)-1]), "refresh") {
					t.Fatalf("footer is not last at height %d width %d:\n%s", height, width, content)
				}
				plan := model.layoutPlanForViewport()
				if plan.viewportLines >= plan.rowsPerSession && !strings.Contains(content, "▌") {
					t.Fatalf("selected row missing with %d viewport lines for a %d-line row:\n%s", plan.viewportLines, plan.rowsPerSession, content)
				}
			})
		}
	}
}

func TestOverflowIndicatorsReportExactHiddenCounts(t *testing.T) {
	model := testModel(130, manyDistinctSessions(5))
	model.height = 5
	model.scrollOffset = 1
	model.selectedID = model.snapshot.Sessions[1].ID
	content := flattenRendered(model.View().Content)

	for _, want := range []string{"↑ 1 more", "↓ 3 more"} {
		if !strings.Contains(content, want) {
			t.Fatalf("view missing exact overflow count %q:\n%s", want, model.View().Content)
		}
	}
}

// TestHeightWidthMatrixNeverOverflowsAndKeepsFooterLast covers the
// height-aware layout budget across a matrix of heights and widths, with and
// without warnings active (which also exercises the detail panel, since a
// selection always exists when sessions are present). At every combination
// the footer must survive as the last line and the total line count must
// respect the height. These supported heights must also retain the selected
// row even when warning and overflow indicators compete for the viewport.
func TestHeightWidthMatrixNeverOverflowsAndKeepsFooterLast(t *testing.T) {
	heights := []int{8, 15, 24, 50}
	widths := []int{40, 90, 130}
	sessions := manyDistinctSessions(12)

	for _, height := range heights {
		for _, width := range widths {
			for _, withWarnings := range []bool{false, true} {
				t.Run(fmt.Sprintf("h%d_w%d_warn%v", height, width, withWarnings), func(t *testing.T) {
					model := testModel(width, sessions)
					model.height = height
					if withWarnings {
						model.snapshot.Warnings = []app.Warning{{Message: "first warning"}, {Message: "second warning"}}
					}
					content := model.View().Content

					assertWithinWidth(t, content, width)

					lines := strings.Split(content, "\n")
					if len(lines) > height {
						t.Fatalf("rendered %d lines, want <= height %d:\n%s", len(lines), height, content)
					}

					last := flattenRendered(lines[len(lines)-1])
					if !strings.Contains(last, "refresh") {
						t.Fatalf("footer hint not on last line (height %d width %d): %q\nfull:\n%s", height, width, last, content)
					}

					if !strings.Contains(content, "▌") {
						t.Fatalf("selection marker missing (height %d width %d):\n%s", height, width, content)
					}
				})
			}
		}
	}
}

// TestDetailPanelShowsSelectedSessionFieldsAtTallHeight covers the
// selected-row detail panel's content: every labeled field for the selected
// session must be present, and the panel itself must be a bordered block
// (not just plain lines) distinguishable from the session table above it.
func TestDetailPanelShowsSelectedSessionFieldsAtTallHeight(t *testing.T) {
	model := testModel(130, []core.Session{detailedSession()})
	model.height = 40
	content := model.View().Content

	assertWithinWidth(t, content, 130)
	plain := flattenRendered(content)
	for _, want := range []string{
		"Selected Session",
		"Tool: ", "Codex CLI · managed",
		"State: ", "running",
		"Workspace: ", `C:\repo`,
		"Root PID: ", "0",
		"Started: ", "01:04:05", "2h ago",
		"Processes: ", "2",
		"CPU: ", "12.3%",
		"Memory: ", "1.5 MB",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("tall detail panel missing %q:\n%s", want, content)
		}
	}
	if !strings.Contains(content, "╭") || !strings.Contains(content, "╰") {
		t.Fatalf("detail panel is not bordered:\n%s", content)
	}
}

func TestDetailPanelDisplaysStartedTimeInLocalTimezone(t *testing.T) {
	local := time.FixedZone("IST", int((5*time.Hour+30*time.Minute)/time.Second))
	now := time.Date(2026, 7, 16, 16, 33, 59, 0, local)
	session := detailedSession()
	session.StartedAt = time.Date(2026, 7, 16, 9, 24, 4, 0, time.UTC)

	model := testModel(130, []core.Session{session})
	model.height = 40
	model.now = func() time.Time { return now }
	plain := flattenRendered(model.View().Content)

	if !strings.Contains(plain, "Started: 14:54:04 (1h ago)") {
		t.Fatalf("detail panel did not convert UTC start time to local time:\n%s", model.View().Content)
	}
	if strings.Contains(plain, "Started: 09:24:04") {
		t.Fatalf("detail panel displayed the raw UTC start time:\n%s", model.View().Content)
	}
}

// TestDetailPanelHiddenAtShortHeightButFooterAndSelectionSurvive covers the
// panel's degradation rule: at a short height it must be dropped entirely
// (rather than partially rendered) so the footer and session viewport keep
// their budget, while the selection marker remains visible.
func TestDetailPanelHiddenAtShortHeightButFooterAndSelectionSurvive(t *testing.T) {
	model := testModel(130, manyDistinctSessions(12))
	model.height = 8
	content := model.View().Content

	assertWithinWidth(t, content, 130)
	if strings.Contains(content, "Selected Session") || strings.Contains(content, "╭") {
		t.Fatalf("detail panel should be hidden at short height:\n%s", content)
	}
	lines := strings.Split(content, "\n")
	if len(lines) > model.height {
		t.Fatalf("rendered %d lines, want <= height %d:\n%s", len(lines), model.height, content)
	}
	last := flattenRendered(lines[len(lines)-1])
	if !strings.Contains(last, "refresh") {
		t.Fatalf("footer hint not on last line at short height:\n%s", content)
	}
	if !strings.Contains(content, "▌") {
		t.Fatalf("selection marker missing at short height:\n%s", content)
	}
}

// TestDetailPanelAbsentWithoutSelection covers the "no selection" case: with
// no sessions there is nothing to select, so the panel must not appear even
// at a generously tall height.
func TestDetailPanelAbsentWithoutSelection(t *testing.T) {
	model := testModel(130, nil)
	model.height = 40
	content := model.View().Content

	if strings.Contains(content, "Selected Session") {
		t.Fatalf("detail panel should not appear without a selection:\n%s", content)
	}
}

// TestScrollFollowsSelectionToLastSessionAndBack exercises the scrolling
// viewport: paging/moving to the last session must bring it into view with an
// "above" overflow indicator, and returning to the top must show a "below"
// indicator instead.
func TestScrollFollowsSelectionToLastSessionAndBack(t *testing.T) {
	sessions := manyDistinctSessions(12)

	for _, method := range []string{"down", "pgdown"} {
		t.Run(method, func(t *testing.T) {
			model := testModel(130, sessions)
			model.height = 10

			steps := len(sessions)
			if method == "pgdown" {
				steps = 4
			}
			for i := 0; i < steps+2; i++ {
				model, _ = update(t, model, key(method))
			}
			if model.selectedID != sessions[len(sessions)-1].ID {
				t.Fatalf("%s: selected = %q, want last session %q", method, model.selectedID, sessions[len(sessions)-1].ID)
			}

			content := model.View().Content
			assertWithinWidth(t, content, 130)
			if len(strings.Split(content, "\n")) > model.height {
				t.Fatalf("%s: rendered content exceeds height %d:\n%s", method, model.height, content)
			}
			if !strings.Contains(content, "▌") {
				t.Fatalf("%s: selection marker missing after scrolling to end:\n%s", method, content)
			}
			if !strings.Contains(content, toolLabel(sessions[len(sessions)-1].Tool)) {
				t.Fatalf("%s: last session's tool label not visible after scrolling:\n%s", method, content)
			}
			if !strings.Contains(content, "↑") || !strings.Contains(content, "more") {
				t.Fatalf("%s: expected an '↑ N more' overflow indicator once scrolled past the top:\n%s", method, content)
			}
		})
	}

	// Symmetric check: scroll back up to the first session.
	model := testModel(130, sessions)
	model.height = 10
	for i := 0; i < len(sessions)+2; i++ {
		model, _ = update(t, model, key("down"))
	}
	for i := 0; i < len(sessions)+2; i++ {
		model, _ = update(t, model, key("up"))
	}
	if model.selectedID != sessions[0].ID {
		t.Fatalf("selected = %q, want first session %q after scrolling back up", model.selectedID, sessions[0].ID)
	}
	content := model.View().Content
	assertWithinWidth(t, content, 130)
	if !strings.Contains(content, "▌") {
		t.Fatalf("selection marker missing after scrolling back to top:\n%s", content)
	}
	if !strings.Contains(content, "↓") || !strings.Contains(content, "more") {
		t.Fatalf("expected a '↓ N more' overflow indicator once scrolled back to the top:\n%s", content)
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
