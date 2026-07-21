package app

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PyromancerBoom/taskwatch/internal/core"
)

var (
	baseTime = time.Date(2026, 7, 15, 9, 0, 0, 0, time.UTC)
	cycleOne = baseTime.Add(time.Minute)
	cycleTwo = baseTime.Add(2 * time.Minute)
)

type fakeStore struct {
	sessions        []core.Session
	loadErr         error
	saveErr         error
	saveErrs        []error
	loadCalls       int
	saveCalls       int
	saved           [][]core.Session
	recent          []string
	recentLoadErr   error
	recentSaveErr   error
	recentLoadCalls int
	recentSaveCalls int
	recentSaved     [][]string
}

func (store *fakeStore) Load() ([]core.Session, error) {
	store.loadCalls++
	return cloneTestSessions(store.sessions), store.loadErr
}

func (store *fakeStore) Save(sessions []core.Session) error {
	store.saveCalls++
	store.saved = append(store.saved, cloneTestSessions(sessions))
	if store.saveCalls <= len(store.saveErrs) {
		return store.saveErrs[store.saveCalls-1]
	}
	return store.saveErr
}

func (store *fakeStore) LoadRecentWorkspaces() ([]string, error) {
	store.recentLoadCalls++
	return append([]string(nil), store.recent...), store.recentLoadErr
}

func (store *fakeStore) SaveRecentWorkspaces(recent []string) error {
	store.recentSaveCalls++
	store.recentSaved = append(store.recentSaved, append([]string(nil), recent...))
	return store.recentSaveErr
}

// passthroughResolver is a workspace resolver that trims and echoes its input,
// letting Launch tests exercise persistence without touching the filesystem.
func passthroughResolver(_, path string) (string, error) {
	return strings.TrimSpace(path), nil
}

type launchCall struct {
	tool      core.Tool
	workspace string
}

type fakeSupervisor struct {
	recovered    []core.Session
	recoverErrs  []error
	refreshed    [][]core.Session
	refreshErrs  [][]error
	launched     core.Session
	launchErr    error
	recoverCalls [][]core.Session
	refreshCalls [][]core.Session
	launchCalls  []launchCall
	closeCalls   int
}

func (supervisor *fakeSupervisor) Launch(tool core.Tool, workspace string) (core.Session, error) {
	supervisor.launchCalls = append(supervisor.launchCalls, launchCall{tool: tool, workspace: workspace})
	return cloneSession(supervisor.launched), supervisor.launchErr
}

func (supervisor *fakeSupervisor) RecoverMany(sessions []core.Session) ([]core.Session, []error) {
	supervisor.recoverCalls = append(supervisor.recoverCalls, cloneTestSessions(sessions))
	return cloneTestSessions(supervisor.recovered), append([]error(nil), supervisor.recoverErrs...)
}

func (supervisor *fakeSupervisor) RefreshMany(sessions []core.Session) ([]core.Session, []error) {
	supervisor.refreshCalls = append(supervisor.refreshCalls, cloneTestSessions(sessions))
	call := len(supervisor.refreshCalls) - 1
	return cloneTestSessions(supervisor.refreshed[call]), append([]error(nil), supervisor.refreshErrs[call]...)
}

func (supervisor *fakeSupervisor) Close() { supervisor.closeCalls++ }

type blockingSupervisor struct {
	recovered      []core.Session
	recoverErrs    []error
	refreshed      []core.Session
	refreshErrs    []error
	recoverStarted chan struct{}
	recoverRelease chan struct{}
	refreshStarted chan struct{}
	refreshRelease chan struct{}
	closed         chan struct{}
	closeOnce      sync.Once
}

func (supervisor *blockingSupervisor) RecoverMany(sessions []core.Session) ([]core.Session, []error) {
	if supervisor.recoverStarted != nil {
		close(supervisor.recoverStarted)
		<-supervisor.recoverRelease
	}
	return cloneTestSessions(supervisor.recovered), append([]error(nil), supervisor.recoverErrs...)
}

func (supervisor *blockingSupervisor) RefreshMany(sessions []core.Session) ([]core.Session, []error) {
	if supervisor.refreshStarted != nil {
		close(supervisor.refreshStarted)
		<-supervisor.refreshRelease
	}
	return cloneTestSessions(supervisor.refreshed), append([]error(nil), supervisor.refreshErrs...)
}

func (supervisor *blockingSupervisor) Launch(core.Tool, string) (core.Session, error) {
	return core.Session{}, nil
}

func (supervisor *blockingSupervisor) Close() {
	supervisor.closeOnce.Do(func() { close(supervisor.closed) })
}

type observation struct {
	sessions []core.Session
	err      error
}

type fakeObserver struct {
	results []observation
	calls   []map[uint32]core.ProcessIdentity
}

type fakeActivityCollector struct {
	activities []core.AgentActivity
	err        error
	calls      []time.Time
}

func (collector *fakeActivityCollector) Collect(now time.Time) ([]core.AgentActivity, error) {
	collector.calls = append(collector.calls, now)
	return append([]core.AgentActivity(nil), collector.activities...), collector.err
}

func (observer *fakeObserver) Observe(excluded map[uint32]core.ProcessIdentity) ([]core.Session, error) {
	copyOfExcluded := make(map[uint32]core.ProcessIdentity, len(excluded))
	for pid, identity := range excluded {
		copyOfExcluded[pid] = identity
	}
	observer.calls = append(observer.calls, copyOfExcluded)
	result := observer.results[len(observer.calls)-1]
	return cloneTestSessions(result.sessions), result.err
}

func TestStartEmptyStoreObservesWithoutRecoveryOrSave(t *testing.T) {
	store := &fakeStore{}
	supervisor := &fakeSupervisor{}
	observed := session("desktop", core.ConfidenceObserved, core.StateRunning, baseTime, process(71, baseTime))
	observer := &fakeObserver{results: []observation{{sessions: []core.Session{observed}}}}

	coordinator := newCoordinator("C:/work", store, supervisor, observer, func() time.Time { return cycleOne })
	snapshot, err := coordinator.Start()

	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if store.loadCalls != 1 || store.saveCalls != 0 {
		t.Fatalf("store calls = load %d, save %d; want load 1, save 0", store.loadCalls, store.saveCalls)
	}
	if len(supervisor.recoverCalls) != 0 {
		t.Fatalf("RecoverMany calls = %d, want 0", len(supervisor.recoverCalls))
	}
	if len(observer.calls) != 1 || len(observer.calls[0]) != 0 {
		t.Fatalf("Observe calls/exclusions = %#v, want one empty exclusion map", observer.calls)
	}
	assertSnapshot(t, snapshot, "C:/work", []string{"desktop"}, nil, cycleOne)
}

func TestStartCollectsAndAttachesDesktopActivityWithoutChangingProcessFacts(t *testing.T) {
	store := &fakeStore{}
	processSnapshot := process(71, baseTime)
	observed := session("desktop", core.ConfidenceObserved, core.StateRunning, baseTime, processSnapshot)
	observed.Tool = core.ToolCodexDesktop
	observer := &fakeObserver{results: []observation{{sessions: []core.Session{observed}}}}
	collector := &fakeActivityCollector{activities: []core.AgentActivity{{
		ThreadID: "thread", Tool: core.ToolCodexDesktop, State: core.ActivityExecuting, UpdatedAt: cycleOne,
	}}}
	coordinator := newCoordinatorWithActivity("C:/work", store, &fakeSupervisor{}, observer, collector, func() time.Time { return cycleOne })

	snapshot, err := coordinator.Start()
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	got := findSession(t, snapshot.Sessions, "desktop")
	if len(got.Activities) != 1 || got.Activities[0].Correlation != core.CorrelationApplication {
		t.Fatalf("desktop activities = %#v", got.Activities)
	}
	if !reflect.DeepEqual(got.Processes, observed.Processes) || got.Resources != observed.Resources || got.State != observed.State {
		t.Fatalf("activity changed process facts: got %#v want %#v", got, observed)
	}
	if len(collector.calls) != 1 || !collector.calls[0].Equal(cycleOne) || store.saveCalls != 0 {
		t.Fatalf("collector/save calls = %#v/%d", collector.calls, store.saveCalls)
	}
}

func TestStartKeepsUsableActivityWhenCollectorWarnsAndDoesNotPersist(t *testing.T) {
	store := &fakeStore{}
	observed := session("desktop", core.ConfidenceObserved, core.StateRunning, baseTime, process(71, baseTime))
	observed.Tool = core.ToolCodexDesktop
	observer := &fakeObserver{results: []observation{{sessions: []core.Session{observed}}}}
	collector := &fakeActivityCollector{
		activities: []core.AgentActivity{{ThreadID: "thread", Tool: core.ToolCodexDesktop, State: core.ActivityUnknown, UpdatedAt: cycleOne}},
		err:        errors.New("compressed active rollout ignored"),
	}
	coordinator := newCoordinatorWithActivity("C:/work", store, &fakeSupervisor{}, observer, collector, func() time.Time { return cycleOne })

	snapshot, err := coordinator.Start()
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	got := findSession(t, snapshot.Sessions, "desktop")
	if len(got.Activities) != 1 || got.State != observed.State || !reflect.DeepEqual(got.Processes, observed.Processes) {
		t.Fatalf("partial activity changed/dropped session = %#v", got)
	}
	if len(snapshot.Warnings) != 1 || !strings.Contains(snapshot.Warnings[0].Message, "compressed active rollout ignored") || store.saveCalls != 0 {
		t.Fatalf("warnings/save = %#v/%d", snapshot.Warnings, store.saveCalls)
	}
}

func TestCollectActivitiesDropsRowsOutsideVisibilityWindow(t *testing.T) {
	coordinator := &Coordinator{
		activityCollector: &fakeActivityCollector{activities: []core.AgentActivity{
			{ThreadID: "fresh", Tool: core.ToolCodexDesktop, UpdatedAt: cycleOne.Add(-15 * time.Minute)},
			{ThreadID: "expired", Tool: core.ToolCodexDesktop, UpdatedAt: cycleOne.Add(-15*time.Minute - time.Nanosecond)},
			{ThreadID: "undated", Tool: core.ToolCodexDesktop},
		}},
		activityMatches: make(map[string]activityMatch),
		observed:        []core.Session{{ID: "desktop", Tool: core.ToolCodexDesktop, State: core.StateRunning}},
	}
	coordinator.collectActivities(cycleOne)
	if got := coordinator.observed[0].Activities; len(got) != 1 || got[0].ThreadID != "fresh" {
		t.Fatalf("visible activities = %#v, want only boundary-fresh row", got)
	}
}

func TestAttachActivitiesCorrelatesCLIByDirectWorkspaceAndTimeOneToOne(t *testing.T) {
	threadDirect := "019f8390-4743-72a1-9df8-d780c4964fce"
	coordinator := &Coordinator{
		activityMatches: make(map[string]activityMatch),
		supervised: []core.Session{
			{ID: "direct", Tool: core.ToolCodexCLI, State: core.StateRunning, Root: core.ProcessIdentity{PID: 1, CreatedAt: baseTime}, StartedAt: baseTime, ActivityMatchHint: core.ActivityMatchHint{ThreadID: threadDirect}},
			{ID: "workspace", Tool: core.ToolCodexCLI, State: core.StateRunning, Root: core.ProcessIdentity{PID: 2, CreatedAt: baseTime}, StartedAt: baseTime.Add(time.Minute), Workspace: `C:\repo`},
		},
		observed: []core.Session{
			{ID: "timed", Tool: core.ToolCodexCLI, State: core.StateRunning, Root: core.ProcessIdentity{PID: 3, CreatedAt: baseTime}, StartedAt: baseTime.Add(2 * time.Minute)},
		},
	}
	activities := []core.AgentActivity{
		{ThreadID: threadDirect, Tool: core.ToolCodexCLI, Workspace: `C:\other`, StartedAt: baseTime.Add(30 * time.Second), UpdatedAt: cycleOne},
		{ThreadID: "workspace-thread", Tool: core.ToolCodexCLI, Workspace: `c:\REPO`, StartedAt: baseTime.Add(time.Minute), UpdatedAt: cycleOne},
		{ThreadID: "timed-thread", Tool: core.ToolCodexCLI, Workspace: `C:\timed`, StartedAt: baseTime.Add(2*time.Minute + 30*time.Second), UpdatedAt: cycleOne},
	}

	coordinator.attachActivities(activities)

	if got := coordinator.supervised[0].Activities; len(got) != 1 || got[0].ThreadID != threadDirect || got[0].Correlation != core.CorrelationDirect {
		t.Fatalf("direct activities = %#v", got)
	}
	if got := coordinator.supervised[1].Activities; len(got) != 1 || got[0].ThreadID != "workspace-thread" || got[0].Correlation != core.CorrelationInferred {
		t.Fatalf("workspace activities = %#v", got)
	}
	if got := coordinator.observed[0].Activities; len(got) != 1 || got[0].ThreadID != "timed-thread" || got[0].Correlation != core.CorrelationInferred {
		t.Fatalf("timed activities = %#v", got)
	}
}

func TestAttachActivitiesUsesStickyWorkspaceForLaterThreadAndRejectsDistantStart(t *testing.T) {
	root := core.ProcessIdentity{PID: 3, CreatedAt: baseTime}
	coordinator := &Coordinator{
		activityMatches: make(map[string]activityMatch),
		observed:        []core.Session{{ID: "cli", Tool: core.ToolCodexCLI, State: core.StateRunning, Root: root, StartedAt: baseTime}},
	}
	coordinator.attachActivities([]core.AgentActivity{{ThreadID: "first", Tool: core.ToolCodexCLI, Workspace: `C:\repo`, StartedAt: baseTime.Add(30 * time.Second), UpdatedAt: cycleOne}})
	if got := coordinator.observed[0].Activities; len(got) != 1 || got[0].ThreadID != "first" {
		t.Fatalf("first match = %#v", got)
	}
	coordinator.attachActivities([]core.AgentActivity{{ThreadID: "later", Tool: core.ToolCodexCLI, Workspace: `c:\REPO`, StartedAt: baseTime.Add(time.Hour), UpdatedAt: cycleTwo}})
	if got := coordinator.observed[0].Activities; len(got) != 1 || got[0].ThreadID != "later" || got[0].Correlation != core.CorrelationInferred {
		t.Fatalf("sticky workspace match = %#v", got)
	}

	other := &Coordinator{
		activityMatches: make(map[string]activityMatch),
		observed:        []core.Session{{ID: "distant", Tool: core.ToolCodexCLI, State: core.StateRunning, Root: core.ProcessIdentity{PID: 4, CreatedAt: baseTime}, StartedAt: baseTime}},
	}
	other.attachActivities([]core.AgentActivity{{ThreadID: "too-late", Tool: core.ToolCodexCLI, StartedAt: baseTime.Add(61 * time.Second), UpdatedAt: cycleOne}})
	if len(other.observed[0].Activities) != 0 {
		t.Fatalf("distant activity matched = %#v", other.observed[0].Activities)
	}
}

func TestAttachActivitiesDoesNotGuessBetweenDesktopRoots(t *testing.T) {
	coordinator := &Coordinator{observed: []core.Session{
		{ID: "one", Tool: core.ToolCodexDesktop, State: core.StateRunning},
		{ID: "two", Tool: core.ToolCodexDesktop, State: core.StateRunning},
	}}
	coordinator.attachActivities([]core.AgentActivity{{ThreadID: "desktop", Tool: core.ToolCodexDesktop}})
	if len(coordinator.observed[0].Activities) != 0 || len(coordinator.observed[1].Activities) != 0 {
		t.Fatalf("ambiguous Desktop activity attached = %#v", coordinator.observed)
	}
}

func TestStartPassesOnlyValidSelfIdentityToObserver(t *testing.T) {
	tests := []struct {
		name string
		self core.ProcessIdentity
		want map[uint32]core.ProcessIdentity
	}{
		{name: "valid", self: core.ProcessIdentity{PID: 42, CreatedAt: baseTime}, want: map[uint32]core.ProcessIdentity{42: {PID: 42, CreatedAt: baseTime}}},
		{name: "zero", self: core.ProcessIdentity{}, want: map[uint32]core.ProcessIdentity{}},
		{name: "missing creation time", self: core.ProcessIdentity{PID: 42}, want: map[uint32]core.ProcessIdentity{}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observer := &fakeObserver{results: []observation{{}}}
			coordinator := NewCoordinator("workspace", &fakeStore{}, &fakeSupervisor{}, observer, test.self)

			if _, err := coordinator.Start(); err != nil {
				t.Fatalf("Start() error = %v", err)
			}
			if len(observer.calls) != 1 || !sameIdentities(observer.calls[0], test.want) {
				t.Fatalf("Observe exclusions = %#v, want %#v", observer.calls, test.want)
			}
		})
	}
}

func TestExclusionsRetainsSelfIdentityWhenSupervisedPIDCollides(t *testing.T) {
	self := core.ProcessIdentity{PID: 42, CreatedAt: baseTime}
	coordinator := &Coordinator{
		selfIdentity: self,
		supervised: []core.Session{{
			Processes: []core.Process{{PID: self.PID, CreatedAt: baseTime.Add(-time.Minute)}},
		}},
	}

	if got := coordinator.exclusions()[self.PID]; got != self {
		t.Fatalf("self exclusion = %#v, want %#v", got, self)
	}
}

func TestStartMissingFileObservesWithoutCreatingStateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "state.json")
	supervisor := &fakeSupervisor{}
	observed := session("desktop", core.ConfidenceObserved, core.StateRunning, baseTime)
	observer := &fakeObserver{results: []observation{{sessions: []core.Session{observed}}}}
	coordinator := newCoordinator("workspace", NewFileStore(path), supervisor, observer, func() time.Time { return cycleOne })

	snapshot, err := coordinator.Start()

	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state path stat error = %v, want missing file", err)
	}
	if len(observer.calls) != 1 || len(supervisor.recoverCalls) != 0 {
		t.Fatalf("dependency calls = observe %d, recover %d; want 1, 0", len(observer.calls), len(supervisor.recoverCalls))
	}
	assertSnapshot(t, snapshot, "workspace", []string{"desktop"}, nil, cycleOne)
}

func TestStartMixedRecoveryPreservesAlignmentExcludesProcessesAndPrunesExited(t *testing.T) {
	loadedRunning := session("running", core.ConfidenceSupervised, "", baseTime.Add(4*time.Minute), process(10, baseTime))
	loadedRunning.JobName = "job-running"
	loadedLeftover := session("leftover", core.ConfidenceSupervised, "", baseTime.Add(2*time.Minute))
	loadedLeftover.JobName = "job-leftover"
	loadedExited := session("exited", core.ConfidenceSupervised, "", baseTime.Add(3*time.Minute))
	loadedExited.JobName = "job-exited"
	loadedError := session("error", core.ConfidenceSupervised, core.StateRunning, baseTime, process(40, baseTime.Add(40*time.Second)))
	loadedError.JobName = "job-error"
	loadedError.Resources.MemoryBytes = 1234

	recoveredRunning := loadedRunning
	recoveredRunning.Processes = []core.Process{process(11, baseTime.Add(11*time.Second))}
	recoveredRunning.State = core.StateRunning
	recoveredLeftover := loadedLeftover
	recoveredLeftover.Processes = []core.Process{process(22, baseTime.Add(22*time.Second))}
	recoveredLeftover.State = core.StateLeftovers
	recoveredExited := loadedExited
	recoveredExited.State = core.StateExited

	store := &fakeStore{sessions: []core.Session{loadedRunning, loadedLeftover, loadedExited, loadedError}}
	supervisor := &fakeSupervisor{
		recovered:   []core.Session{recoveredRunning, recoveredLeftover, recoveredExited, {}},
		recoverErrs: []error{nil, nil, nil, errors.New("access denied")},
	}
	observed := session("desktop", core.ConfidenceObserved, core.StateRunning, baseTime, process(99, baseTime))
	observer := &fakeObserver{results: []observation{{sessions: []core.Session{observed}}}}
	coordinator := newCoordinator("workspace", store, supervisor, observer, func() time.Time { return cycleOne })

	snapshot, err := coordinator.Start()

	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if len(supervisor.recoverCalls) != 1 || ids(supervisor.recoverCalls[0])[0] != "running" || len(supervisor.recoverCalls[0]) != 4 {
		t.Fatalf("RecoverMany calls = %#v, want all loaded rows once in input order", supervisor.recoverCalls)
	}
	wantExcluded := map[uint32]core.ProcessIdentity{
		11: recoveredRunning.Processes[0].Identity(),
		22: recoveredLeftover.Processes[0].Identity(),
		40: loadedError.Processes[0].Identity(),
	}
	if len(observer.calls) != 1 || !sameIdentities(observer.calls[0], wantExcluded) {
		t.Fatalf("Observe exclusions = %#v, want %#v", observer.calls, wantExcluded)
	}
	assertSnapshot(t, snapshot, "workspace", []string{"desktop", "error", "leftover", "exited", "running"}, []Warning{{SessionID: "error", Message: "recover session: access denied"}}, cycleOne)
	stale := findSession(t, snapshot.Sessions, "error")
	if stale.State != core.StateStale || stale.Resources.MemoryBytes != 1234 || stale.Processes[0].PID != 40 {
		t.Fatalf("failed recovery row = %#v, want last-known data with stale state", stale)
	}
	if store.saveCalls != 1 {
		t.Fatalf("Save calls = %d, want 1", store.saveCalls)
	}
	if got := ids(store.saved[0]); strings.Join(got, ",") != "running,leftover,error" {
		t.Fatalf("saved IDs = %v, want running,leftover,error", got)
	}
	for _, saved := range store.saved[0] {
		if saved.State == core.StateExited || saved.Confidence == core.ConfidenceObserved {
			t.Fatalf("saved invalid row = %#v", saved)
		}
	}
}

func TestStartObservationFailureIsNonfatalAndKeepsSupervisedRows(t *testing.T) {
	loaded := session("supervised", core.ConfidenceSupervised, core.StateRunning, baseTime)
	loaded.JobName = "job"
	store := &fakeStore{sessions: []core.Session{loaded}}
	supervisor := &fakeSupervisor{recovered: []core.Session{loaded}, recoverErrs: []error{nil}}
	observer := &fakeObserver{results: []observation{{err: errors.New("snapshot failed")}}}
	coordinator := newCoordinator("workspace", store, supervisor, observer, func() time.Time { return cycleOne })

	snapshot, err := coordinator.Start()

	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	assertSnapshot(t, snapshot, "workspace", []string{"supervised"}, []Warning{{Message: "observe Codex sessions: snapshot failed"}}, cycleOne)
}

func TestStartLoadFailureIsFatalAndPathBearing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	supervisor := &fakeSupervisor{}
	observer := &fakeObserver{}
	coordinator := newCoordinator("workspace", NewFileStore(path), supervisor, observer, func() time.Time { return cycleOne })

	_, err := coordinator.Start()

	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("Start() error = %v, want fatal error containing %q", err, path)
	}
	if len(supervisor.recoverCalls) != 0 || len(observer.calls) != 0 {
		t.Fatalf("dependency calls after load failure = recover %d, observe %d", len(supervisor.recoverCalls), len(observer.calls))
	}
}

func TestStartReconciliationSaveFailureIsFatalAndPathBearing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	loaded := session("gone", core.ConfidenceSupervised, "", baseTime)
	loaded.JobName = "job-gone"
	exited := loaded
	exited.State = core.StateExited
	store := &fakeStore{sessions: []core.Session{loaded}, saveErr: errors.New("save " + path + ": disk full")}
	supervisor := &fakeSupervisor{recovered: []core.Session{exited}, recoverErrs: []error{nil}}
	observer := &fakeObserver{results: []observation{{}}}
	coordinator := newCoordinator("workspace", store, supervisor, observer, func() time.Time { return cycleOne })

	_, err := coordinator.Start()

	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("Start() error = %v, want fatal error containing %q", err, path)
	}
	if store.saveCalls != 1 {
		t.Fatalf("Save calls = %d, want 1", store.saveCalls)
	}
}

func TestFileStoreWrapsLoadAndSaveErrorsWithExactPath(t *testing.T) {
	t.Run("load", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "broken-state.json")
		if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := NewFileStore(path).Load()
		if err == nil || !strings.Contains(err.Error(), path) {
			t.Fatalf("Load() error = %v, want path %q", err, path)
		}
	})
	t.Run("save", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "state-as-directory")
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		err := NewFileStore(path).Save([]core.Session{session("saved", core.ConfidenceSupervised, core.StateRunning, baseTime)})
		if err == nil || !strings.Contains(err.Error(), path) {
			t.Fatalf("Save() error = %v, want path %q", err, path)
		}
	})
}

func TestRefreshPartialFailurePreservesLastKnownAndInaccessibleSuccessWarns(t *testing.T) {
	one := session("one", core.ConfidenceSupervised, core.StateRunning, baseTime, process(1, baseTime))
	one.JobName = "job-one"
	two := session("two", core.ConfidenceSupervised, core.StateRunning, baseTime.Add(time.Minute), process(2, baseTime))
	two.JobName = "job-two"
	two.Resources.MemoryBytes = 222
	refreshedOne := one
	refreshedOne.State = core.StateInaccessible
	refreshedOne.Resources.MemoryBytes = 111
	store := &fakeStore{sessions: []core.Session{one, two}}
	supervisor := &fakeSupervisor{
		recovered:   []core.Session{one, two},
		recoverErrs: []error{nil, nil},
		refreshed:   [][]core.Session{{refreshedOne, {}}},
		refreshErrs: [][]error{{nil, errors.New("query failed")}},
	}
	observer := &fakeObserver{results: []observation{{}, {}}}
	times := clockSequence(cycleOne, cycleTwo)
	coordinator := newCoordinator("workspace", store, supervisor, observer, times)
	if _, err := coordinator.Start(); err != nil {
		t.Fatal(err)
	}

	snapshot := coordinator.Refresh()

	if len(supervisor.refreshCalls) != 1 || strings.Join(ids(supervisor.refreshCalls[0]), ",") != "one,two" {
		t.Fatalf("RefreshMany calls = %#v, want one call with non-exited rows", supervisor.refreshCalls)
	}
	gotOne := findSession(t, snapshot.Sessions, "one")
	if gotOne.State != core.StateInaccessible || gotOne.Resources.MemoryBytes != 111 {
		t.Fatalf("inaccessible result = %#v", gotOne)
	}
	gotTwo := findSession(t, snapshot.Sessions, "two")
	if gotTwo.State != core.StateStale || gotTwo.Resources.MemoryBytes != 222 || gotTwo.Processes[0].PID != 2 {
		t.Fatalf("failed refresh result = %#v, want preserved last-known row marked stale", gotTwo)
	}
	wantWarnings := []Warning{
		{SessionID: "one", Message: "session inaccessible"},
		{SessionID: "two", Message: "refresh session: query failed"},
	}
	assertWarnings(t, snapshot.Warnings, wantWarnings)
	if store.saveCalls != 0 {
		t.Fatalf("Save calls = %d, want 0 without newly exited rows", store.saveCalls)
	}
}

func TestRefreshObservationSuccessReplacesAndFailureRetainsStaleRows(t *testing.T) {
	supervised := session("supervised", core.ConfidenceSupervised, core.StateRunning, baseTime, process(1, baseTime))
	supervised.JobName = "job"
	oldObserved := session("old", core.ConfidenceObserved, core.StateRunning, baseTime.Add(time.Minute), process(2, baseTime))
	newObserved := session("new", core.ConfidenceObserved, core.StateRunning, baseTime.Add(2*time.Minute), process(3, baseTime))
	store := &fakeStore{sessions: []core.Session{supervised}}
	supervisor := &fakeSupervisor{
		recovered:   []core.Session{supervised},
		recoverErrs: []error{nil},
		refreshed:   [][]core.Session{{supervised}, {supervised}},
		refreshErrs: [][]error{{nil}, {nil}},
	}
	observer := &fakeObserver{results: []observation{
		{sessions: []core.Session{oldObserved}},
		{sessions: []core.Session{newObserved}},
		{err: errors.New("desktop snapshot unavailable")},
	}}
	coordinator := newCoordinator("workspace", store, supervisor, observer, clockSequence(cycleOne, cycleTwo, cycleTwo.Add(time.Minute)))
	if _, err := coordinator.Start(); err != nil {
		t.Fatal(err)
	}

	replaced := coordinator.Refresh()
	if findOptional(replaced.Sessions, "old") != nil || findOptional(replaced.Sessions, "new") == nil {
		t.Fatalf("observation success rows = %v, want new replacing old", ids(replaced.Sessions))
	}
	retained := coordinator.Refresh()
	gotNew := findSession(t, retained.Sessions, "new")
	if gotNew.State != core.StateStale {
		t.Fatalf("retained observed state = %q, want stale", gotNew.State)
	}
	assertWarnings(t, retained.Warnings, []Warning{{Message: "observe Codex sessions: desktop snapshot unavailable"}})
}

func TestRefreshNewlyExitedSavesOnlyNonExitedAndSaveFailureWarns(t *testing.T) {
	active := session("active", core.ConfidenceSupervised, core.StateRunning, baseTime)
	active.JobName = "job-active"
	staying := session("staying", core.ConfidenceSupervised, core.StateRunning, baseTime.Add(time.Minute))
	staying.JobName = "job-staying"
	exited := active
	exited.State = core.StateExited
	store := &fakeStore{sessions: []core.Session{active, staying}, saveErr: errors.New("disk full")}
	supervisor := &fakeSupervisor{
		recovered:   []core.Session{active, staying},
		recoverErrs: []error{nil, nil},
		refreshed:   [][]core.Session{{exited, staying}, {staying}},
		refreshErrs: [][]error{{nil, nil}, {nil}},
	}
	observer := &fakeObserver{results: []observation{{}, {}, {}}}
	coordinator := newCoordinator("workspace", store, supervisor, observer, clockSequence(cycleOne, cycleTwo, cycleTwo.Add(time.Minute)))
	if _, err := coordinator.Start(); err != nil {
		t.Fatal(err)
	}

	snapshot := coordinator.Refresh()

	if store.saveCalls != 1 || strings.Join(ids(store.saved[0]), ",") != "staying" {
		t.Fatalf("saved rows = %#v, want only staying in one call", store.saved)
	}
	if findSession(t, snapshot.Sessions, "active").State != core.StateExited {
		t.Fatalf("newly exited row not retained in snapshot: %#v", snapshot.Sessions)
	}
	assertWarnings(t, snapshot.Warnings, []Warning{{Message: "save reconciled sessions: disk full", kind: warningPersistence}})
	if !coordinator.persistenceDirty {
		t.Fatal("persistenceDirty = false, want true after reconciliation save failure")
	}
	if snapshot.RefreshedAt != cycleTwo {
		t.Fatalf("RefreshedAt = %v, want %v", snapshot.RefreshedAt, cycleTwo)
	}

	next := coordinator.Refresh()
	if strings.Join(ids(supervisor.refreshCalls[1]), ",") != "staying" {
		t.Fatalf("second RefreshMany input = %v, want only staying", ids(supervisor.refreshCalls[1]))
	}
	if findSession(t, next.Sessions, "active").State != core.StateExited {
		t.Fatalf("exited row changed on later refresh: %#v", next.Sessions)
	}
	if store.saveCalls != 2 || strings.Join(ids(store.saved[1]), ",") != "staying" {
		t.Fatalf("retry saved rows = %#v, want staying in second call", store.saved)
	}
	if !coordinator.persistenceDirty {
		t.Fatal("persistenceDirty = false, want preserved after retry failure")
	}
	assertWarnings(t, next.Warnings, []Warning{{Message: "save reconciled sessions: disk full", kind: warningPersistence}})
}

func TestLaunchClearsStalePersistenceWarningAfterRefreshSaveFailure(t *testing.T) {
	active := session("active", core.ConfidenceSupervised, core.StateRunning, baseTime)
	staying := session("staying", core.ConfidenceSupervised, core.StateRunning, baseTime.Add(time.Minute))
	exited := active
	exited.State = core.StateExited
	launched := session("launched", core.ConfidenceSupervised, core.StateRunning, baseTime.Add(2*time.Minute))
	store := &fakeStore{
		sessions: []core.Session{active, staying},
		saveErrs: []error{
			errors.New("disk full"), // Refresh's reconciliation save
			nil,                     // Launch's save
		},
	}
	supervisor := &fakeSupervisor{
		recovered:   []core.Session{active, staying},
		recoverErrs: []error{nil, nil},
		refreshed:   [][]core.Session{{exited, staying}},
		refreshErrs: [][]error{{nil, nil}},
		launched:    launched,
	}
	observer := &fakeObserver{results: []observation{{}, {}}}
	coordinator := newCoordinator("workspace", store, supervisor, observer, clockSequence(cycleOne, cycleTwo))
	coordinator.resolveWorkspace = passthroughResolver
	if _, err := coordinator.Start(); err != nil {
		t.Fatal(err)
	}

	snapshot := coordinator.Refresh()
	if store.saveCalls != 1 || strings.Join(ids(store.saved[0]), ",") != "staying" {
		t.Fatalf("saved rows = %#v, want only staying in one call", store.saved)
	}
	assertWarnings(t, snapshot.Warnings, []Warning{{Message: "save reconciled sessions: disk full", kind: warningPersistence}})
	if !coordinator.persistenceDirty {
		t.Fatal("persistenceDirty = false, want true after reconciliation save failure")
	}

	result, err := coordinator.Launch(core.ToolCodexCLI, "workspace")
	if err != nil {
		t.Fatalf("Launch() error = %v, want nil after successful save", err)
	}
	if coordinator.persistenceDirty {
		t.Fatal("persistenceDirty = true, want false after successful launch save")
	}
	assertWarnings(t, result.Snapshot.Warnings, nil)
	if store.saveCalls != 2 || strings.Join(ids(store.saved[1]), ",") != "staying,launched" {
		t.Fatalf("launch save = %#v, want complete staying+launched set", store.saved)
	}
}

func TestLaunchReplacesStalePersistenceWarningWhenItsOwnSaveAlsoFails(t *testing.T) {
	active := session("active", core.ConfidenceSupervised, core.StateRunning, baseTime)
	staying := session("staying", core.ConfidenceSupervised, core.StateRunning, baseTime.Add(time.Minute))
	exited := active
	exited.State = core.StateExited
	launched := session("launched", core.ConfidenceSupervised, core.StateRunning, baseTime.Add(2*time.Minute))
	launchSaveErr := errors.New("still full")
	store := &fakeStore{
		sessions: []core.Session{active, staying},
		saveErrs: []error{
			errors.New("disk full"), // Refresh's reconciliation save
			launchSaveErr,           // Launch's save
		},
	}
	supervisor := &fakeSupervisor{
		recovered:   []core.Session{active, staying},
		recoverErrs: []error{nil, nil},
		refreshed:   [][]core.Session{{exited, staying}},
		refreshErrs: [][]error{{nil, nil}},
		launched:    launched,
	}
	observer := &fakeObserver{results: []observation{{}, {}}}
	coordinator := newCoordinator("workspace", store, supervisor, observer, clockSequence(cycleOne, cycleTwo))
	coordinator.resolveWorkspace = passthroughResolver
	if _, err := coordinator.Start(); err != nil {
		t.Fatal(err)
	}
	if snapshot := coordinator.Refresh(); !coordinator.persistenceDirty {
		t.Fatalf("persistenceDirty = false, want true after reconciliation save failure; warnings = %#v", snapshot.Warnings)
	}

	result, err := coordinator.Launch(core.ToolCodexCLI, "workspace")
	if !errors.Is(err, launchSaveErr) {
		t.Fatalf("Launch() error = %v, want wrapped %v", err, launchSaveErr)
	}
	if !coordinator.persistenceDirty {
		t.Fatal("persistenceDirty = false, want true after launch save failure")
	}
	if len(result.Snapshot.Warnings) != 1 || result.Snapshot.Warnings[0].SessionID != "launched" || !strings.Contains(result.Snapshot.Warnings[0].Message, "still full") {
		t.Fatalf("warnings = %#v, want single launch-save warning replacing the stale reconciliation warning", result.Snapshot.Warnings)
	}
}

func TestRefreshDirtyPersistenceRetriesWithoutNewExitAndClearsOnSuccess(t *testing.T) {
	workspace := t.TempDir()
	launched := session("launched", core.ConfidenceSupervised, core.StateRunning, baseTime)
	firstSaveErr := errors.New("first save failed")
	store := &fakeStore{saveErrs: []error{firstSaveErr, nil}}
	supervisor := &fakeSupervisor{
		launched:    launched,
		refreshed:   [][]core.Session{{launched}},
		refreshErrs: [][]error{{nil}},
	}
	observer := &fakeObserver{results: []observation{{}}}
	coordinator := newCoordinator(workspace, store, supervisor, observer, func() time.Time { return cycleTwo })
	coordinator.refreshed = cycleOne
	if _, err := coordinator.Launch(core.ToolCodexCLI, workspace); !errors.Is(err, firstSaveErr) {
		t.Fatalf("Launch() error = %v, want first save failure", err)
	}

	got := coordinator.Refresh()

	if store.saveCalls != 2 || strings.Join(ids(store.saved[1]), ",") != "launched" {
		t.Fatalf("refresh retry saves = %#v, want complete launched set on second call", store.saved)
	}
	if coordinator.persistenceDirty {
		t.Fatal("persistenceDirty = true, want cleared after successful refresh retry")
	}
	assertSnapshot(t, got, workspace, []string{"launched"}, nil, cycleTwo)
}

func TestRefreshDirtyPersistenceFailedRetryPreservesDirtyAndWarns(t *testing.T) {
	workspace := t.TempDir()
	launched := session("launched", core.ConfidenceSupervised, core.StateRunning, baseTime)
	firstSaveErr := errors.New("first save failed")
	retryErr := errors.New("retry still full")
	store := &fakeStore{saveErrs: []error{firstSaveErr, retryErr}}
	supervisor := &fakeSupervisor{
		launched:    launched,
		refreshed:   [][]core.Session{{launched}},
		refreshErrs: [][]error{{nil}},
	}
	observer := &fakeObserver{results: []observation{{}}}
	coordinator := newCoordinator(workspace, store, supervisor, observer, func() time.Time { return cycleTwo })
	if _, err := coordinator.Launch(core.ToolCodexCLI, workspace); !errors.Is(err, firstSaveErr) {
		t.Fatalf("Launch() error = %v, want first save failure", err)
	}

	got := coordinator.Refresh()

	if store.saveCalls != 2 || !coordinator.persistenceDirty {
		t.Fatalf("saveCalls/persistenceDirty = %d/%v, want 2/true", store.saveCalls, coordinator.persistenceDirty)
	}
	assertWarnings(t, got.Warnings, []Warning{{Message: "save reconciled sessions: retry still full", kind: warningPersistence}})
}

func TestSnapshotsAreDeepCopies(t *testing.T) {
	supervised := session("supervised", core.ConfidenceSupervised, core.StateRunning, baseTime, process(1, baseTime))
	supervised.JobName = "job"
	observed := session("observed", core.ConfidenceObserved, core.StateRunning, baseTime.Add(time.Minute), process(2, baseTime))
	store := &fakeStore{sessions: []core.Session{supervised}}
	supervisor := &fakeSupervisor{
		recovered:   []core.Session{supervised},
		recoverErrs: []error{nil},
		refreshed:   [][]core.Session{{{}}, {{}}},
		refreshErrs: [][]error{{errors.New("supervisor down")}, {errors.New("supervisor down")}},
	}
	observer := &fakeObserver{results: []observation{
		{sessions: []core.Session{observed}},
		{err: errors.New("observer down")},
		{err: errors.New("observer down")},
	}}
	coordinator := newCoordinator("workspace", store, supervisor, observer, clockSequence(cycleOne, cycleTwo, cycleTwo.Add(time.Minute)))
	first, err := coordinator.Start()
	if err != nil {
		t.Fatal(err)
	}
	first.Sessions[0].ID = "mutated"
	first.Sessions[0].Processes[0].Name = "mutated"
	first.Sessions = nil

	second := coordinator.Refresh()
	if findSession(t, second.Sessions, "supervised").Processes[0].Name == "mutated" || findOptional(second.Sessions, "observed") == nil {
		t.Fatalf("first snapshot mutation leaked into state: %#v", second.Sessions)
	}
	second.Warnings[0].Message = "mutated warning"
	second.Sessions[0].Processes[0].Name = "mutated again"

	third := coordinator.Refresh()
	if findSession(t, third.Sessions, "supervised").Processes[0].Name == "mutated again" {
		t.Fatalf("second snapshot process mutation leaked into state: %#v", third.Sessions)
	}
	for _, warning := range third.Warnings {
		if warning.Message == "mutated warning" {
			t.Fatalf("warning mutation leaked into state: %#v", third.Warnings)
		}
	}
}

func TestLaunchTrimsAndResolvesRelativeWorkspaceAndKeepsExistingSessionWorkspaces(t *testing.T) {
	base := t.TempDir()
	wantWorkspace := filepath.Join(base, "nested", "project")
	if err := os.MkdirAll(wantWorkspace, 0o700); err != nil {
		t.Fatal(err)
	}
	managed := session("managed", core.ConfidenceSupervised, core.StateRunning, baseTime, process(1, baseTime))
	managed.Workspace = "original-managed-workspace"
	observed := session("observed", core.ConfidenceObserved, core.StateRunning, baseTime.Add(time.Minute), process(2, baseTime))
	observed.Workspace = "original-observed-workspace"
	launched := session("launched", core.ConfidenceSupervised, core.StateRunning, baseTime.Add(2*time.Minute), process(3, baseTime))
	store := &fakeStore{}
	supervisor := &fakeSupervisor{launched: launched}
	coordinator := newCoordinator(base, store, supervisor, &fakeObserver{}, func() time.Time { return cycleOne })
	coordinator.supervised = []core.Session{managed}
	coordinator.observed = []core.Session{observed}
	coordinator.refreshed = cycleOne

	got, err := coordinator.Launch(core.ToolClaudeCLI, "  nested/project  ")

	if err != nil {
		t.Fatalf("Launch() error = %v", err)
	}
	wantResolved := filepath.Clean(wantWorkspace)
	if len(supervisor.launchCalls) != 1 || supervisor.launchCalls[0] != (launchCall{tool: core.ToolClaudeCLI, workspace: wantResolved}) {
		t.Fatalf("Supervisor.Launch calls = %#v, want resolved workspace %q", supervisor.launchCalls, wantResolved)
	}
	if len(got.Snapshot.RecentWorkspaces) != 1 || got.Snapshot.RecentWorkspaces[0] != wantResolved {
		t.Fatalf("recent workspaces = %#v, want [%q]", got.Snapshot.RecentWorkspaces, wantResolved)
	}
	if findSession(t, got.Snapshot.Sessions, "managed").Workspace != "original-managed-workspace" || findSession(t, got.Snapshot.Sessions, "observed").Workspace != "original-observed-workspace" {
		t.Fatalf("Launch() rewrote existing session workspaces: %#v", got.Snapshot.Sessions)
	}
}

func TestLaunchUsesInjectableResolverWithTrimmedInput(t *testing.T) {
	launched := session("launched", core.ConfidenceSupervised, core.StateRunning, baseTime, process(1, baseTime))
	coordinator := newCoordinator("old-workspace", &fakeStore{}, &fakeSupervisor{launched: launched}, &fakeObserver{}, func() time.Time { return cycleOne })
	var gotBase, gotInput string
	coordinator.resolveWorkspace = func(base, input string) (string, error) {
		gotBase, gotInput = base, input
		return "resolved-workspace", nil
	}

	got, err := coordinator.Launch(core.ToolCodexCLI, "  child  ")

	if err != nil {
		t.Fatalf("Launch() error = %v", err)
	}
	if gotBase != "old-workspace" || gotInput != "child" {
		t.Fatalf("resolver inputs = (%q, %q), want (%q, %q)", gotBase, gotInput, "old-workspace", "child")
	}
	if len(got.Snapshot.RecentWorkspaces) != 1 || got.Snapshot.RecentWorkspaces[0] != "resolved-workspace" {
		t.Fatalf("recent workspaces = %#v, want [resolved-workspace]", got.Snapshot.RecentWorkspaces)
	}
}

func TestLaunchRejectsInvalidWorkspaceWithoutChangingState(t *testing.T) {
	base := t.TempDir()
	filePath := filepath.Join(base, "not-a-directory")
	if err := os.WriteFile(filePath, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		input string
	}{
		{name: "empty", input: "  \t  "},
		{name: "missing", input: "missing"},
		{name: "file", input: filePath},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			managed := session("managed", core.ConfidenceSupervised, core.StateRunning, baseTime, process(1, baseTime))
			store := &fakeStore{}
			supervisor := &fakeSupervisor{launched: session("unexpected", core.ConfidenceSupervised, core.StateRunning, baseTime)}
			coordinator := newCoordinator(base, store, supervisor, &fakeObserver{}, func() time.Time { return cycleOne })
			coordinator.supervised = []core.Session{managed}
			coordinator.warnings = []Warning{{Message: "existing warning"}}
			coordinator.refreshed = cycleOne
			before := coordinator.snapshot()

			got, err := coordinator.Launch(core.ToolCodexCLI, test.input)

			if err == nil {
				t.Fatalf("Launch(%q) error = nil, want invalid-workspace error", test.input)
			}
			if got.SessionID != "" || !reflect.DeepEqual(got.Snapshot, before) || !reflect.DeepEqual(coordinator.snapshot(), before) {
				t.Fatalf("state after Launch(%q) = %#v / %#v, want unchanged %#v", test.input, got, coordinator.snapshot(), before)
			}
			if len(supervisor.launchCalls) != 0 || store.saveCalls != 0 || store.recentSaveCalls != 0 {
				t.Fatalf("dependency calls after invalid Launch(%q) = launch %d, save %d, recent save %d; want zero", test.input, len(supervisor.launchCalls), store.saveCalls, store.recentSaveCalls)
			}
			got.Snapshot.Sessions[0].Processes[0].Name = "mutated"
			if coordinator.snapshot().Sessions[0].Processes[0].Name == "mutated" {
				t.Fatal("invalid Launch() returned mutable coordinator state")
			}
		})
	}
}

func TestLaunchForwardsChosenWorkspacePersistsSupervisedSessionsAndReturnsID(t *testing.T) {
	for _, tool := range []core.Tool{core.ToolClaudeCLI, core.ToolCodexCLI} {
		t.Run(string(tool), func(t *testing.T) {
			initialWorkspace := t.TempDir()
			workspace := t.TempDir()
			running := session("running", core.ConfidenceSupervised, core.StateRunning, baseTime, process(1, baseTime))
			running.Workspace = "original-running-workspace"
			exited := session("exited", core.ConfidenceSupervised, core.StateExited, baseTime.Add(time.Minute))
			exited.Workspace = "original-exited-workspace"
			observed := session("observed", core.ConfidenceObserved, core.StateRunning, baseTime.Add(2*time.Minute))
			launched := session("launched", core.ConfidenceSupervised, core.StateRunning, baseTime.Add(3*time.Minute), process(2, baseTime))
			launched.Tool = tool
			launched.Workspace = workspace
			store := &fakeStore{}
			supervisor := &fakeSupervisor{launched: launched}
			coordinator := newCoordinator(initialWorkspace, store, supervisor, &fakeObserver{}, func() time.Time {
				t.Fatal("Launch called the coordinator clock")
				return time.Time{}
			})
			coordinator.supervised = []core.Session{running, exited}
			coordinator.observed = []core.Session{observed}
			coordinator.refreshed = cycleOne

			got, err := coordinator.Launch(tool, workspace)

			if err != nil {
				t.Fatalf("Launch() error = %v", err)
			}
			if len(supervisor.launchCalls) != 1 || supervisor.launchCalls[0] != (launchCall{tool: tool, workspace: workspace}) {
				t.Fatalf("Supervisor.Launch calls = %#v, want tool %q workspace %q", supervisor.launchCalls, tool, workspace)
			}
			if got.SessionID != "launched" {
				t.Fatalf("LaunchResult.SessionID = %q, want launched", got.SessionID)
			}
			// The current directory is unchanged by a launch; only the recent list grows.
			assertSnapshot(t, got.Snapshot, initialWorkspace, []string{"running", "exited", "observed", "launched"}, nil, cycleOne)
			if len(got.Snapshot.RecentWorkspaces) != 1 || got.Snapshot.RecentWorkspaces[0] != workspace {
				t.Fatalf("recent workspaces = %#v, want [%q]", got.Snapshot.RecentWorkspaces, workspace)
			}
			if store.recentSaveCalls != 1 || len(store.recentSaved) != 1 || strings.Join(store.recentSaved[0], ",") != workspace {
				t.Fatalf("recent saves = %#v, want one save of [%q]", store.recentSaved, workspace)
			}
			if findSession(t, got.Snapshot.Sessions, "running").Workspace != "original-running-workspace" || findSession(t, got.Snapshot.Sessions, "exited").Workspace != "original-exited-workspace" {
				t.Fatalf("Launch() rewrote original workspaces: %#v", got.Snapshot.Sessions)
			}
			if store.saveCalls != 1 || strings.Join(ids(store.saved[0]), ",") != "running,launched" {
				t.Fatalf("saved sessions = %#v, want non-exited supervised IDs running,launched", store.saved)
			}
			got.Snapshot.Sessions[3].Processes[0].Name = "mutated"
			if findSession(t, coordinator.snapshot().Sessions, "launched").Processes[0].Name == "mutated" {
				t.Fatal("Launch() returned mutable coordinator state")
			}
		})
	}
}

func TestLaunchSupervisorFailureLeavesStateUnchangedAndDoesNotSave(t *testing.T) {
	launchErr := errors.New("executable missing")
	workspace := t.TempDir()
	store := &fakeStore{}
	supervisor := &fakeSupervisor{launchErr: launchErr}
	coordinator := newCoordinator(workspace, store, supervisor, &fakeObserver{}, func() time.Time { return cycleOne })
	coordinator.supervised = []core.Session{session("running", core.ConfidenceSupervised, core.StateRunning, baseTime, process(1, baseTime))}
	coordinator.warnings = []Warning{{Message: "existing warning"}}
	coordinator.refreshed = cycleOne
	before := coordinator.snapshot()

	got, err := coordinator.Launch(core.ToolCodexCLI, workspace)

	if !errors.Is(err, launchErr) {
		t.Fatalf("Launch() error = %v, want wrapped supervisor error", err)
	}
	if got.SessionID != "" || !reflect.DeepEqual(got.Snapshot, before) || !reflect.DeepEqual(coordinator.snapshot(), before) {
		t.Fatalf("Launch() result/state = %#v / %#v, want empty ID and unchanged %#v", got, coordinator.snapshot(), before)
	}
	if store.saveCalls != 0 {
		t.Fatalf("Save calls = %d, want 0", store.saveCalls)
	}
	got.Snapshot.Sessions[0].Processes[0].Name = "mutated"
	if coordinator.snapshot().Sessions[0].Processes[0].Name == "mutated" {
		t.Fatal("failed Launch() returned mutable coordinator state")
	}
}

func TestLaunchSaveFailureRetainsLiveSessionMarksDirtyAndWarns(t *testing.T) {
	saveErr := errors.New("disk full")
	workspace := t.TempDir()
	launched := session("launched", core.ConfidenceSupervised, core.StateRunning, baseTime, process(2, baseTime))
	launched.Workspace = workspace
	store := &fakeStore{saveErr: saveErr}
	supervisor := &fakeSupervisor{launched: launched}
	coordinator := newCoordinator(workspace, store, supervisor, &fakeObserver{}, func() time.Time { return cycleOne })
	coordinator.refreshed = cycleOne

	got, err := coordinator.Launch(core.ToolCodexCLI, workspace)

	if !errors.Is(err, saveErr) {
		t.Fatalf("Launch() error = %v, want wrapped save error", err)
	}
	if got.SessionID != "launched" || findOptional(got.Snapshot.Sessions, "launched") == nil || findOptional(coordinator.snapshot().Sessions, "launched") == nil {
		t.Fatalf("Launch() result/state = %#v / %#v, want retained live session", got, coordinator.snapshot())
	}
	if store.saveCalls != 1 || strings.Join(ids(store.saved[0]), ",") != "launched" {
		t.Fatalf("saved sessions = %#v, want launched", store.saved)
	}
	if !coordinator.persistenceDirty {
		t.Fatal("persistenceDirty = false, want true after save failure")
	}
	if len(got.Snapshot.Warnings) != 1 || got.Snapshot.Warnings[0].SessionID != "launched" || !strings.Contains(got.Snapshot.Warnings[0].Message, "disk full") {
		t.Fatalf("warnings = %#v, want session-specific save warning", got.Snapshot.Warnings)
	}
	if got.Snapshot.RefreshedAt != cycleOne {
		t.Fatalf("RefreshedAt = %v, want unchanged %v", got.Snapshot.RefreshedAt, cycleOne)
	}
}

func TestRepeatedLaunchSaveFailuresDoNotAccumulateDuplicateWarnings(t *testing.T) {
	workspace := t.TempDir()
	first := session("first", core.ConfidenceSupervised, core.StateRunning, baseTime)
	second := session("second", core.ConfidenceSupervised, core.StateRunning, baseTime.Add(time.Minute))
	store := &fakeStore{saveErr: errors.New("disk full")}
	supervisor := &fakeSupervisor{launched: first}
	coordinator := newCoordinator(workspace, store, supervisor, &fakeObserver{}, func() time.Time { return cycleOne })
	if _, err := coordinator.Launch(core.ToolCodexCLI, workspace); err == nil {
		t.Fatal("first Launch() error = nil, want save failure")
	}
	supervisor.launched = second

	got, err := coordinator.Launch(core.ToolClaudeCLI, workspace)

	if err == nil {
		t.Fatal("second Launch() error = nil, want save failure")
	}
	if len(got.Snapshot.Warnings) != 1 || got.Snapshot.Warnings[0].SessionID != "second" {
		t.Fatalf("warnings = %#v, want single warning for latest failed launch, not an accumulation", got.Snapshot.Warnings)
	}
}

func TestLaterSuccessfulLaunchSaveClearsDirtyAndPersistsCompleteSet(t *testing.T) {
	workspace := t.TempDir()
	first := session("first", core.ConfidenceSupervised, core.StateRunning, baseTime)
	second := session("second", core.ConfidenceSupervised, core.StateRunning, baseTime.Add(time.Minute))
	store := &fakeStore{saveErr: errors.New("disk full")}
	supervisor := &fakeSupervisor{launched: first}
	coordinator := newCoordinator(workspace, store, supervisor, &fakeObserver{}, func() time.Time { return cycleOne })
	if _, err := coordinator.Launch(core.ToolCodexCLI, workspace); err == nil {
		t.Fatal("first Launch() error = nil, want save failure")
	}
	store.saveErr = nil
	supervisor.launched = second

	got, err := coordinator.Launch(core.ToolClaudeCLI, workspace)

	if err != nil {
		t.Fatalf("second Launch() error = %v", err)
	}
	if coordinator.persistenceDirty {
		t.Fatal("persistenceDirty = true, want false after complete successful save")
	}
	if got.SessionID != "second" || store.saveCalls != 2 || strings.Join(ids(store.saved[1]), ",") != "first,second" {
		t.Fatalf("second result/saved = %#v / %#v, want second and complete first,second set", got, store.saved)
	}
	if len(got.Snapshot.Warnings) != 0 {
		t.Fatalf("warnings = %#v, want stale save-failure warning cleared by later successful launch", got.Snapshot.Warnings)
	}
}

func TestLaunchAfterCloseReturnsCopiedUnchangedSnapshotWithoutDependencies(t *testing.T) {
	store := &fakeStore{}
	supervisor := &fakeSupervisor{launched: session("unexpected", core.ConfidenceSupervised, core.StateRunning, baseTime)}
	coordinator := newCoordinator(t.TempDir(), store, supervisor, &fakeObserver{}, func() time.Time { return cycleOne })
	coordinator.supervised = []core.Session{session("managed", core.ConfidenceSupervised, core.StateRunning, baseTime, process(1, baseTime))}
	coordinator.refreshed = cycleOne
	before := coordinator.snapshot()
	coordinator.Close()

	got, err := coordinator.Launch(core.ToolCodexCLI, "ignored")

	if !errors.Is(err, errCoordinatorClosed) {
		t.Fatalf("Launch() error = %v, want coordinator-closed error", err)
	}
	if got.SessionID != "" || !reflect.DeepEqual(got.Snapshot, before) || !reflect.DeepEqual(coordinator.snapshot(), before) {
		t.Fatalf("Launch() result/state = %#v / %#v, want unchanged %#v", got, coordinator.snapshot(), before)
	}
	if len(supervisor.launchCalls) != 0 || store.saveCalls != 0 {
		t.Fatalf("dependency calls after Close() = launch %d, save %d; want zero", len(supervisor.launchCalls), store.saveCalls)
	}
	got.Snapshot.Sessions[0].Processes[0].Name = "mutated"
	if coordinator.snapshot().Sessions[0].Processes[0].Name == "mutated" {
		t.Fatal("closed Launch() returned mutable coordinator state")
	}
}

func TestLaunchRecordsRecentWorkspacesMostRecentFirstDedupedAndCapped(t *testing.T) {
	store := &fakeStore{}
	supervisor := &fakeSupervisor{launched: session("launched", core.ConfidenceSupervised, core.StateRunning, baseTime, process(1, baseTime))}
	coordinator := newCoordinator("base", store, supervisor, &fakeObserver{}, func() time.Time { return cycleOne })
	coordinator.resolveWorkspace = passthroughResolver

	for _, ws := range []string{"w0", "w1", "w2", "w3", "w4", "w5", "w6", "w7", "w8", "w9"} {
		if _, err := coordinator.Launch(core.ToolCodexCLI, ws); err != nil {
			t.Fatalf("Launch(%q) error = %v", ws, err)
		}
	}
	got := coordinator.snapshot().RecentWorkspaces
	if want := "w9,w8,w7,w6,w5,w4,w3,w2"; strings.Join(got, ",") != want {
		t.Fatalf("recent workspaces = %#v, want most-recent-first capped %q", got, want)
	}

	if _, err := coordinator.Launch(core.ToolCodexCLI, "w5"); err != nil {
		t.Fatalf("re-launch error = %v", err)
	}
	got = coordinator.snapshot().RecentWorkspaces
	if want := "w5,w9,w8,w7,w6,w4,w3,w2"; strings.Join(got, ",") != want {
		t.Fatalf("recent after re-launch = %#v, want %q (moved to front, no duplicate)", got, want)
	}
	last := store.recentSaved[len(store.recentSaved)-1]
	if strings.Join(last, ",") != "w5,w9,w8,w7,w6,w4,w3,w2" {
		t.Fatalf("persisted recents = %#v, want to match snapshot", last)
	}
}

func TestLaunchRecentWorkspaceSaveWarningIsReplacedAndClearedByLaterAttempts(t *testing.T) {
	store := &fakeStore{recentSaveErr: errors.New("disk full")}
	supervisor := &fakeSupervisor{launched: session("first", core.ConfidenceSupervised, core.StateRunning, baseTime)}
	coordinator := newCoordinator("base", store, supervisor, &fakeObserver{}, func() time.Time { return cycleOne })
	coordinator.resolveWorkspace = passthroughResolver

	first, err := coordinator.Launch(core.ToolCodexCLI, "one")
	if err != nil {
		t.Fatalf("first Launch() error = %v, want nil for non-fatal recent-workspace save failure", err)
	}
	if len(first.Snapshot.Warnings) != 1 || !strings.Contains(first.Snapshot.Warnings[0].Message, "save recent workspaces") {
		t.Fatalf("first warnings = %#v, want one recent-workspace save warning", first.Snapshot.Warnings)
	}

	supervisor.launched = session("second", core.ConfidenceSupervised, core.StateRunning, baseTime.Add(time.Minute))
	second, err := coordinator.Launch(core.ToolClaudeCLI, "two")
	if err != nil {
		t.Fatalf("second Launch() error = %v, want nil for non-fatal recent-workspace save failure", err)
	}
	if len(second.Snapshot.Warnings) != 1 || !strings.Contains(second.Snapshot.Warnings[0].Message, "save recent workspaces") {
		t.Fatalf("second warnings = %#v, want one replacement warning rather than an accumulation", second.Snapshot.Warnings)
	}

	store.recentSaveErr = nil
	supervisor.launched = session("third", core.ConfidenceSupervised, core.StateRunning, baseTime.Add(2*time.Minute))
	third, err := coordinator.Launch(core.ToolCodexCLI, "three")
	if err != nil {
		t.Fatalf("third Launch() error = %v", err)
	}
	if len(third.Snapshot.Warnings) != 0 {
		t.Fatalf("third warnings = %#v, want stale recent-workspace save warning cleared after success", third.Snapshot.Warnings)
	}
}

func TestStartLoadsRecentWorkspaces(t *testing.T) {
	store := &fakeStore{recent: []string{"C:/a", "C:/b"}}
	coordinator := newCoordinator("base", store, &fakeSupervisor{}, &fakeObserver{results: []observation{{}}}, func() time.Time { return cycleOne })

	got, err := coordinator.Start()
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if strings.Join(got.RecentWorkspaces, ",") != "C:/a,C:/b" {
		t.Fatalf("recent workspaces = %#v, want [C:/a C:/b]", got.RecentWorkspaces)
	}
	if store.recentLoadCalls != 1 {
		t.Fatalf("recent load calls = %d, want 1", store.recentLoadCalls)
	}
}

func TestStartRecentWorkspacesLoadFailureWarnsAndStaysEmpty(t *testing.T) {
	store := &fakeStore{recentLoadErr: errors.New("corrupt recents")}
	coordinator := newCoordinator("base", store, &fakeSupervisor{}, &fakeObserver{results: []observation{{}}}, func() time.Time { return cycleOne })

	got, err := coordinator.Start()
	if err != nil {
		t.Fatalf("Start() error = %v, want recents load failure to be non-fatal", err)
	}
	if len(got.RecentWorkspaces) != 0 {
		t.Fatalf("recent workspaces = %#v, want empty after load failure", got.RecentWorkspaces)
	}
	if len(got.Warnings) != 1 || !strings.Contains(got.Warnings[0].Message, "corrupt recents") {
		t.Fatalf("warnings = %#v, want single recents load warning", got.Warnings)
	}
}

func TestInspectReturnsCachedProcessTreeWithoutCallingDependencies(t *testing.T) {
	managed := session(
		"managed",
		core.ConfidenceSupervised,
		core.StateLeftovers,
		baseTime,
		core.Process{PID: 12, ParentPID: 11, Name: "grandchild.exe", CreatedAt: baseTime.Add(2 * time.Second)},
		core.Process{PID: 10, ParentPID: 999, Name: "leftover-root.exe", CreatedAt: baseTime},
		core.Process{PID: 11, ParentPID: 10, Name: "child.exe", CreatedAt: baseTime.Add(time.Second)},
	)
	observed := session("observed", core.ConfidenceObserved, core.StateRunning, baseTime, process(20, baseTime))
	store := &fakeStore{}
	supervisor := &fakeSupervisor{}
	observer := &fakeObserver{}
	coordinator := newCoordinator("workspace", store, supervisor, observer, func() time.Time {
		t.Fatal("Inspect called the coordinator clock")
		return time.Time{}
	})
	coordinator.supervised = []core.Session{managed}
	coordinator.observed = []core.Session{observed}

	got, err := coordinator.Inspect("managed")

	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if got.Session.ID != "managed" || got.Session.Processes[0].PID != 12 {
		t.Fatalf("Inspect() session = %#v, want cloned cached session", got.Session)
	}
	if len(got.Rows) != 3 || got.Rows[0].Process.PID != 10 || got.Rows[0].Depth != 0 || got.Rows[1].Process.PID != 11 || got.Rows[1].Depth != 1 || got.Rows[2].Process.PID != 12 || got.Rows[2].Depth != 2 {
		t.Fatalf("Inspect() rows = %#v, want deterministic preorder", got.Rows)
	}
	if len(got.CompactRows) != 3 || got.CompactRows[0].PID != 10 || got.CompactRows[0].Depth != 0 || got.CompactRows[1].PID != 11 || got.CompactRows[1].Depth != 1 || got.CompactRows[2].PID != 12 || got.CompactRows[2].Depth != 2 {
		t.Fatalf("Inspect() compact rows = %#v, want compact projection of cached forest", got.CompactRows)
	}
	if store.loadCalls != 0 || store.saveCalls != 0 || len(supervisor.recoverCalls) != 0 || len(supervisor.refreshCalls) != 0 || supervisor.closeCalls != 0 || len(observer.calls) != 0 {
		t.Fatalf("dependency calls after Inspect() = load %d, save %d, recover %d, refresh %d, close %d, observe %d; want all zero", store.loadCalls, store.saveCalls, len(supervisor.recoverCalls), len(supervisor.refreshCalls), supervisor.closeCalls, len(observer.calls))
	}

	got.Session.ID = "mutated"
	got.Session.Processes[0].Name = "mutated"
	got.Rows[0].Process.Name = "mutated"
	got.CompactRows[0].Label = "mutated"
	got.Rows = nil
	got.CompactRows = nil

	again, err := coordinator.Inspect("managed")
	if err != nil {
		t.Fatalf("second Inspect() error = %v", err)
	}
	if again.Session.ID != "managed" || again.Session.Processes[0].Name == "mutated" || len(again.Rows) != 3 || again.Rows[0].Process.Name == "mutated" || len(again.CompactRows) != 3 || again.CompactRows[0].Label == "mutated" {
		t.Fatalf("Inspect() caller mutation leaked into coordinator state: %#v", again)
	}
	observedTree, err := coordinator.Inspect("observed")
	if err != nil || observedTree.Session.ID != "observed" {
		t.Fatalf("Inspect() observed tree = %#v, %v", observedTree, err)
	}
}

func TestInspectReturnsSentinelForUnknownSession(t *testing.T) {
	coordinator := newCoordinator("workspace", &fakeStore{}, &fakeSupervisor{}, &fakeObserver{}, func() time.Time { return cycleOne })
	coordinator.supervised = []core.Session{session("known", core.ConfidenceSupervised, core.StateRunning, baseTime)}

	got, err := coordinator.Inspect("missing")

	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Inspect() error = %v, want ErrSessionNotFound", err)
	}
	if !reflect.DeepEqual(got, ProcessTree{}) {
		t.Fatalf("Inspect() tree = %#v, want zero tree", got)
	}
}

func TestInspectAfterCloseReturnsCoordinatorClosedErrorAndZeroTree(t *testing.T) {
	supervisor := &fakeSupervisor{}
	coordinator := newCoordinator("workspace", &fakeStore{}, supervisor, &fakeObserver{}, func() time.Time { return cycleOne })
	coordinator.supervised = []core.Session{session("known", core.ConfidenceSupervised, core.StateRunning, baseTime)}
	coordinator.Close()

	got, err := coordinator.Inspect("known")

	if !errors.Is(err, errCoordinatorClosed) {
		t.Fatalf("Inspect() error = %v, want coordinator-closed error", err)
	}
	if !reflect.DeepEqual(got, ProcessTree{}) {
		t.Fatalf("Inspect() tree = %#v, want zero tree", got)
	}
	if supervisor.closeCalls != 1 {
		t.Fatalf("Supervisor.Close() calls = %d after Inspect(), want unchanged 1", supervisor.closeCalls)
	}
}

func TestCloseDelegatesToSupervisor(t *testing.T) {
	supervisor := &fakeSupervisor{}
	coordinator := NewCoordinator("workspace", &fakeStore{}, supervisor, &fakeObserver{}, core.ProcessIdentity{})

	coordinator.Close()

	if supervisor.closeCalls != 1 {
		t.Fatalf("Close calls = %d, want 1", supervisor.closeCalls)
	}
}

func TestStartAfterCloseReturnsCopiedSnapshotWithoutAccessingDependencies(t *testing.T) {
	managed := session("managed", core.ConfidenceSupervised, core.StateRunning, baseTime, process(1, baseTime))
	managed.JobName = "job-managed"
	found := session("found", core.ConfidenceObserved, core.StateRunning, baseTime.Add(time.Minute), process(2, baseTime))
	store := &fakeStore{sessions: []core.Session{managed}}
	supervisor := &fakeSupervisor{recovered: []core.Session{managed}, recoverErrs: []error{nil}}
	observer := &fakeObserver{results: []observation{{sessions: []core.Session{found}}, {sessions: []core.Session{found}}}}
	coordinator := newCoordinator("workspace", store, supervisor, observer, func() time.Time { return cycleOne })
	if _, err := coordinator.Start(); err != nil {
		t.Fatal(err)
	}
	coordinator.Close()

	got, err := coordinator.Start()
	if err == nil || !strings.Contains(err.Error(), "closed") {
		t.Errorf("Start() error = %v, want closed error", err)
	}
	if store.loadCalls != 1 || len(supervisor.recoverCalls) != 1 || len(observer.calls) != 1 {
		t.Errorf("dependency calls after Close() = load %d, recover %d, observe %d; want unchanged 1, 1, 1", store.loadCalls, len(supervisor.recoverCalls), len(observer.calls))
	}
	assertSnapshot(t, got, "workspace", []string{"managed", "found"}, nil, cycleOne)

	got.Sessions[0].Processes[0].Name = "mutated"
	if coordinator.snapshot().Sessions[0].Processes[0].Name == "mutated" {
		t.Fatal("Start() after Close() exposed mutable coordinator state")
	}
}

func TestRefreshAfterCloseReturnsCopiedSnapshotWithoutAccessingDependencies(t *testing.T) {
	managed := session("managed", core.ConfidenceSupervised, core.StateRunning, baseTime, process(1, baseTime))
	managed.JobName = "job-managed"
	store := &fakeStore{sessions: []core.Session{managed}}
	supervisor := &fakeSupervisor{
		recovered:   []core.Session{managed},
		recoverErrs: []error{nil},
		refreshed:   [][]core.Session{{managed}, {managed}},
		refreshErrs: [][]error{{nil}, {nil}},
	}
	observer := &fakeObserver{results: []observation{
		{err: errors.New("observer unavailable")},
		{err: errors.New("observer unavailable")},
		{err: errors.New("observer unavailable")},
	}}
	coordinator := newCoordinator("workspace", store, supervisor, observer, func() time.Time { return cycleOne })
	if _, err := coordinator.Start(); err != nil {
		t.Fatal(err)
	}
	coordinator.Close()

	got := coordinator.Refresh()
	if len(supervisor.refreshCalls) != 0 || len(observer.calls) != 1 {
		t.Errorf("dependency calls after Close() = refresh %d, observe %d; want unchanged 0, 1", len(supervisor.refreshCalls), len(observer.calls))
	}
	assertSnapshot(t, got, "workspace", []string{"managed"}, []Warning{{Message: "observe Codex sessions: observer unavailable"}}, cycleOne)

	got.Sessions[0].Processes[0].Name = "mutated"
	got.Warnings[0].Message = "mutated"
	next := coordinator.Refresh()
	if next.Sessions[0].Processes[0].Name == "mutated" || next.Warnings[0].Message == "mutated" {
		t.Fatal("Refresh() after Close() exposed mutable coordinator state")
	}
}

func TestCloseWaitsForBlockedStartupRecovery(t *testing.T) {
	loaded := session("managed", core.ConfidenceSupervised, core.StateRunning, baseTime)
	supervisor := &blockingSupervisor{
		recovered:      []core.Session{loaded},
		recoverErrs:    []error{nil},
		recoverStarted: make(chan struct{}),
		recoverRelease: make(chan struct{}),
		closed:         make(chan struct{}),
	}
	observer := &fakeObserver{results: []observation{{}}}
	coordinator := newCoordinator("workspace", &fakeStore{sessions: []core.Session{loaded}}, supervisor, observer, func() time.Time { return cycleOne })

	startDone := make(chan error, 1)
	go func() {
		_, err := coordinator.Start()
		startDone <- err
	}()
	awaitSignal(t, supervisor.recoverStarted)

	closeDone := beginClose(coordinator)
	assertNotSignaled(t, supervisor.closed)

	close(supervisor.recoverRelease)
	if err := awaitError(t, startDone); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	awaitSignal(t, supervisor.closed)
	awaitSignal(t, closeDone)
}

func TestCloseWaitsForBlockedRefresh(t *testing.T) {
	loaded := session("managed", core.ConfidenceSupervised, core.StateRunning, baseTime)
	supervisor := &blockingSupervisor{
		recovered:      []core.Session{loaded},
		recoverErrs:    []error{nil},
		refreshed:      []core.Session{loaded},
		refreshErrs:    []error{nil},
		refreshStarted: make(chan struct{}),
		refreshRelease: make(chan struct{}),
		closed:         make(chan struct{}),
	}
	observer := &fakeObserver{results: []observation{{}, {}}}
	coordinator := newCoordinator("workspace", &fakeStore{sessions: []core.Session{loaded}}, supervisor, observer, clockSequence(cycleOne, cycleTwo))
	if _, err := coordinator.Start(); err != nil {
		t.Fatal(err)
	}

	refreshDone := make(chan struct{})
	go func() {
		coordinator.Refresh()
		close(refreshDone)
	}()
	awaitSignal(t, supervisor.refreshStarted)

	closeDone := beginClose(coordinator)
	assertNotSignaled(t, supervisor.closed)

	close(supervisor.refreshRelease)
	awaitSignal(t, refreshDone)
	awaitSignal(t, supervisor.closed)
	awaitSignal(t, closeDone)
}

func beginClose(coordinator *Coordinator) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		coordinator.Close()
		close(done)
	}()
	return done
}

func assertNotSignaled(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	for range 1000 {
		runtime.Gosched()
		select {
		case <-signal:
			t.Fatal("Supervisor.Close() was called before the in-flight operation completed")
		default:
		}
	}
}

func awaitSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for signal")
	}
}

func awaitError(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for result")
		return nil
	}
}

func session(id string, confidence core.Confidence, state core.State, startedAt time.Time, processes ...core.Process) core.Session {
	return core.Session{
		ID:         id,
		Tool:       core.ToolCodexCLI,
		Confidence: confidence,
		Workspace:  "workspace-" + id,
		StartedAt:  startedAt,
		Processes:  append([]core.Process(nil), processes...),
		State:      state,
	}
}

func process(pid uint32, createdAt time.Time) core.Process {
	return core.Process{PID: pid, Name: "process", CreatedAt: createdAt, CPUAvailable: true, MemoryAvailable: true}
}

func cloneTestSessions(sessions []core.Session) []core.Session {
	cloned := append([]core.Session(nil), sessions...)
	for i := range cloned {
		cloned[i].Processes = append([]core.Process(nil), sessions[i].Processes...)
	}
	return cloned
}

func ids(sessions []core.Session) []string {
	result := make([]string, len(sessions))
	for i := range sessions {
		result[i] = sessions[i].ID
	}
	return result
}

func sameIdentities(got, want map[uint32]core.ProcessIdentity) bool {
	if len(got) != len(want) {
		return false
	}
	for pid, identity := range want {
		if got[pid] != identity {
			return false
		}
	}
	return true
}

func findOptional(sessions []core.Session, id string) *core.Session {
	for i := range sessions {
		if sessions[i].ID == id {
			return &sessions[i]
		}
	}
	return nil
}

func findSession(t *testing.T, sessions []core.Session, id string) core.Session {
	t.Helper()
	found := findOptional(sessions, id)
	if found == nil {
		t.Fatalf("session %q not found in %v", id, ids(sessions))
	}
	return *found
}

func assertSnapshot(t *testing.T, got Snapshot, workspace string, wantIDs []string, wantWarnings []Warning, refreshedAt time.Time) {
	t.Helper()
	if got.CurrentDir != workspace || strings.Join(ids(got.Sessions), ",") != strings.Join(wantIDs, ",") || got.RefreshedAt != refreshedAt {
		t.Fatalf("snapshot = %#v, want current dir %q, IDs %v, refreshed %v", got, workspace, wantIDs, refreshedAt)
	}
	assertWarnings(t, got.Warnings, wantWarnings)
}

func assertWarnings(t *testing.T, got, want []Warning) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("warnings = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("warnings = %#v, want %#v", got, want)
		}
	}
}

func clockSequence(times ...time.Time) func() time.Time {
	index := 0
	return func() time.Time {
		result := times[index]
		index++
		return result
	}
}
