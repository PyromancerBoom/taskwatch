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
	startSnapshot    app.Snapshot
	startErr         error
	startCalls       int
	refreshCalls     int
	refreshSnapshot  app.Snapshot
	workspaceCalls   []string
	workspaceResults []app.Snapshot
	workspaceErrs    []error
	launchCalls      []core.Tool
	launchResults    []app.LaunchResult
	launchErrs       []error
	inspectCalls     []string
	inspectTrees     []app.ProcessTree
	inspectErrs      []error
	events           []string
}

func (c *stubCoordinator) SetWorkspace(path string) (app.Snapshot, error) {
	call := len(c.workspaceCalls)
	c.workspaceCalls = append(c.workspaceCalls, path)
	var snapshot app.Snapshot
	if call < len(c.workspaceResults) {
		snapshot = c.workspaceResults[call]
	}
	var err error
	if call < len(c.workspaceErrs) {
		err = c.workspaceErrs[call]
	}
	return snapshot, err
}

func (c *stubCoordinator) Launch(tool core.Tool) (app.LaunchResult, error) {
	call := len(c.launchCalls)
	c.launchCalls = append(c.launchCalls, tool)
	var result app.LaunchResult
	if call < len(c.launchResults) {
		result = c.launchResults[call]
	}
	var err error
	if call < len(c.launchErrs) {
		err = c.launchErrs[call]
	}
	return result, err
}

func (c *stubCoordinator) Start() (app.Snapshot, error) {
	c.startCalls++
	return c.startSnapshot, c.startErr
}

func (c *stubCoordinator) Refresh() app.Snapshot {
	c.refreshCalls++
	c.events = append(c.events, "refresh")
	return c.refreshSnapshot
}

func (c *stubCoordinator) Inspect(sessionID string) (app.ProcessTree, error) {
	call := len(c.inspectCalls)
	c.inspectCalls = append(c.inspectCalls, sessionID)
	c.events = append(c.events, "inspect:"+sessionID)
	var tree app.ProcessTree
	if call < len(c.inspectTrees) {
		tree = c.inspectTrees[call]
	}
	var err error
	if call < len(c.inspectErrs) {
		err = c.inspectErrs[call]
	}
	return tree, err
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

func TestWOpensOrderedWorkspacePickerAndInvalidatesTimer(t *testing.T) {
	coordinator := &stubCoordinator{}
	model := NewModel(coordinator, time.Second)
	var calls []tickCall
	model.tick = fakeTick(&calls)
	sessions := []core.Session{
		{ID: "one", Workspace: `C:\zeta`},
		{ID: "two", Workspace: `C:\Alpha`},
		{ID: "three", Workspace: `c:\alpha`},
		{ID: "four", Workspace: ""},
		{ID: "five", Workspace: `C:\active`},
	}
	model, _ = update(t, model, startupMsg{snapshot: app.Snapshot{Workspace: `C:\active`, Sessions: sessions}})
	oldTick := calls[0].msg

	model, cmd := update(t, model, key("w"))

	if cmd != nil || model.screen != screenWorkspacePicker {
		t.Fatalf("open picker: screen=%v cmd=%v", model.screen, cmd)
	}
	want := []workspaceChoice{
		{path: `C:\active`},
		{path: `C:\Alpha`},
		{path: `C:\zeta`},
		{other: true},
	}
	if !reflect.DeepEqual(model.workspaceChoices, want) || model.workspacePickerIndex != 0 {
		t.Fatalf("workspace choices/index = %#v/%d, want %#v/0", model.workspaceChoices, model.workspacePickerIndex, want)
	}
	if model.snapshot.Sessions[0].Workspace != `C:\zeta` || model.snapshot.Sessions[1].Workspace != `C:\Alpha` {
		t.Fatalf("opening picker rewrote original session workspaces: %#v", model.snapshot.Sessions)
	}

	model, staleCmd := update(t, model, oldTick)
	if staleCmd != nil || model.refreshing || coordinator.refreshCalls != 0 {
		t.Fatalf("stale picker tick: cmd=%v refreshing=%v refresh calls=%d", staleCmd, model.refreshing, coordinator.refreshCalls)
	}
}

func TestEscapeClosesWorkspacePickerAndResumesTimerWithoutCoordinatorCall(t *testing.T) {
	coordinator := &stubCoordinator{}
	model := NewModel(coordinator, time.Second)
	var calls []tickCall
	model.tick = fakeTick(&calls)
	model, _ = update(t, model, startupMsg{snapshot: app.Snapshot{Workspace: `C:\active`}})
	model, _ = update(t, model, key("w"))

	model, cmd := update(t, model, key("esc"))

	if model.screen != screenDashboard || cmd == nil || len(calls) != 2 {
		t.Fatalf("escape picker: screen=%v cmd nil=%v ticks=%d", model.screen, cmd == nil, len(calls))
	}
	if len(coordinator.workspaceCalls) != 0 || coordinator.refreshCalls != 0 || model.snapshot.Workspace != `C:\active` {
		t.Fatalf("escape mutated coordinator/model: workspace calls=%v refresh=%d snapshot=%#v", coordinator.workspaceCalls, coordinator.refreshCalls, model.snapshot)
	}
}

func workspaceModel(t *testing.T, coordinator *stubCoordinator, sessions []core.Session) (Model, *[]tickCall) {
	t.Helper()
	model := NewModel(coordinator, time.Second)
	calls := new([]tickCall)
	model.tick = fakeTick(calls)
	model, _ = update(t, model, startupMsg{snapshot: app.Snapshot{Workspace: `C:\active`, Sessions: sessions}})
	model, _ = update(t, model, key("w"))
	return model, calls
}

func TestWorkspacePickerMovementAndActiveSelection(t *testing.T) {
	coordinator := &stubCoordinator{}
	model, ticks := workspaceModel(t, coordinator, []core.Session{
		{ID: "one", Workspace: `C:\one`},
		{ID: "two", Workspace: `C:\two`},
	})

	model, _ = update(t, model, key("pgdown"))
	if got := model.workspaceChoices[model.workspacePickerIndex]; !got.other {
		t.Fatalf("PgDown choice = %#v, want synthetic row", got)
	}
	model, _ = update(t, model, key("pgup"))
	if model.workspacePickerIndex != 0 {
		t.Fatalf("PgUp index = %d, want active row 0", model.workspacePickerIndex)
	}

	model, cmd := update(t, model, key("enter"))
	if model.screen != screenDashboard || cmd == nil || len(*ticks) != 2 || len(coordinator.workspaceCalls) != 0 {
		t.Fatalf("active enter: screen=%v cmd nil=%v ticks=%d calls=%v", model.screen, cmd == nil, len(*ticks), coordinator.workspaceCalls)
	}
}

func TestWorkspaceKnownSelectionValidatesAsynchronouslyAndInstallsSuccess(t *testing.T) {
	want := app.Snapshot{
		Workspace: `C:\other`,
		Sessions: []core.Session{
			{ID: "old", Workspace: `C:\original`},
			{ID: "new", Workspace: `C:\other`},
		},
	}
	coordinator := &stubCoordinator{workspaceResults: []app.Snapshot{want}}
	model, ticks := workspaceModel(t, coordinator, []core.Session{{ID: "old", Workspace: `C:\other`}})
	model.actionErr = errors.New("old launch failure")
	model, _ = update(t, model, key("down"))

	model, cmd := update(t, model, key("enter"))
	if !model.workspacePending || cmd == nil || len(coordinator.workspaceCalls) != 0 {
		t.Fatalf("known submit: pending=%v cmd nil=%v synchronous calls=%v", model.workspacePending, cmd == nil, coordinator.workspaceCalls)
	}
	result := cmd()
	if !reflect.DeepEqual(coordinator.workspaceCalls, []string{`C:\other`}) {
		t.Fatalf("SetWorkspace calls = %v, want C:\\other", coordinator.workspaceCalls)
	}
	model, nextTick := update(t, model, result)
	if model.screen != screenDashboard || model.workspacePending || model.workspaceErr != nil || model.actionErr != nil {
		t.Fatalf("workspace success state: screen=%v pending=%v workspace err=%v action err=%v", model.screen, model.workspacePending, model.workspaceErr, model.actionErr)
	}
	if !reflect.DeepEqual(model.snapshot, want) || model.selectedID != "old" || nextTick == nil || len(*ticks) != 2 {
		t.Fatalf("workspace success: snapshot=%#v selected=%q tick nil=%v ticks=%d", model.snapshot, model.selectedID, nextTick == nil, len(*ticks))
	}
	if model.snapshot.Sessions[0].Workspace != `C:\original` {
		t.Fatalf("TUI rewrote returned session workspace: %#v", model.snapshot.Sessions)
	}
}

func TestWorkspaceOtherEditorHasTwoLayerCancellation(t *testing.T) {
	coordinator := &stubCoordinator{}
	model, ticks := workspaceModel(t, coordinator, nil)
	model, _ = update(t, model, key("down"))

	model, cmd := update(t, model, key("enter"))
	if model.screen != screenWorkspaceEditor || model.workspaceDraft != "" || model.workspaceCursor != 0 || cmd != nil {
		t.Fatalf("open editor: screen=%v draft=%q cursor=%d cmd=%v", model.screen, model.workspaceDraft, model.workspaceCursor, cmd)
	}
	model, cmd = update(t, model, key("esc"))
	if model.screen != screenWorkspacePicker || cmd != nil || len(*ticks) != 1 {
		t.Fatalf("editor escape: screen=%v cmd=%v ticks=%d", model.screen, cmd, len(*ticks))
	}
	model, cmd = update(t, model, key("esc"))
	if model.screen != screenDashboard || cmd == nil || len(*ticks) != 2 || len(coordinator.workspaceCalls) != 0 {
		t.Fatalf("picker escape: screen=%v cmd nil=%v ticks=%d calls=%v", model.screen, cmd == nil, len(*ticks), coordinator.workspaceCalls)
	}
}

func TestWorkspaceEditorOperationsPasteAndLiteralKQ(t *testing.T) {
	model, _ := workspaceModel(t, &stubCoordinator{}, nil)
	model, _ = update(t, model, key("down"))
	model, _ = update(t, model, key("enter"))

	model, _ = update(t, model, tea.KeyPressMsg(tea.Key{Code: 'k', Text: "kqAB"}))
	model, _ = update(t, model, key("left"))
	model, _ = update(t, model, key("left"))
	model, _ = update(t, model, key("backspace"))
	model, _ = update(t, model, key("delete"))
	model, _ = update(t, model, key("home"))
	model, _ = update(t, model, key("right"))
	model, _ = update(t, model, tea.PasteMsg{Content: ":\nq\r\tk\x00"})
	if model.workspaceDraft != "k:qkB" || model.workspaceCursor != 4 {
		t.Fatalf("edited draft/cursor = %q/%d, want %q/4", model.workspaceDraft, model.workspaceCursor, "k:qkB")
	}
	model, _ = update(t, model, key("end"))
	if model.workspaceCursor != 5 {
		t.Fatalf("End cursor = %d, want 5", model.workspaceCursor)
	}
	model, _ = update(t, model, key("ctrl+u"))
	if model.workspaceDraft != "" || model.workspaceCursor != 0 {
		t.Fatalf("Ctrl+U draft/cursor = %q/%d, want blank/0", model.workspaceDraft, model.workspaceCursor)
	}
}

func TestWorkspaceValidationFailureStaysInlineWithDraftAndBlocksKeysWhilePending(t *testing.T) {
	wantErr := errors.New("folder does not exist")
	coordinator := &stubCoordinator{workspaceErrs: []error{wantErr}}
	model, ticks := workspaceModel(t, coordinator, nil)
	model, _ = update(t, model, key("down"))
	model, _ = update(t, model, key("enter"))
	model, _ = update(t, model, tea.PasteMsg{Content: `C:\missing\kq`})

	model, cmd := update(t, model, key("enter"))
	if !model.workspacePending || cmd == nil {
		t.Fatalf("editor submit: pending=%v cmd nil=%v", model.workspacePending, cmd == nil)
	}
	pending := model
	model, ignored := update(t, model, key("esc"))
	if ignored != nil || model.screen != pending.screen || model.workspaceDraft != pending.workspaceDraft || !model.workspacePending {
		t.Fatalf("pending Esc mutated editor: before=%#v after=%#v cmd=%v", pending, model, ignored)
	}

	model, nextCmd := update(t, model, cmd())
	if model.screen != screenWorkspaceEditor || model.workspacePending || !errors.Is(model.workspaceErr, wantErr) || model.workspaceDraft != `C:\missing\kq` {
		t.Fatalf("validation failure: screen=%v pending=%v err=%v draft=%q", model.screen, model.workspacePending, model.workspaceErr, model.workspaceDraft)
	}
	if nextCmd != nil || len(*ticks) != 1 || !reflect.DeepEqual(coordinator.workspaceCalls, []string{`C:\missing\kq`}) {
		t.Fatalf("validation failure resumed work: cmd=%v ticks=%d calls=%v", nextCmd, len(*ticks), coordinator.workspaceCalls)
	}
}

func launchModel(t *testing.T, coordinator *stubCoordinator, snapshot app.Snapshot) (Model, *[]tickCall) {
	t.Helper()
	model := NewModel(coordinator, time.Second)
	calls := new([]tickCall)
	model.tick = fakeTick(calls)
	model.width = 130
	model.height = 8
	model, _ = update(t, model, startupMsg{snapshot: snapshot})
	return model, calls
}

func TestLaunchKeysMapExactlyAndRunAsynchronously(t *testing.T) {
	for _, tc := range []struct {
		key  string
		tool core.Tool
	}{{key: "c", tool: core.ToolClaudeCLI}, {key: "x", tool: core.ToolCodexCLI}} {
		t.Run(tc.key, func(t *testing.T) {
			coordinator := &stubCoordinator{launchResults: []app.LaunchResult{{Snapshot: app.Snapshot{Workspace: `C:\repo`}}}}
			model, _ := launchModel(t, coordinator, app.Snapshot{Workspace: `C:\repo`})

			model, cmd := update(t, model, key(tc.key))
			if !model.launchPending || model.launchTool != tc.tool || cmd == nil || len(coordinator.launchCalls) != 0 {
				t.Fatalf("launch start: pending=%v tool=%q cmd nil=%v calls=%v", model.launchPending, model.launchTool, cmd == nil, coordinator.launchCalls)
			}
			result := cmd()
			if !reflect.DeepEqual(coordinator.launchCalls, []core.Tool{tc.tool}) {
				t.Fatalf("Launch calls = %v, want [%s]", coordinator.launchCalls, tc.tool)
			}
			model, _ = update(t, model, result)
			if model.launchPending || model.actionErr != nil {
				t.Fatalf("launch completion: pending=%v err=%v", model.launchPending, model.actionErr)
			}
		})
	}
}

func TestActionsDoNotOverlapRefreshInspectionWorkspaceOrLaunch(t *testing.T) {
	t.Run("refresh blocks actions", func(t *testing.T) {
		coordinator := &stubCoordinator{}
		model, _ := launchModel(t, coordinator, app.Snapshot{Sessions: []core.Session{session("one")}})
		model, refreshCmd := update(t, model, key("r"))
		if refreshCmd == nil || !model.refreshing {
			t.Fatal("precondition: refresh did not start")
		}
		for _, name := range []string{"c", "x", "w", "enter"} {
			var cmd tea.Cmd
			model, cmd = update(t, model, key(name))
			if cmd != nil || model.screen != screenDashboard {
				t.Fatalf("refresh allowed %q: cmd=%v screen=%v", name, cmd, model.screen)
			}
		}
		if len(coordinator.launchCalls) != 0 || len(coordinator.workspaceCalls) != 0 || len(coordinator.inspectCalls) != 0 {
			t.Fatalf("refresh overlap calls: launch=%v workspace=%v inspect=%v", coordinator.launchCalls, coordinator.workspaceCalls, coordinator.inspectCalls)
		}
	})

	t.Run("launch blocks duplicate and other actions", func(t *testing.T) {
		coordinator := &stubCoordinator{}
		model, ticks := launchModel(t, coordinator, app.Snapshot{Sessions: []core.Session{session("one")}})
		oldTick := (*ticks)[0].msg
		model, launchCmd := update(t, model, key("c"))
		if launchCmd == nil || !model.launchPending {
			t.Fatal("precondition: launch did not start")
		}
		for _, message := range []tea.Msg{key("c"), key("x"), key("r"), key("w"), key("enter"), oldTick} {
			var cmd tea.Cmd
			model, cmd = update(t, model, message)
			if cmd != nil {
				t.Fatalf("launch pending allowed %T/%v", message, message)
			}
		}
		if len(coordinator.launchCalls) != 0 || coordinator.refreshCalls != 0 || len(coordinator.workspaceCalls) != 0 || len(coordinator.inspectCalls) != 0 {
			t.Fatalf("pending launch caused calls: launch=%v refresh=%d workspace=%v inspect=%v", coordinator.launchCalls, coordinator.refreshCalls, coordinator.workspaceCalls, coordinator.inspectCalls)
		}
	})

	t.Run("inspection load blocks refresh action", func(t *testing.T) {
		model, _ := launchModel(t, &stubCoordinator{}, app.Snapshot{Sessions: []core.Session{session("one")}})
		model.selectedID = "one"
		model, inspectCmd := update(t, model, key("enter"))
		if inspectCmd == nil || !model.inspectionPending {
			t.Fatal("precondition: inspection did not start")
		}
		model, refreshCmd := update(t, model, key("r"))
		if refreshCmd != nil || model.refreshing {
			t.Fatalf("inspection load allowed refresh: cmd=%v refreshing=%v", refreshCmd, model.refreshing)
		}
	})
}

func TestLaunchSuccessSelectsAndScrollsLaunchedSessionIntoView(t *testing.T) {
	sessions := manySessions(12)
	for i := range sessions {
		sessions[i].Workspace = fmt.Sprintf(`C:\session-%02d`, i)
	}
	launched := session("launched")
	launched.Workspace = `C:\launched-visible`
	wantSnapshot := app.Snapshot{Workspace: `C:\repo`, Sessions: append(append([]core.Session(nil), sessions...), launched)}
	coordinator := &stubCoordinator{launchResults: []app.LaunchResult{{Snapshot: wantSnapshot, SessionID: "launched"}}}
	model, _ := launchModel(t, coordinator, app.Snapshot{Workspace: `C:\repo`, Sessions: sessions})

	model, cmd := update(t, model, key("x"))
	model, _ = update(t, model, cmd())

	if !reflect.DeepEqual(model.snapshot, wantSnapshot) || model.selectedID != "launched" || model.scrollOffset == 0 {
		t.Fatalf("launch success: snapshot match=%v selected=%q offset=%d", reflect.DeepEqual(model.snapshot, wantSnapshot), model.selectedID, model.scrollOffset)
	}
	if !strings.Contains(flattenRendered(model.View().Content), `C:\launched-visible`) || !strings.Contains(model.View().Content, "▌") {
		t.Fatalf("launched session is not immediately visible:\n%s", model.View().Content)
	}
}

func TestLaunchSupervisorErrorInstallsSnapshotAndSetsActionError(t *testing.T) {
	wantErr := errors.New("supervisor unavailable")
	wantSnapshot := app.Snapshot{Workspace: `C:\still-current`, Sessions: []core.Session{session("old")}}
	coordinator := &stubCoordinator{
		launchResults: []app.LaunchResult{{Snapshot: wantSnapshot}},
		launchErrs:    []error{wantErr},
	}
	model, ticks := launchModel(t, coordinator, app.Snapshot{Workspace: `C:\before`})

	model, cmd := update(t, model, key("c"))
	model, nextTick := update(t, model, cmd())

	if model.launchPending || !reflect.DeepEqual(model.snapshot, wantSnapshot) || !errors.Is(model.actionErr, wantErr) || nextTick == nil || len(*ticks) != 2 {
		t.Fatalf("launch error: pending=%v snapshot=%#v err=%v tick nil=%v ticks=%d", model.launchPending, model.snapshot, model.actionErr, nextTick == nil, len(*ticks))
	}
}

func TestLaunchPersistenceErrorKeepsUpdatedSnapshotSelectionAndBannerUntilDismissed(t *testing.T) {
	wantErr := errors.New("save launched session: disk full")
	launched := session("new-session")
	launched.Workspace = `C:\new-session`
	wantSnapshot := app.Snapshot{Workspace: `C:\repo`, Sessions: []core.Session{session("old"), launched}}
	coordinator := &stubCoordinator{
		launchResults:   []app.LaunchResult{{Snapshot: wantSnapshot, SessionID: "new-session"}},
		launchErrs:      []error{wantErr},
		refreshSnapshot: wantSnapshot,
	}
	model, ticks := launchModel(t, coordinator, app.Snapshot{Workspace: `C:\repo`, Sessions: []core.Session{session("old")}})

	model, launchCmd := update(t, model, key("x"))
	model, _ = update(t, model, launchCmd())
	if !reflect.DeepEqual(model.snapshot, wantSnapshot) || model.selectedID != "new-session" || !errors.Is(model.actionErr, wantErr) {
		t.Fatalf("persistence error result: snapshot=%#v selected=%q err=%v", model.snapshot, model.selectedID, model.actionErr)
	}

	model, refreshCmd := update(t, model, (*ticks)[1].msg)
	model, _ = update(t, model, refreshCmd())
	if !errors.Is(model.actionErr, wantErr) {
		t.Fatalf("periodic refresh cleared action error: %v", model.actionErr)
	}
	model, dismissCmd := update(t, model, key("esc"))
	if dismissCmd != nil || model.actionErr != nil || !reflect.DeepEqual(model.snapshot, wantSnapshot) || coordinator.refreshCalls != 1 {
		t.Fatalf("Esc dismissal: cmd=%v err=%v snapshot=%#v refresh=%d", dismissCmd, model.actionErr, model.snapshot, coordinator.refreshCalls)
	}
}

func TestLaterSuccessfulLaunchClearsActionError(t *testing.T) {
	wantErr := errors.New("first launch failed")
	snapshot := app.Snapshot{Workspace: `C:\repo`}
	coordinator := &stubCoordinator{
		launchResults: []app.LaunchResult{{Snapshot: snapshot}, {Snapshot: snapshot}},
		launchErrs:    []error{wantErr, nil},
	}
	model, _ := launchModel(t, coordinator, snapshot)
	model, firstCmd := update(t, model, key("c"))
	model, _ = update(t, model, firstCmd())
	model, secondCmd := update(t, model, key("x"))
	model, _ = update(t, model, secondCmd())
	if model.actionErr != nil || !reflect.DeepEqual(coordinator.launchCalls, []core.Tool{core.ToolClaudeCLI, core.ToolCodexCLI}) {
		t.Fatalf("successful launch: err=%v calls=%v", model.actionErr, coordinator.launchCalls)
	}
}

func TestStaleLaunchAndRefreshResultsCannotOverwriteNewerSnapshots(t *testing.T) {
	refreshed := app.Snapshot{Workspace: `C:\refreshed`, Sessions: []core.Session{session("refreshed")}}
	launched := app.Snapshot{Workspace: `C:\launched`, Sessions: []core.Session{session("launched")}}
	coordinator := &stubCoordinator{
		refreshSnapshot: refreshed,
		launchResults:   []app.LaunchResult{{Snapshot: launched, SessionID: "launched"}},
	}
	model, _ := launchModel(t, coordinator, app.Snapshot{Workspace: `C:\initial`})
	model, refreshCmd := update(t, model, key("r"))
	oldRefreshResult := refreshCmd()
	model, _ = update(t, model, oldRefreshResult)

	model, launchCmd := update(t, model, key("c"))
	requestID := model.launchRequestID
	model, _ = update(t, model, launchMsg{requestID: requestID - 1, result: app.LaunchResult{Snapshot: refreshed}})
	if !model.launchPending || model.snapshot.Workspace != `C:\refreshed` {
		t.Fatalf("stale launch result mutated pending launch: pending=%v snapshot=%#v", model.launchPending, model.snapshot)
	}
	model, _ = update(t, model, launchCmd())
	model, staleCmd := update(t, model, oldRefreshResult)
	if staleCmd != nil || !reflect.DeepEqual(model.snapshot, launched) || model.selectedID != "launched" {
		t.Fatalf("stale refresh overwrote launch: cmd=%v snapshot=%#v selected=%q", staleCmd, model.snapshot, model.selectedID)
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
	if model.selectedID != "one" {
		t.Fatalf("j moved selection to %q", model.selectedID)
	}
	model, _ = update(t, model, key("down"))
	model, _ = update(t, model, key("down"))
	model, _ = update(t, model, key("down"))
	if model.selectedID != "three" {
		t.Fatalf("down selection = %q, want three", model.selectedID)
	}
	model, _ = update(t, model, key("k"))
	if model.selectedID != "three" {
		t.Fatalf("k moved selection to %q", model.selectedID)
	}
}

func TestKAndQAreActionNoOpsAcrossDashboardPickerAndInspection(t *testing.T) {
	for _, screen := range []screenState{screenDashboard, screenWorkspacePicker, screenInspection} {
		t.Run(fmt.Sprintf("screen_%d", screen), func(t *testing.T) {
			coordinator := &stubCoordinator{}
			model := NewModel(coordinator, time.Second)
			model.started = true
			model.screen = screen
			model.snapshot = app.Snapshot{Workspace: `C:\repo`, Sessions: []core.Session{session("one"), session("two")}}
			model.selectedID = "two"
			model.workspaceChoices = workspaceChoices(model.snapshot)
			model.workspacePickerIndex = 1
			model.inspectionSessionID = "two"
			model.inspectionTree = app.ProcessTree{Rows: []core.ProcessTreeRow{{}, {}, {}}}
			model.inspectionScrollOffset = 1

			for _, name := range []string{"k", "q"} {
				beforeSelected := model.selectedID
				beforePicker := model.workspacePickerIndex
				beforeInspection := model.inspectionScrollOffset
				beforeGeneration := model.generation
				var cmd tea.Cmd
				model, cmd = update(t, model, key(name))
				if cmd != nil || model.selectedID != beforeSelected || model.workspacePickerIndex != beforePicker || model.inspectionScrollOffset != beforeInspection || model.generation != beforeGeneration {
					t.Fatalf("%q mutated screen %v: cmd=%v selected=%q picker=%d inspection=%d generation=%d", name, screen, cmd, model.selectedID, model.workspacePickerIndex, model.inspectionScrollOffset, model.generation)
				}
			}
			if coordinator.refreshCalls != 0 || len(coordinator.workspaceCalls) != 0 || len(coordinator.launchCalls) != 0 || len(coordinator.inspectCalls) != 0 {
				t.Fatalf("no-op keys called coordinator: %#v", coordinator)
			}
		})
	}
}

func TestCtrlCQuitsFromEveryScreen(t *testing.T) {
	for _, screen := range []screenState{screenDashboard, screenWorkspacePicker, screenWorkspaceEditor, screenInspection} {
		t.Run(fmt.Sprintf("screen_%d", screen), func(t *testing.T) {
			model := NewModel(&stubCoordinator{}, time.Second)
			model.started = true
			model.screen = screen
			_, cmd := update(t, model, key("ctrl+c"))
			if cmd == nil {
				t.Fatal("Ctrl+C returned nil")
			}
			if _, ok := cmd().(tea.QuitMsg); !ok {
				t.Fatalf("Ctrl+C message = %T, want tea.QuitMsg", cmd())
			}
		})
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

func TestEnterDispatchesInspectionForSelectedSessionAndInstallsResult(t *testing.T) {
	wantTree := app.ProcessTree{
		Session: session("two"),
		Rows:    []core.ProcessTreeRow{{Process: core.Process{PID: 42, Name: "codex"}, Depth: 1}},
	}
	coordinator := &stubCoordinator{inspectTrees: []app.ProcessTree{wantTree}}
	model := NewModel(coordinator, time.Second)
	model.started = true
	model.snapshot.Sessions = []core.Session{session("one"), session("two")}
	model.selectedID = "two"

	model, cmd := update(t, model, key("enter"))
	if model.screen != screenInspection || !model.inspectionPending || model.inspectionSessionID != "two" || cmd == nil {
		t.Fatalf("inspection start: screen=%v pending=%v session=%q cmd nil=%v", model.screen, model.inspectionPending, model.inspectionSessionID, cmd == nil)
	}
	if len(coordinator.inspectCalls) != 0 {
		t.Fatalf("Inspect called synchronously: %v", coordinator.inspectCalls)
	}

	result := cmd()
	if !reflect.DeepEqual(coordinator.inspectCalls, []string{"two"}) {
		t.Fatalf("Inspect calls = %v, want [two]", coordinator.inspectCalls)
	}
	model, _ = update(t, model, result)
	if model.inspectionPending || model.inspectionErr != nil || !reflect.DeepEqual(model.inspectionTree, wantTree) {
		t.Fatalf("inspection result: pending=%v err=%v tree=%#v", model.inspectionPending, model.inspectionErr, model.inspectionTree)
	}
}

func TestEnterWithoutSelectionDoesNotInspect(t *testing.T) {
	coordinator := &stubCoordinator{}
	model := NewModel(coordinator, time.Second)
	model.started = true

	model, cmd := update(t, model, key("enter"))

	if cmd != nil || model.screen != screenDashboard || len(coordinator.inspectCalls) != 0 {
		t.Fatalf("empty enter: screen=%v cmd=%v inspect calls=%v", model.screen, cmd, coordinator.inspectCalls)
	}
}

func TestInspectionAcceptsErrorsAndAnEmptyTreeAsTerminalStates(t *testing.T) {
	wantErr := errors.New("process query denied")
	tests := []struct {
		name string
		tree app.ProcessTree
		err  error
	}{
		{name: "error", err: wantErr},
		{name: "empty tree", tree: app.ProcessTree{Session: session("one")}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			coordinator := &stubCoordinator{inspectTrees: []app.ProcessTree{tc.tree}, inspectErrs: []error{tc.err}}
			model := NewModel(coordinator, time.Second)
			model.started = true
			model.snapshot.Sessions = []core.Session{session("one")}
			model.selectedID = "one"

			model, cmd := update(t, model, key("enter"))
			model, _ = update(t, model, cmd())

			if model.screen != screenInspection || model.inspectionPending || !errors.Is(model.inspectionErr, tc.err) || !reflect.DeepEqual(model.inspectionTree, tc.tree) {
				t.Fatalf("terminal inspection: screen=%v pending=%v err=%v tree=%#v", model.screen, model.inspectionPending, model.inspectionErr, model.inspectionTree)
			}
		})
	}
}

func TestEscapeCancelsPendingInspectionAndRejectsItsLateResult(t *testing.T) {
	lateTree := app.ProcessTree{Session: session("one"), Rows: []core.ProcessTreeRow{{Process: core.Process{PID: 99}}}}
	coordinator := &stubCoordinator{inspectTrees: []app.ProcessTree{lateTree}}
	model := NewModel(coordinator, time.Second)
	model.started = true
	model.snapshot.Sessions = []core.Session{session("one")}
	model.selectedID = "one"

	model, inspectCmd := update(t, model, key("enter"))
	requestID := model.inspectionRequestID
	model, backCmd := update(t, model, key("esc"))
	if backCmd != nil || model.screen != screenDashboard || model.inspectionRequestID <= requestID {
		t.Fatalf("escape: screen=%v request=%d want >%d cmd=%v", model.screen, model.inspectionRequestID, requestID, backCmd)
	}

	model, lateCmd := update(t, model, inspectCmd())
	if lateCmd == nil || model.screen != screenDashboard || len(model.inspectionTree.Rows) != 0 || model.inspectionErr != nil {
		t.Fatalf("late result mutated canceled inspection: screen=%v tree=%#v err=%v cmd=%v", model.screen, model.inspectionTree, model.inspectionErr, lateCmd)
	}
}

func TestTickDoesNotRefreshWhileInitialInspectionIsInFlight(t *testing.T) {
	coordinator := &stubCoordinator{}
	model := NewModel(coordinator, time.Second)
	var ticks []tickCall
	model.tick = fakeTick(&ticks)
	model, _ = update(t, model, startupMsg{snapshot: app.Snapshot{Sessions: []core.Session{session("one")}}})
	model.selectedID = "one"

	model, inspectCmd := update(t, model, key("enter"))
	if inspectCmd == nil || !model.inspectionPending {
		t.Fatal("precondition: initial inspection did not start")
	}
	model, tickCmd := update(t, model, ticks[0].msg)

	if tickCmd != nil || model.refreshing || coordinator.refreshCalls != 0 {
		t.Fatalf("tick overlapped initial inspection: cmd=%v refreshing=%v refresh calls=%d", tickCmd, model.refreshing, coordinator.refreshCalls)
	}
}

func TestEscapeKeepsCoordinatorActionsBlockedUntilLateInitialInspectionReturns(t *testing.T) {
	coordinator := &stubCoordinator{}
	model := NewModel(coordinator, time.Second)
	model.started = true
	model.snapshot = app.Snapshot{Workspace: `C:\repo`, Sessions: []core.Session{session("one")}}
	model.selectedID = "one"

	model, inspectCmd := update(t, model, key("enter"))
	model, _ = update(t, model, key("esc"))
	if model.screen != screenDashboard {
		t.Fatalf("Esc screen = %v, want dashboard", model.screen)
	}
	for _, name := range []string{"c", "w", "r"} {
		next, cmd := update(t, model, key(name))
		if cmd != nil || next.screen != screenDashboard {
			t.Fatalf("%q was allowed before late inspect result: cmd=%v screen=%v", name, cmd, next.screen)
		}
	}

	model, lateCmd := update(t, model, inspectCmd())
	if lateCmd == nil || model.screen != screenDashboard || len(model.inspectionTree.Rows) != 0 {
		t.Fatalf("late canceled inspection mutated data: cmd=%v screen=%v tree=%#v", lateCmd, model.screen, model.inspectionTree)
	}
	model, launchCmd := update(t, model, key("c"))
	if launchCmd == nil || !model.launchPending {
		t.Fatalf("action remained blocked after matching late result: cmd=%v pending=%v", launchCmd, model.launchPending)
	}
}

func TestInitialInspectionAndSuccessfulUserInspectionRefreshClearActionError(t *testing.T) {
	wantErr := errors.New("previous launch failed")
	snapshot := app.Snapshot{Sessions: []core.Session{session("one")}}
	coordinator := &stubCoordinator{
		refreshSnapshot: snapshot,
		inspectTrees: []app.ProcessTree{
			{Session: session("one")},
			{Session: session("one"), Rows: []core.ProcessTreeRow{{Process: core.Process{PID: 42}}}},
		},
	}
	model := NewModel(coordinator, time.Second)
	model.started = true
	model.snapshot = snapshot
	model.selectedID = "one"
	model.actionErr = wantErr
	model.tick = fakeTick(new([]tickCall))

	model, inspectCmd := update(t, model, key("enter"))
	model, _ = update(t, model, inspectCmd())
	if model.actionErr != nil {
		t.Fatalf("successful initial inspection retained action error: %v", model.actionErr)
	}

	model.actionErr = wantErr
	model, refreshCmd := update(t, model, key("r"))
	if refreshCmd == nil {
		t.Fatal("live inspection refresh returned nil command")
	}
	model, _ = update(t, model, refreshCmd())
	if model.actionErr != nil {
		t.Fatalf("successful user inspection refresh retained action error: %v", model.actionErr)
	}
}

func TestInitialInspectionCompletionRearmsTimerAndInvalidatesOldTick(t *testing.T) {
	coordinator := &stubCoordinator{inspectTrees: []app.ProcessTree{{Session: session("one")}}}
	model := NewModel(coordinator, time.Second)
	var ticks []tickCall
	model.tick = fakeTick(&ticks)
	model, _ = update(t, model, startupMsg{snapshot: app.Snapshot{Sessions: []core.Session{session("one")}}})
	oldTick := ticks[0].msg
	model.selectedID = "one"

	model, inspectCmd := update(t, model, key("enter"))
	model, consumedCmd := update(t, model, oldTick)
	if consumedCmd != nil {
		t.Fatalf("in-flight tick returned command: %v", consumedCmd)
	}
	model, nextTick := update(t, model, inspectCmd())
	if nextTick == nil || len(ticks) != 2 {
		t.Fatalf("inspection completion timer: cmd nil=%v ticks=%d", nextTick == nil, len(ticks))
	}
	model, staleCmd := update(t, model, oldTick)
	if staleCmd != nil || model.refreshing || coordinator.refreshCalls != 0 {
		t.Fatalf("old tick survived rearm: cmd=%v refreshing=%v refresh calls=%d", staleCmd, model.refreshing, coordinator.refreshCalls)
	}
}

func TestCanceledLateInspectionCompletionRearmsTimerAndInvalidatesOldTick(t *testing.T) {
	coordinator := &stubCoordinator{inspectTrees: []app.ProcessTree{{Session: session("one"), Rows: []core.ProcessTreeRow{{Process: core.Process{PID: 99}}}}}}
	model := NewModel(coordinator, time.Second)
	var ticks []tickCall
	model.tick = fakeTick(&ticks)
	model, _ = update(t, model, startupMsg{snapshot: app.Snapshot{Sessions: []core.Session{session("one")}}})
	oldTick := ticks[0].msg
	model.selectedID = "one"

	model, inspectCmd := update(t, model, key("enter"))
	model, _ = update(t, model, key("esc"))
	model, nextTick := update(t, model, inspectCmd())
	if nextTick == nil || len(ticks) != 2 || model.screen != screenDashboard || len(model.inspectionTree.Rows) != 0 {
		t.Fatalf("canceled completion: cmd nil=%v ticks=%d screen=%v tree=%#v", nextTick == nil, len(ticks), model.screen, model.inspectionTree)
	}
	model, staleCmd := update(t, model, oldTick)
	if staleCmd != nil || model.refreshing || coordinator.refreshCalls != 0 {
		t.Fatalf("old canceled tick survived rearm: cmd=%v refreshing=%v refresh calls=%d", staleCmd, model.refreshing, coordinator.refreshCalls)
	}
}

func TestRefreshClearsActionErrorOnlyForSuccessfulUserAction(t *testing.T) {
	wantErr := errors.New("retained action error")
	inspectionErr := errors.New("inspection failed")
	for _, tc := range []struct {
		name                string
		userAction          bool
		inspectionSessionID string
		inspectionErr       error
		wantCleared         bool
	}{
		{name: "user dashboard refresh", userAction: true, wantCleared: true},
		{name: "user inspection success", userAction: true, inspectionSessionID: "one", wantCleared: true},
		{name: "user inspection failure", userAction: true, inspectionSessionID: "one", inspectionErr: inspectionErr},
		{name: "automatic dashboard refresh"},
		{name: "automatic inspection success", inspectionSessionID: "one"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := NewModel(&stubCoordinator{}, time.Second)
			model.started = true
			model.actionErr = wantErr
			model.tick = fakeTick(new([]tickCall))

			model, _ = update(t, model, refreshMsg{
				userAction: tc.userAction, snapshot: app.Snapshot{},
				inspectionSessionID: tc.inspectionSessionID, inspectionErr: tc.inspectionErr,
			})
			if tc.wantCleared && model.actionErr != nil {
				t.Fatalf("action error retained: %v", model.actionErr)
			}
			if !tc.wantCleared && !errors.Is(model.actionErr, wantErr) {
				t.Fatalf("action error cleared: %v", model.actionErr)
			}
		})
	}
}

func TestEnterDoesNotCancelPendingInspection(t *testing.T) {
	model := NewModel(&stubCoordinator{}, time.Second)
	model.started = true
	model.screen = screenInspection
	model.inspectionSessionID = "one"
	model.inspectionPending = true
	model.inspectionRequestID = 7

	model, cmd := update(t, model, key("enter"))

	if cmd != nil || model.screen != screenInspection || !model.inspectionPending || model.inspectionRequestID != 7 {
		t.Fatalf("pending enter: screen=%v pending=%v request=%d cmd=%v", model.screen, model.inspectionPending, model.inspectionRequestID, cmd)
	}
}

func TestEnterAndEscapeReturnFromCompletedInspectionWithoutCoordinatorMutation(t *testing.T) {
	for _, keyName := range []string{"enter", "esc"} {
		t.Run(keyName, func(t *testing.T) {
			coordinator := &stubCoordinator{}
			model := NewModel(coordinator, time.Second)
			model.started = true
			model.screen = screenInspection
			model.inspectionSessionID = "one"
			model.inspectionTree = app.ProcessTree{Session: session("one")}

			model, cmd := update(t, model, key(keyName))
			if model.screen != screenDashboard || cmd != nil || coordinator.refreshCalls != 0 || len(coordinator.inspectCalls) != 0 {
				t.Fatalf("back key %q: screen=%v cmd=%v refresh=%d inspect=%v", keyName, model.screen, cmd, coordinator.refreshCalls, coordinator.inspectCalls)
			}
		})
	}
}

func TestInspectionRefreshesSnapshotThenSameSessionTreeAtomically(t *testing.T) {
	for _, trigger := range []string{"automatic", "manual"} {
		t.Run(trigger, func(t *testing.T) {
			oldTree := app.ProcessTree{Session: session("one"), Rows: []core.ProcessTreeRow{{Process: core.Process{PID: 1}}}}
			newSession := session("one")
			newSession.Processes = []core.Process{{PID: 2}}
			newTree := app.ProcessTree{Session: newSession, Rows: []core.ProcessTreeRow{{Process: core.Process{PID: 2}}}}
			coordinator := &stubCoordinator{
				refreshSnapshot: app.Snapshot{Workspace: "fresh", Sessions: []core.Session{newSession}},
				inspectTrees:    []app.ProcessTree{newTree},
			}
			model := NewModel(coordinator, time.Second)
			var ticks []tickCall
			model.tick = fakeTick(&ticks)
			model.started = true
			model.snapshot = app.Snapshot{Workspace: "old", Sessions: []core.Session{session("one")}}
			model.selectedID = "one"
			model.screen = screenInspection
			model.inspectionSessionID = "one"
			model.inspectionTree = oldTree
			scheduled, _ := model.scheduleTick()
			model = scheduled.(Model)

			var refreshCmd tea.Cmd
			if trigger == "automatic" {
				model, refreshCmd = update(t, model, ticks[0].msg)
			} else {
				model, refreshCmd = update(t, model, key("r"))
			}
			if !model.refreshing || refreshCmd == nil {
				t.Fatalf("refresh dispatch: refreshing=%v cmd nil=%v", model.refreshing, refreshCmd == nil)
			}
			_, overlap := update(t, model, key("r"))
			if overlap != nil {
				t.Fatal("overlapped an inspection refresh")
			}
			if model.snapshot.Workspace != "old" || model.inspectionTree.Rows[0].Process.PID != 1 {
				t.Fatal("model changed before the combined command completed")
			}

			result := refreshCmd()
			if !reflect.DeepEqual(coordinator.events, []string{"refresh", "inspect:one"}) {
				t.Fatalf("coordinator order = %v, want refresh then inspect:one", coordinator.events)
			}
			model, nextTick := update(t, model, result)
			if model.refreshing || model.snapshot.Workspace != "fresh" || model.inspectionTree.Rows[0].Process.PID != 2 || nextTick == nil {
				t.Fatalf("combined result: refreshing=%v workspace=%q tree=%#v next tick nil=%v", model.refreshing, model.snapshot.Workspace, model.inspectionTree, nextTick == nil)
			}
		})
	}
}

func TestInspectionRefreshKeepsScreenWhenSessionDisappears(t *testing.T) {
	coordinator := &stubCoordinator{
		refreshSnapshot: app.Snapshot{Workspace: "fresh"},
		inspectErrs:     []error{app.ErrSessionNotFound},
	}
	model := NewModel(coordinator, time.Second)
	model.started = true
	model.snapshot.Sessions = []core.Session{session("one")}
	model.selectedID = "one"
	model.screen = screenInspection
	model.inspectionSessionID = "one"
	model.inspectionTree = app.ProcessTree{Session: session("one"), Rows: []core.ProcessTreeRow{{Process: core.Process{PID: 1}}}}
	model.tick = fakeTick(new([]tickCall))

	model, cmd := update(t, model, key("r"))
	if cmd == nil {
		t.Fatal("inspection refresh returned nil command")
	}
	model, _ = update(t, model, cmd())

	if model.screen != screenInspection || !errors.Is(model.inspectionErr, app.ErrSessionNotFound) || len(model.inspectionTree.Rows) != 0 {
		t.Fatalf("disappearance: screen=%v err=%v tree=%#v", model.screen, model.inspectionErr, model.inspectionTree)
	}
}

func TestInspectionScrollingAndResizeRefreshClamping(t *testing.T) {
	rows := make([]core.ProcessTreeRow, 12)
	for i := range rows {
		rows[i].Process = core.Process{PID: uint32(i + 1), Name: fmt.Sprintf("p%02d", i)}
	}
	coordinator := &stubCoordinator{
		refreshSnapshot: app.Snapshot{Sessions: []core.Session{session("one")}},
		inspectTrees:    []app.ProcessTree{{Session: session("one"), Rows: rows[:2]}},
	}
	model := NewModel(coordinator, time.Second)
	model.started = true
	model.width = 130
	model.height = 9
	model.snapshot.Sessions = []core.Session{session("one")}
	model.selectedID = "one"
	model.screen = screenInspection
	model.inspectionSessionID = "one"
	model.inspectionTree = app.ProcessTree{Session: session("one"), Rows: rows}
	model.tick = fakeTick(new([]tickCall))

	for i := 0; i < 30; i++ {
		model, _ = update(t, model, key("down"))
	}
	if model.inspectionScrollOffset <= 0 || model.inspectionScrollOffset >= len(rows) {
		t.Fatalf("down offset = %d, want clamped within process rows", model.inspectionScrollOffset)
	}
	for i := 0; i < 30; i++ {
		model, _ = update(t, model, key("pgup"))
	}
	if model.inspectionScrollOffset != 0 {
		t.Fatalf("page-up offset = %d, want 0", model.inspectionScrollOffset)
	}
	for i := 0; i < 30; i++ {
		model, _ = update(t, model, key("pgdown"))
	}
	if model.inspectionScrollOffset == 0 {
		t.Fatal("page-down did not scroll")
	}

	model, _ = update(t, model, tea.WindowSizeMsg{Width: 130, Height: 50})
	if model.inspectionScrollOffset != 0 {
		t.Fatalf("expanded resize offset = %d, want 0 when all rows fit", model.inspectionScrollOffset)
	}

	model.height = 9
	for i := 0; i < 30; i++ {
		model, _ = update(t, model, key("down"))
	}
	model, refreshCmd := update(t, model, key("r"))
	if refreshCmd == nil {
		t.Fatal("inspection refresh returned nil command")
	}
	model, _ = update(t, model, refreshCmd())
	if model.inspectionScrollOffset != 0 || len(model.inspectionTree.Rows) != 2 {
		t.Fatalf("refresh clamp: offset=%d rows=%d", model.inspectionScrollOffset, len(model.inspectionTree.Rows))
	}
}

func TestInspectionErrorBudgetKeepsPageSizeAndFinalRowReachable(t *testing.T) {
	rows := make([]core.ProcessTreeRow, 8)
	for i := range rows {
		rows[i].Process = core.Process{PID: uint32(i + 1), Name: fmt.Sprintf("process-%02d", i)}
	}
	model := NewModel(&stubCoordinator{}, time.Second)
	model.started = true
	model.width = 130
	model.height = 9
	model.screen = screenInspection
	model.inspectionSessionID = "one"
	model.inspectionTree = app.ProcessTree{Session: session("one"), Rows: rows}
	model.inspectionErr = errors.New("sampling denied")

	if got := model.inspectionPageSize(); got != 2 {
		t.Fatalf("inspectionPageSize() = %d, want 2 after reserving the retained-error line", got)
	}
	for i := 0; i < len(rows)+2; i++ {
		model, _ = update(t, model, key("down"))
	}

	content := flattenRendered(model.View().Content)
	if !strings.Contains(content, "process-07") {
		t.Fatalf("final process row is unreachable with a retained error; offset=%d:\n%s", model.inspectionScrollOffset, model.View().Content)
	}
}

func TestCtrlCQuitsFromInspection(t *testing.T) {
	model := NewModel(&stubCoordinator{}, time.Second)
	model.started = true
	model.screen = screenInspection

	_, quitCmd := update(t, model, key("ctrl+c"))
	if quitCmd == nil {
		t.Fatal("Ctrl+C returned nil from inspection")
	}
	if _, ok := quitCmd().(tea.QuitMsg); !ok {
		t.Fatalf("Ctrl+C command message = %T, want tea.QuitMsg", quitCmd())
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
	case "ctrl+u":
		return tea.KeyPressMsg(tea.Key{Code: 'u', Mod: tea.ModCtrl})
	case "enter":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})
	case "esc":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyEsc})
	case "left":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyLeft})
	case "right":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyRight})
	case "home":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyHome})
	case "end":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyEnd})
	case "backspace":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace})
	case "delete":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyDelete})
	default:
		return tea.KeyPressMsg(tea.Key{Code: rune(name[0]), Text: name})
	}
}
