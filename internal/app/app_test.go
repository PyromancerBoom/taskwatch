package app

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"taskwatch/internal/core"
)

var (
	baseTime = time.Date(2026, 7, 15, 9, 0, 0, 0, time.UTC)
	cycleOne = baseTime.Add(time.Minute)
	cycleTwo = baseTime.Add(2 * time.Minute)
)

type fakeStore struct {
	sessions  []core.Session
	loadErr   error
	saveErr   error
	loadCalls int
	saveCalls int
	saved     [][]core.Session
}

func (store *fakeStore) Load() ([]core.Session, error) {
	store.loadCalls++
	return cloneTestSessions(store.sessions), store.loadErr
}

func (store *fakeStore) Save(sessions []core.Session) error {
	store.saveCalls++
	store.saved = append(store.saved, cloneTestSessions(sessions))
	return store.saveErr
}

type fakeSupervisor struct {
	recovered    []core.Session
	recoverErrs  []error
	refreshed    [][]core.Session
	refreshErrs  [][]error
	recoverCalls [][]core.Session
	refreshCalls [][]core.Session
	closeCalls   int
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
	assertSnapshot(t, snapshot, "workspace", []string{"supervised"}, []Warning{{Message: "observe desktop sessions: snapshot failed"}}, cycleOne)
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
	assertWarnings(t, retained.Warnings, []Warning{{Message: "observe desktop sessions: desktop snapshot unavailable"}})
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
	assertWarnings(t, snapshot.Warnings, []Warning{{Message: "save reconciled sessions: disk full"}})
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
	if len(next.Warnings) != 0 {
		t.Fatalf("warnings accumulated across cycles: %#v", next.Warnings)
	}
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

func TestCloseDelegatesToSupervisor(t *testing.T) {
	supervisor := &fakeSupervisor{}
	coordinator := NewCoordinator("workspace", &fakeStore{}, supervisor, &fakeObserver{})

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
	assertSnapshot(t, got, "workspace", []string{"managed"}, []Warning{{Message: "observe desktop sessions: observer unavailable"}}, cycleOne)

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
	if got.Workspace != workspace || strings.Join(ids(got.Sessions), ",") != strings.Join(wantIDs, ",") || got.RefreshedAt != refreshedAt {
		t.Fatalf("snapshot = %#v, want workspace %q, IDs %v, refreshed %v", got, workspace, wantIDs, refreshedAt)
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
