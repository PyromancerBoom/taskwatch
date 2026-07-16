package tui

import (
	"errors"
	"fmt"
	"image/color"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"taskwatch/internal/app"
	"taskwatch/internal/core"
)

type stubCoordinator struct {
	startSnapshot   app.Snapshot
	startErr        error
	startCalls      int
	refreshCalls    int
	refreshSnapshot app.Snapshot
}

func (c *stubCoordinator) Start() (app.Snapshot, error) {
	c.startCalls++
	return c.startSnapshot, c.startErr
}

func (c *stubCoordinator) Refresh() app.Snapshot {
	c.refreshCalls++
	return c.refreshSnapshot
}

func TestInitStartsCoordinatorAndRequestsBackgroundColor(t *testing.T) {
	coordinator := &stubCoordinator{}
	model := NewModel(coordinator, time.Second)

	cmd := model.Init()
	if cmd == nil {
		t.Fatal("Init returned nil command")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("Init command = %#v, want two-command tea.BatchMsg", cmd())
	}

	backgroundCommand := reflect.ValueOf(tea.RequestBackgroundColor).Pointer()
	var gotStartup, gotBackgroundRequest bool
	for _, child := range batch {
		if reflect.ValueOf(child).Pointer() == backgroundCommand {
			gotBackgroundRequest = true
			continue
		}
		if _, ok := child().(startupMsg); ok {
			gotStartup = true
		}
	}
	if !gotStartup || !gotBackgroundRequest || coordinator.startCalls != 1 {
		t.Fatalf("startup=%v background request=%v start calls=%d", gotStartup, gotBackgroundRequest, coordinator.startCalls)
	}
}

type tickCall struct {
	duration time.Duration
	msg      tea.Msg
}

func fakeTick(calls *[]tickCall) tickFunc {
	return func(duration time.Duration, makeMsg func(time.Time) tea.Msg) tea.Cmd {
		msg := makeMsg(time.Unix(123, 0))
		*calls = append(*calls, tickCall{duration: duration, msg: msg})
		return func() tea.Msg { return msg }
	}
}

func update(t *testing.T, model Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	next, cmd := model.Update(msg)
	result, ok := next.(Model)
	if !ok {
		t.Fatalf("Update model type = %T, want tui.Model", next)
	}
	return result, cmd
}

func session(id string) core.Session {
	return core.Session{ID: id, Tool: core.ToolCodexCLI, State: core.StateRunning}
}

func TestStartupSuccessInstallsSnapshotSelectsFirstAndSchedulesTick(t *testing.T) {
	coordinator := &stubCoordinator{}
	model := NewModel(coordinator, 37*time.Millisecond)
	var calls []tickCall
	model.tick = fakeTick(&calls)
	snapshot := app.Snapshot{Workspace: `C:\work`, Sessions: []core.Session{session("one"), session("two")}}

	model, cmd := update(t, model, startupMsg{snapshot: snapshot})

	if model.snapshot.Workspace != snapshot.Workspace || model.selectedID != "one" || !model.started {
		t.Fatalf("startup state = workspace %q selected %q started %v", model.snapshot.Workspace, model.selectedID, model.started)
	}
	if cmd == nil || len(calls) != 1 || calls[0].duration != 37*time.Millisecond {
		t.Fatalf("scheduled ticks = %#v, cmd nil = %v", calls, cmd == nil)
	}
}

func TestDefaultSchedulingUsesExactlyDefaultRefreshInterval(t *testing.T) {
	model := NewModel(&stubCoordinator{}, 0)
	var calls []tickCall
	model.tick = fakeTick(&calls)

	_, cmd := update(t, model, startupMsg{})

	if cmd == nil || len(calls) != 1 {
		t.Fatalf("default scheduling: cmd nil=%v calls=%d", cmd == nil, len(calls))
	}
	if DefaultRefreshInterval != 2*time.Second || calls[0].duration != DefaultRefreshInterval {
		t.Fatalf("default interval constant=%s scheduled=%s, want exactly 2s", DefaultRefreshInterval, calls[0].duration)
	}
}

func TestStartupErrorBlocksWithoutRefreshTick(t *testing.T) {
	model := NewModel(&stubCoordinator{}, time.Second)
	var calls []tickCall
	model.tick = fakeTick(&calls)
	want := errors.New(`load sessions: open C:\Users\me\sessions.json: access denied`)

	model, cmd := update(t, model, startupMsg{err: want})

	if !errors.Is(model.startupErr, want) || cmd != nil || len(calls) != 0 {
		t.Fatalf("startup error = %v, cmd = %v, ticks = %d", model.startupErr, cmd, len(calls))
	}
}

func TestBackgroundColorSelectsDarkAndLightPalettes(t *testing.T) {
	model := NewModel(&stubCoordinator{}, time.Second)
	initialText := model.palette.text.Render("text")

	dark, _ := update(t, model, tea.BackgroundColorMsg{Color: color.RGBA{R: 1, G: 2, B: 3, A: 255}})
	light, _ := update(t, dark, tea.BackgroundColorMsg{Color: color.RGBA{R: 250, G: 250, B: 250, A: 255}})

	if !dark.palette.dark || light.palette.dark {
		t.Fatalf("palette detection: dark=%v light=%v", dark.palette.dark, light.palette.dark)
	}
	if light.palette.text.Render("text") == initialText || light.palette.text.Render("text") == dark.palette.text.Render("text") {
		t.Fatal("BackgroundColorMsg did not change semantic text styles")
	}
}

func TestManualRefreshInvalidatesTimerAndDoesNotOverlap(t *testing.T) {
	coordinator := &stubCoordinator{refreshSnapshot: app.Snapshot{Sessions: []core.Session{session("new")}}}
	model := NewModel(coordinator, time.Second)
	var calls []tickCall
	model.tick = fakeTick(&calls)
	model, _ = update(t, model, startupMsg{snapshot: app.Snapshot{Sessions: []core.Session{session("old")}}})

	model, refreshCmd := update(t, model, key("r"))
	if !model.refreshing || refreshCmd == nil {
		t.Fatalf("manual refresh: refreshing=%v cmd nil=%v", model.refreshing, refreshCmd == nil)
	}
	model, overlapCmd := update(t, model, key("r"))
	if overlapCmd != nil {
		t.Fatal("second r overlapped refresh")
	}
	result := refreshCmd()
	if coordinator.refreshCalls != 1 {
		t.Fatalf("Refresh calls = %d, want 1", coordinator.refreshCalls)
	}
	model, nextTick := update(t, model, result)
	if model.refreshing || model.snapshot.Sessions[0].ID != "new" || nextTick == nil || len(calls) != 2 {
		t.Fatalf("refresh result: refreshing=%v sessions=%v ticks=%d", model.refreshing, model.snapshot.Sessions, len(calls))
	}
}

func TestStaleTickAfterManualRefreshCompletesStartsNoWorkOrTimer(t *testing.T) {
	coordinator := &stubCoordinator{refreshSnapshot: app.Snapshot{Sessions: []core.Session{session("new")}}}
	model := NewModel(coordinator, time.Second)
	var calls []tickCall
	model.tick = fakeTick(&calls)
	model, _ = update(t, model, startupMsg{snapshot: app.Snapshot{Sessions: []core.Session{session("old")}}})
	oldTick := calls[0].msg

	model, refreshCmd := update(t, model, key("r"))
	model, _ = update(t, model, refreshCmd())
	if model.refreshing || coordinator.refreshCalls != 1 || len(calls) != 2 {
		t.Fatalf("completed manual refresh: refreshing=%v calls=%d timers=%d", model.refreshing, coordinator.refreshCalls, len(calls))
	}

	model, staleCmd := update(t, model, oldTick)
	if staleCmd != nil || model.refreshing || coordinator.refreshCalls != 1 || len(calls) != 2 {
		t.Fatalf("stale tick: cmd nil=%v refreshing=%v calls=%d timers=%d", staleCmd == nil, model.refreshing, coordinator.refreshCalls, len(calls))
	}
}

func TestCurrentTickStartsOneRefresh(t *testing.T) {
	coordinator := &stubCoordinator{}
	model := NewModel(coordinator, DefaultRefreshInterval)
	var calls []tickCall
	model.tick = fakeTick(&calls)
	model, _ = update(t, model, startupMsg{})

	model, cmd := update(t, model, calls[0].msg)
	if !model.refreshing || cmd == nil {
		t.Fatalf("current tick: refreshing=%v cmd nil=%v", model.refreshing, cmd == nil)
	}
	_ = cmd()
	if coordinator.refreshCalls != 1 {
		t.Fatalf("Refresh calls = %d, want 1", coordinator.refreshCalls)
	}
}

func TestSelectionMovesWithoutWrapping(t *testing.T) {
	model := NewModel(&stubCoordinator{}, time.Second)
	model.started = true
	model.snapshot.Sessions = []core.Session{session("one"), session("two"), session("three")}
	model.selectedID = "one"

	model, _ = update(t, model, key("up"))
	if model.selectedID != "one" {
		t.Fatalf("up wrapped selection to %q", model.selectedID)
	}
	model, _ = update(t, model, key("j"))
	model, _ = update(t, model, key("down"))
	model, _ = update(t, model, key("down"))
	if model.selectedID != "three" {
		t.Fatalf("down selection = %q, want three", model.selectedID)
	}
	model, _ = update(t, model, key("k"))
	if model.selectedID != "two" {
		t.Fatalf("k selection = %q, want two", model.selectedID)
	}
}

func TestWindowSizeMsgSetsWidthAndHeight(t *testing.T) {
	model := NewModel(&stubCoordinator{}, time.Second)

	model, cmd := update(t, model, tea.WindowSizeMsg{Width: 100, Height: 30})

	if cmd != nil {
		t.Fatalf("WindowSizeMsg returned command %v, want nil", cmd)
	}
	if model.width != 100 || model.height != 30 {
		t.Fatalf("size = %dx%d, want 100x30", model.width, model.height)
	}
}

func TestWindowResizeKeepsSelectionVisibleAcrossRowModes(t *testing.T) {
	tests := []struct {
		name          string
		width, height int
	}{
		{name: "medium rows", width: 90, height: 8},
		{name: "narrow rows", width: 40, height: 8},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			model := NewModel(&stubCoordinator{}, time.Second)
			model.started = true
			model.width = 130
			model.height = 10
			model.snapshot.Sessions = manySessions(12)
			model.selectedID = "s00"

			for i := 0; i < len(model.snapshot.Sessions); i++ {
				model, _ = update(t, model, key("down"))
			}
			if model.selectedID != "s11" {
				t.Fatalf("precondition: selected = %q, want s11", model.selectedID)
			}

			model, _ = update(t, model, tea.WindowSizeMsg{Width: tc.width, Height: tc.height})
			if !strings.Contains(model.View().Content, "▌") {
				t.Fatalf("selected row disappeared after resize to %dx%d; offset=%d:\n%s", tc.width, tc.height, model.scrollOffset, model.View().Content)
			}
		})
	}
}

func TestWindowResizeBackfillsExpandedViewport(t *testing.T) {
	model := NewModel(&stubCoordinator{}, time.Second)
	model.started = true
	model.width = 130
	model.height = 5
	model.snapshot.Sessions = manySessions(12)
	model.selectedID = "s00"

	for i := 0; i < len(model.snapshot.Sessions); i++ {
		model, _ = update(t, model, key("down"))
	}
	if model.scrollOffset == 0 {
		t.Fatal("precondition: scrolling to the last session did not move the viewport")
	}

	model, _ = update(t, model, tea.WindowSizeMsg{Width: 130, Height: 50})
	plan := model.layoutPlanForViewport()
	start, end, _, _ := sessionWindow(len(model.snapshot.Sessions), model.scrollOffset, plan.viewportLines, plan.rowsPerSession)

	if model.selectedID != "s11" {
		t.Fatalf("selectedID = %q, want s11", model.selectedID)
	}
	if start != 0 || end != len(model.snapshot.Sessions) {
		t.Fatalf("expanded window = [%d:%d], want all %d sessions", start, end, len(model.snapshot.Sessions))
	}
}

func manySessions(n int) []core.Session {
	sessions := make([]core.Session, n)
	for i := range sessions {
		sessions[i] = session(fmt.Sprintf("s%02d", i))
	}
	return sessions
}

func TestRecordCPUDistinguishesMissingSampleFromRealZero(t *testing.T) {
	tests := []struct {
		name      string
		resources core.ResourceUsage
		want      []float64
	}{
		{name: "missing sample is skipped", want: []float64{12.3}},
		{name: "sampled zero is recorded", resources: core.ResourceUsage{CPUAvailable: true}, want: []float64{12.3, 0}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			model := NewModel(&stubCoordinator{}, time.Second)
			model.cpuHistory = []float64{12.3}
			model.snapshot.Sessions = []core.Session{{
				State:     core.StateRunning,
				Processes: []core.Process{{PID: 1}},
				Resources: tc.resources,
			}}

			model.recordCPU()

			if !reflect.DeepEqual(model.cpuHistory, tc.want) {
				t.Fatalf("cpuHistory = %v, want %v", model.cpuHistory, tc.want)
			}
		})
	}
}

func TestRecordCPUSkipsIncompleteSnapshots(t *testing.T) {
	tests := []struct {
		name     string
		sessions []core.Session
	}{
		{
			name: "stale last-known sample",
			sessions: []core.Session{{
				State:     core.StateStale,
				Processes: []core.Process{{PID: 1}},
				Resources: core.ResourceUsage{CPUAvailable: true, CPUPercent: 12.3},
			}},
		},
		{
			name: "partly sampled sessions",
			sessions: []core.Session{
				{State: core.StateRunning, Processes: []core.Process{{PID: 1}}, Resources: core.ResourceUsage{CPUAvailable: true, CPUPercent: 8}},
				{State: core.StateRunning, Processes: []core.Process{{PID: 2}}},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			model := NewModel(&stubCoordinator{}, time.Second)
			model.cpuHistory = []float64{5}
			model.snapshot.Sessions = tc.sessions

			model.recordCPU()

			if !reflect.DeepEqual(model.cpuHistory, []float64{5}) {
				t.Fatalf("cpuHistory = %v, want unchanged [5]", model.cpuHistory)
			}
		})
	}
}

func TestPageDownAndPageUpMoveByViewportWithoutWrapping(t *testing.T) {
	model := NewModel(&stubCoordinator{}, time.Second)
	model.started = true
	model.width = 130
	model.height = 10
	model.snapshot.Sessions = manySessions(12)
	model.selectedID = "s00"

	model, _ = update(t, model, key("pgdown"))
	afterFirstPage := model.selectedID
	if afterFirstPage == "s00" {
		t.Fatal("pgdn did not move selection")
	}

	for i := 0; i < 20; i++ {
		model, _ = update(t, model, key("pgdown"))
	}
	if model.selectedID != "s11" {
		t.Fatalf("pgdn clamp: selected = %q, want s11 (last session, no wrap)", model.selectedID)
	}

	for i := 0; i < 20; i++ {
		model, _ = update(t, model, key("pgup"))
	}
	if model.selectedID != "s00" {
		t.Fatalf("pgup clamp: selected = %q, want s00 (first session, no wrap)", model.selectedID)
	}
}

func TestPageMovementUsesRenderedSessionCapacity(t *testing.T) {
	for _, width := range []int{40, 90, 130} {
		for _, direction := range []string{"pgdown", "pgup"} {
			t.Run(fmt.Sprintf("width_%d_%s", width, direction), func(t *testing.T) {
				model := NewModel(&stubCoordinator{}, time.Second)
				model.started = true
				model.width = width
				model.height = 10
				model.snapshot.Sessions = manySessions(12)

				selectedIndex := 0
				if direction == "pgup" {
					selectedIndex = len(model.snapshot.Sessions) - 1
				}
				model.selectedID = model.snapshot.Sessions[selectedIndex].ID
				model.ensureSelectionVisible()

				plan := model.layoutPlanForViewport()
				start, end, _, _ := sessionWindow(len(model.snapshot.Sessions), model.scrollOffset, plan.viewportLines, plan.rowsPerSession)
				visible := end - start
				if visible < 1 {
					t.Fatalf("precondition: rendered session capacity = %d", visible)
				}
				wantIndex := selectedIndex + visible
				if direction == "pgup" {
					wantIndex = selectedIndex - visible
				}

				model, _ = update(t, model, key(direction))
				if got, want := model.selectedID, model.snapshot.Sessions[wantIndex].ID; got != want {
					t.Fatalf("%s moved selection to %q, want %q using rendered capacity %d", direction, got, want, visible)
				}
			})
		}
	}
}

func TestScrollOffsetReconciledWhenSessionListShrinks(t *testing.T) {
	model := NewModel(&stubCoordinator{}, time.Second)
	model.started = true
	model.width = 130
	model.height = 8
	model.snapshot.Sessions = manySessions(12)
	model.selectedID = "s00"
	model.tick = fakeTick(new([]tickCall))

	for i := 0; i < 20; i++ {
		model, _ = update(t, model, key("pgdown"))
	}
	if model.scrollOffset == 0 {
		t.Fatal("expected scroll offset to advance after paging down through many sessions")
	}

	model, _ = update(t, model, refreshMsg{snapshot: app.Snapshot{Sessions: manySessions(2)}})

	if model.scrollOffset != 0 {
		t.Fatalf("scrollOffset after shrink = %d, want 0 (only 2 sessions left)", model.scrollOffset)
	}
	if model.selectedID != "s00" {
		t.Fatalf("selection after shrink = %q, want fallback to first remaining session", model.selectedID)
	}
}

func TestRefreshPreservesSelectionByIDThenFallsBackAndClears(t *testing.T) {
	model := NewModel(&stubCoordinator{}, time.Second)
	model.started = true
	model.snapshot.Sessions = []core.Session{session("one"), session("two")}
	model.selectedID = "two"
	model.tick = fakeTick(new([]tickCall))

	model, _ = update(t, model, refreshMsg{snapshot: app.Snapshot{Sessions: []core.Session{session("two"), session("three")}}})
	if model.selectedID != "two" {
		t.Fatalf("preserved selection = %q, want two", model.selectedID)
	}
	model, _ = update(t, model, refreshMsg{snapshot: app.Snapshot{Sessions: []core.Session{session("three")}}})
	if model.selectedID != "three" {
		t.Fatalf("fallback selection = %q, want three", model.selectedID)
	}
	model, _ = update(t, model, refreshMsg{snapshot: app.Snapshot{}})
	if model.selectedID != "" {
		t.Fatalf("empty selection = %q, want empty", model.selectedID)
	}
}

func TestOnlyCtrlCQuitsInNormalAndErrorStates(t *testing.T) {
	for _, startupErr := range []error{nil, errors.New("blocked")} {
		model := NewModel(&stubCoordinator{}, time.Second)
		model.started = startupErr == nil
		model.startupErr = startupErr

		_, qCmd := update(t, model, key("q"))
		if qCmd != nil {
			t.Fatalf("q returned command in state error=%v", startupErr)
		}
		_, quitCmd := update(t, model, key("ctrl+c"))
		if quitCmd == nil {
			t.Fatalf("Ctrl+C returned nil in state error=%v", startupErr)
		}
		if _, ok := quitCmd().(tea.QuitMsg); !ok {
			t.Fatalf("Ctrl+C command message = %T, want tea.QuitMsg", quitCmd())
		}
	}
}

func TestViewUsesAlternateScreenTitleDefaultBackgroundAndNoMouse(t *testing.T) {
	view := NewModel(&stubCoordinator{}, time.Second).View()
	if !view.AltScreen || view.WindowTitle != "TaskWatch" || view.BackgroundColor != nil || view.OnMouse != nil {
		t.Fatalf("view config: alt=%v title=%q background=%v mouse=%v", view.AltScreen, view.WindowTitle, view.BackgroundColor, view.OnMouse != nil)
	}
}

func key(name string) tea.KeyPressMsg {
	switch name {
	case "up":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyUp})
	case "down":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyDown})
	case "pgup":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp})
	case "pgdown":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyPgDown})
	case "ctrl+c":
		return tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl})
	default:
		return tea.KeyPressMsg(tea.Key{Code: rune(name[0]), Text: name})
	}
}
