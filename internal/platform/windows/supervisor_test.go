package windows

import (
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"taskwatch/internal/core"
)

type fakeResolver struct {
	paths  map[string]string
	looked []string
}

func (resolver *fakeResolver) LookPath(name string) (string, error) {
	resolver.looked = append(resolver.looked, name)
	path, ok := resolver.paths[name]
	if !ok {
		return "", errors.New("not found")
	}
	return path, nil
}

type fakeNative struct {
	events             []string
	failAssign         bool
	jobPIDs            []uint32
	createdAt          time.Time
	createdByPID       map[uint32]time.Time
	openedForTerminate []uint32
	failOpenPID        uint32
	terminatedPIDs     []uint32
}

func (native *fakeNative) createJob(string) (nativeHandle, error) {
	native.events = append(native.events, "create-job")
	return 1, nil
}
func (native *fakeNative) openJob(string) (nativeHandle, error) {
	native.events = append(native.events, "open-job")
	return 1, nil
}
func (native *fakeNative) createSuspended(string, string) (nativeHandle, nativeHandle, uint32, error) {
	native.events = append(native.events, "create-suspended")
	return 2, 3, 42, nil
}
func (native *fakeNative) assignProcess(nativeHandle, nativeHandle) error {
	native.events = append(native.events, "assign")
	if native.failAssign {
		return errors.New("assign failed")
	}
	return nil
}
func (native *fakeNative) resumeThread(nativeHandle) error {
	native.events = append(native.events, "resume")
	return nil
}
func (native *fakeNative) processCreatedAt(handle nativeHandle) (time.Time, error) {
	native.events = append(native.events, "created-at")
	if createdAt, exists := native.createdByPID[uint32(handle)]; exists {
		return createdAt, nil
	}
	return native.createdAt, nil
}
func (native *fakeNative) terminateProcess(nativeHandle) error {
	native.events = append(native.events, "terminate-suspended")
	return nil
}
func (native *fakeNative) closeHandle(nativeHandle) {
	native.events = append(native.events, "close")
}
func (native *fakeNative) jobProcessIDs(nativeHandle) ([]uint32, error) {
	native.events = append(native.events, "job-pids")
	return append([]uint32(nil), native.jobPIDs...), nil
}
func (native *fakeNative) openProcessForTerminate(pid uint32) (nativeHandle, error) {
	native.openedForTerminate = append(native.openedForTerminate, pid)
	if pid == native.failOpenPID {
		return 0, errors.New("access denied")
	}
	return nativeHandle(pid), nil
}
func (native *fakeNative) terminateOpenedProcess(handle nativeHandle) error {
	native.terminatedPIDs = append(native.terminatedPIDs, uint32(handle))
	return nil
}

type fakeSnapshotter struct {
	processes []ProcessDetails
	err       error
}

func (snapshotter *fakeSnapshotter) Snapshot() ([]ProcessDetails, error) {
	return append([]ProcessDetails(nil), snapshotter.processes...), snapshotter.err
}

func TestLaunchResolvesOnlySupportedToolAndAssignsBeforeResume(t *testing.T) {
	created := time.Unix(100, 0).UTC()
	resolver := &fakeResolver{paths: map[string]string{"codex": `C:\tools\codex.exe`}}
	native := &fakeNative{createdAt: created}
	supervisor := newSupervisor(resolver, native, &fakeSnapshotter{})

	session, err := supervisor.Launch(core.ToolCodexCLI, `C:\work`)
	if err != nil {
		t.Fatalf("Launch() error = %v", err)
	}
	if !reflect.DeepEqual(resolver.looked, []string{"codex"}) {
		t.Fatalf("resolver calls = %v, want [codex]", resolver.looked)
	}
	if index(native.events, "assign") > index(native.events, "resume") {
		t.Fatalf("native events = %v, assignment must precede resume", native.events)
	}
	if session.Tool != core.ToolCodexCLI || session.Root.PID != 42 || !session.Root.CreatedAt.Equal(created) || session.Confidence != core.ConfidenceSupervised {
		t.Fatalf("Launch() session = %#v", session)
	}
}

func TestLaunchAssignmentFailureTerminatesSuspendedProcess(t *testing.T) {
	resolver := &fakeResolver{paths: map[string]string{"claude": `C:\tools\claude.exe`}}
	native := &fakeNative{failAssign: true, createdAt: time.Unix(100, 0).UTC()}
	supervisor := newSupervisor(resolver, native, &fakeSnapshotter{})

	if _, err := supervisor.Launch(core.ToolClaudeCLI, `C:\work`); err == nil {
		t.Fatal("Launch() accepted failed job assignment")
	}
	if index(native.events, "terminate-suspended") == -1 || index(native.events, "resume") != -1 {
		t.Fatalf("native events = %v, want suspended process terminated and never resumed", native.events)
	}
}

func TestLaunchRejectsUnsupportedToolWithoutResolution(t *testing.T) {
	resolver := &fakeResolver{}
	supervisor := newSupervisor(resolver, &fakeNative{}, &fakeSnapshotter{})

	if _, err := supervisor.Launch(core.ToolCodexDesktop, `C:\work`); err == nil {
		t.Fatal("Launch() accepted unsupported tool")
	}
	if len(resolver.looked) != 0 {
		t.Fatalf("resolver calls = %v, want none", resolver.looked)
	}
}

func TestRecoverDerivesLeftoversFromNamedJobMembership(t *testing.T) {
	created := time.Unix(100, 0).UTC()
	native := &fakeNative{jobPIDs: []uint32{43}}
	snapshotter := &fakeSnapshotter{processes: []ProcessDetails{{Process: core.Process{PID: 43, ParentPID: 42, Name: "node.exe", CreatedAt: created.Add(time.Second)}, MetadataAccessible: true}}}
	supervisor := newSupervisor(&fakeResolver{}, native, snapshotter)
	metadata := core.Session{ID: "s1", Tool: core.ToolCodexCLI, Confidence: core.ConfidenceSupervised, JobName: "TaskWatch-test", Root: core.ProcessIdentity{PID: 42, CreatedAt: created}}

	got, err := supervisor.Recover(metadata)
	if err != nil {
		t.Fatalf("Recover() error = %v", err)
	}
	if got.State != core.StateLeftovers || len(got.Processes) != 1 || got.Processes[0].PID != 43 {
		t.Fatalf("Recover() = %#v, want one leftover", got)
	}
}

func TestTerminateSupervisedRequiresConfirmation(t *testing.T) {
	supervisor := newSupervisor(&fakeResolver{}, &fakeNative{}, &fakeSnapshotter{})
	session := core.Session{ID: "s1", Confidence: core.ConfidenceSupervised, JobName: "TaskWatch-test"}
	request := core.NewTerminationRequest(session.ID, []core.Process{{PID: 42, CreatedAt: time.Unix(100, 0)}})

	if err := supervisor.TerminateSupervised(request, session); err == nil {
		t.Fatal("TerminateSupervised() accepted unconfirmed request")
	}
}

func TestTerminateSupervisedRejectsConfirmationForDifferentSession(t *testing.T) {
	native := &fakeNative{}
	supervisor := newSupervisor(&fakeResolver{}, native, &fakeSnapshotter{})
	sessionB := core.Session{ID: "session-b", Confidence: core.ConfidenceSupervised, JobName: "TaskWatch-b"}
	requestForA := core.NewTerminationRequest("session-a", []core.Process{{PID: 42, CreatedAt: time.Unix(100, 0)}}).Confirm()

	if err := supervisor.TerminateSupervised(requestForA, sessionB); err == nil {
		t.Fatal("TerminateSupervised() accepted another session's confirmation")
	}
	if index(native.events, "open-job") != -1 || index(native.events, "terminate-job") != -1 {
		t.Fatalf("native events = %v, want no job operation", native.events)
	}
}

func TestTerminateSupervisedRejectsConfirmedEmptyRequest(t *testing.T) {
	native := &fakeNative{}
	supervisor := newSupervisor(&fakeResolver{}, native, &fakeSnapshotter{})
	session := core.Session{ID: "session-a", Confidence: core.ConfidenceSupervised, JobName: "TaskWatch-a"}
	request := core.NewTerminationRequest(session.ID, nil).Confirm()

	if err := supervisor.TerminateSupervised(request, session); err == nil {
		t.Fatal("TerminateSupervised() accepted confirmed empty target set")
	}
	if index(native.events, "open-job") != -1 || index(native.events, "terminate-job") != -1 {
		t.Fatalf("native events = %v, want no job operation", native.events)
	}
}

func TestTerminateSupervisedTerminatesOnlyExactConfirmedTargets(t *testing.T) {
	created := time.Unix(100, 0).UTC()
	processes := []core.Process{
		{PID: 42, CreatedAt: created},
		{PID: 43, ParentPID: 42, CreatedAt: created.Add(time.Second)},
	}
	native := &fakeNative{
		jobPIDs: []uint32{42, 43},
		createdByPID: map[uint32]time.Time{
			42: created,
			43: created.Add(time.Second),
		},
	}
	snapshotter := &fakeSnapshotter{processes: []ProcessDetails{
		{Process: processes[0], MetadataAccessible: true},
		{Process: processes[1], MetadataAccessible: true},
	}}
	supervisor := newSupervisor(&fakeResolver{}, native, snapshotter)
	session := core.Session{
		ID:         "session-a",
		Confidence: core.ConfidenceSupervised,
		JobName:    "TaskWatch-a",
		Processes:  processes,
	}
	request := core.NewTerminationRequest(session.ID, processes).Confirm()

	if err := supervisor.TerminateSupervised(request, session); err != nil {
		t.Fatalf("TerminateSupervised() error = %v", err)
	}
	if !reflect.DeepEqual(native.openedForTerminate, []uint32{42, 43}) {
		t.Fatalf("preflight opens = %v, want exact confirmed targets", native.openedForTerminate)
	}
	if !reflect.DeepEqual(native.terminatedPIDs, []uint32{43, 42}) {
		t.Fatalf("terminated PIDs = %v, want exact targets child-first", native.terminatedPIDs)
	}
	if index(native.events, "terminate-job") != -1 {
		t.Fatalf("native events = %v, must not terminate the whole job", native.events)
	}
}

func TestTerminateSupervisedRejectsJobMemberAddedAfterConfirmation(t *testing.T) {
	created := time.Unix(100, 0).UTC()
	confirmed := []core.Process{{PID: 42, CreatedAt: created}}
	current := []ProcessDetails{
		{Process: confirmed[0], MetadataAccessible: true},
		{Process: core.Process{PID: 43, ParentPID: 42, CreatedAt: created.Add(time.Second)}, MetadataAccessible: true},
	}
	native := &fakeNative{jobPIDs: []uint32{42, 43}}
	supervisor := newSupervisor(&fakeResolver{}, native, &fakeSnapshotter{processes: current})
	session := core.Session{
		ID:         "session-a",
		Confidence: core.ConfidenceSupervised,
		JobName:    "TaskWatch-a",
		Processes:  confirmed,
	}
	request := core.NewTerminationRequest(session.ID, confirmed).Confirm()

	if err := supervisor.TerminateSupervised(request, session); err == nil {
		t.Fatal("TerminateSupervised() accepted a job member added after confirmation")
	}
	if len(native.openedForTerminate) != 0 || len(native.terminatedPIDs) != 0 || index(native.events, "terminate-job") != -1 {
		t.Fatalf("opened/terminated/events = %v/%v/%v, want zero process termination", native.openedForTerminate, native.terminatedPIDs, native.events)
	}
}

func TestTerminateSupervisedRejectsReusedPIDWithoutKillingAnything(t *testing.T) {
	created := time.Unix(100, 0).UTC()
	processes := []core.Process{
		{PID: 42, CreatedAt: created},
		{PID: 43, ParentPID: 42, CreatedAt: created.Add(time.Second)},
	}
	native := &fakeNative{
		jobPIDs: []uint32{42, 43},
		createdByPID: map[uint32]time.Time{
			42: created,
			43: created.Add(time.Hour),
		},
	}
	snapshotter := &fakeSnapshotter{processes: []ProcessDetails{
		{Process: processes[0], MetadataAccessible: true},
		{Process: processes[1], MetadataAccessible: true},
	}}
	supervisor := newSupervisor(&fakeResolver{}, native, snapshotter)
	session := core.Session{
		ID:         "session-a",
		Confidence: core.ConfidenceSupervised,
		JobName:    "TaskWatch-a",
		Processes:  processes,
	}
	request := core.NewTerminationRequest(session.ID, processes).Confirm()

	if err := supervisor.TerminateSupervised(request, session); err == nil {
		t.Fatal("TerminateSupervised() accepted a PID reused during preflight")
	}
	if !reflect.DeepEqual(native.openedForTerminate, []uint32{42, 43}) {
		t.Fatalf("preflight opens = %v, want every target checked before termination", native.openedForTerminate)
	}
	if len(native.terminatedPIDs) != 0 {
		t.Fatalf("terminated PIDs = %v, want zero", native.terminatedPIDs)
	}
}

func TestTerminateSupervisedRejectsMissingJobMemberWithoutKillingAnything(t *testing.T) {
	created := time.Unix(100, 0).UTC()
	processes := []core.Process{{PID: 42, CreatedAt: created}}
	native := &fakeNative{jobPIDs: []uint32{42}}
	supervisor := newSupervisor(&fakeResolver{}, native, &fakeSnapshotter{})
	session := core.Session{
		ID:         "session-a",
		Confidence: core.ConfidenceSupervised,
		JobName:    "TaskWatch-a",
		Processes:  processes,
	}
	request := core.NewTerminationRequest(session.ID, processes).Confirm()

	if err := supervisor.TerminateSupervised(request, session); err == nil {
		t.Fatal("TerminateSupervised() accepted a job member missing from the process snapshot")
	}
	if len(native.openedForTerminate) != 0 || len(native.terminatedPIDs) != 0 {
		t.Fatalf("opened/terminated = %v/%v, want zero", native.openedForTerminate, native.terminatedPIDs)
	}
}

func TestTerminateSupervisedRejectsInaccessibleJobMemberWithoutKillingAnything(t *testing.T) {
	created := time.Unix(100, 0).UTC()
	processes := []core.Process{{PID: 42, CreatedAt: created}}
	native := &fakeNative{jobPIDs: []uint32{42}}
	snapshotter := &fakeSnapshotter{processes: []ProcessDetails{{
		Process:            processes[0],
		MetadataAccessible: false,
		MetadataError:      "access denied",
	}}}
	supervisor := newSupervisor(&fakeResolver{}, native, snapshotter)
	session := core.Session{
		ID:         "session-a",
		Confidence: core.ConfidenceSupervised,
		JobName:    "TaskWatch-a",
		Processes:  processes,
	}
	request := core.NewTerminationRequest(session.ID, processes).Confirm()

	if err := supervisor.TerminateSupervised(request, session); err == nil {
		t.Fatal("TerminateSupervised() accepted inaccessible job membership")
	}
	if len(native.openedForTerminate) != 0 || len(native.terminatedPIDs) != 0 {
		t.Fatalf("opened/terminated = %v/%v, want zero", native.openedForTerminate, native.terminatedPIDs)
	}
}

func TestRefreshTreatsUnreadableMatchedJobMemberAsInaccessible(t *testing.T) {
	created := time.Unix(100, 0).UTC()
	native := &fakeNative{jobPIDs: []uint32{42}}
	snapshotter := &fakeSnapshotter{processes: []ProcessDetails{{
		Process:            core.Process{PID: 42, Name: "codex.exe", CreatedAt: created},
		MetadataAccessible: false,
		MetadataError:      "access denied",
	}}}
	supervisor := newSupervisor(&fakeResolver{}, native, snapshotter)
	session := core.Session{ID: "s1", Confidence: core.ConfidenceSupervised, JobName: "TaskWatch-test", Root: core.ProcessIdentity{PID: 42, CreatedAt: created}}

	got, err := supervisor.Refresh(session)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if got.State != core.StateInaccessible {
		t.Fatalf("Refresh() state = %q, want inaccessible", got.State)
	}
}

func TestTerminateObservedPreflightsAllTargetsBeforeLeafFirstKill(t *testing.T) {
	created := time.Unix(100, 0).UTC()
	current := []ProcessDetails{
		{Process: core.Process{PID: 10, ParentPID: 1, CreatedAt: created}},
		{Process: core.Process{PID: 11, ParentPID: 10, CreatedAt: created.Add(time.Second)}},
		{Process: core.Process{PID: 12, ParentPID: 11, CreatedAt: created.Add(2 * time.Second)}},
	}
	native := &fakeNative{createdByPID: map[uint32]time.Time{
		10: created,
		11: created.Add(time.Second),
		12: created.Add(2 * time.Second),
	}}
	supervisor := newSupervisor(&fakeResolver{}, native, &fakeSnapshotter{processes: current})
	request := core.NewTerminationRequest("observed-10", detailsProcesses(current)).Confirm()

	if err := supervisor.TerminateObserved(request); err != nil {
		t.Fatalf("TerminateObserved() error = %v", err)
	}
	if !reflect.DeepEqual(native.openedForTerminate, []uint32{10, 11, 12}) {
		t.Fatalf("preflight opens = %v, want all targets", native.openedForTerminate)
	}
	if !reflect.DeepEqual(native.terminatedPIDs, []uint32{12, 11, 10}) {
		t.Fatalf("termination order = %v, want leaf-first", native.terminatedPIDs)
	}
}

func TestTerminateObservedPreflightFailureKillsNothing(t *testing.T) {
	created := time.Unix(100, 0).UTC()
	current := []ProcessDetails{
		{Process: core.Process{PID: 10, ParentPID: 1, CreatedAt: created}},
		{Process: core.Process{PID: 11, ParentPID: 10, CreatedAt: created.Add(time.Second)}},
	}
	native := &fakeNative{failOpenPID: 11, createdByPID: map[uint32]time.Time{10: created}}
	supervisor := newSupervisor(&fakeResolver{}, native, &fakeSnapshotter{processes: current})
	request := core.NewTerminationRequest("observed-10", detailsProcesses(current)).Confirm()

	if err := supervisor.TerminateObserved(request); err == nil {
		t.Fatal("TerminateObserved() accepted failed preflight")
	}
	if len(native.terminatedPIDs) != 0 {
		t.Fatalf("terminated PIDs = %v, want zero", native.terminatedPIDs)
	}
}

func TestTerminateObservedReusedPIDDuringPreflightKillsNothing(t *testing.T) {
	created := time.Unix(100, 0).UTC()
	current := []ProcessDetails{
		{Process: core.Process{PID: 10, ParentPID: 1, CreatedAt: created}},
		{Process: core.Process{PID: 11, ParentPID: 10, CreatedAt: created.Add(time.Second)}},
	}
	native := &fakeNative{createdByPID: map[uint32]time.Time{
		10: created,
		11: created.Add(time.Hour),
	}}
	supervisor := newSupervisor(&fakeResolver{}, native, &fakeSnapshotter{processes: current})
	request := core.NewTerminationRequest("observed-10", detailsProcesses(current)).Confirm()

	if err := supervisor.TerminateObserved(request); err == nil {
		t.Fatal("TerminateObserved() accepted a PID reused after the snapshot")
	}
	if len(native.terminatedPIDs) != 0 {
		t.Fatalf("terminated PIDs = %v, want zero", native.terminatedPIDs)
	}
}

func TestTerminateObservedDoesNotDependOnConfirmationOrder(t *testing.T) {
	created := time.Unix(100, 0).UTC()
	current := []ProcessDetails{
		{Process: core.Process{PID: 10, ParentPID: 1, CreatedAt: created}},
		{Process: core.Process{PID: 11, ParentPID: 10, CreatedAt: created.Add(time.Second)}},
	}
	native := &fakeNative{createdByPID: map[uint32]time.Time{
		10: created,
		11: created.Add(time.Second),
	}}
	supervisor := newSupervisor(&fakeResolver{}, native, &fakeSnapshotter{processes: current})
	request := core.NewTerminationRequest("observed-10", []core.Process{current[1].Process, current[0].Process}).Confirm()

	if err := supervisor.TerminateObserved(request); err != nil {
		t.Fatalf("TerminateObserved() error for reordered confirmation = %v", err)
	}
	if !reflect.DeepEqual(native.terminatedPIDs, []uint32{11, 10}) {
		t.Fatalf("termination order = %v, want leaf-first", native.terminatedPIDs)
	}
}

func TestTerminateObservedRejectsFalseAncestryFromOlderChild(t *testing.T) {
	created := time.Unix(100, 0).UTC()
	current := []ProcessDetails{
		{Process: core.Process{PID: 10, ParentPID: 1, CreatedAt: created}, MetadataAccessible: true},
		{Process: core.Process{PID: 11, ParentPID: 10, CreatedAt: created.Add(-time.Second)}, MetadataAccessible: true},
	}
	native := &fakeNative{createdByPID: map[uint32]time.Time{10: created, 11: created.Add(-time.Second)}}
	supervisor := newSupervisor(&fakeResolver{}, native, &fakeSnapshotter{processes: current})
	request := core.NewTerminationRequest("observed-10", detailsProcesses(current)).Confirm()

	if err := supervisor.TerminateObserved(request); err == nil {
		t.Fatal("TerminateObserved() accepted an older process as the root's descendant")
	}
	if len(native.openedForTerminate) != 0 || len(native.terminatedPIDs) != 0 {
		t.Fatalf("opened/terminated = %v/%v, want zero", native.openedForTerminate, native.terminatedPIDs)
	}
}

func TestWindowsTerminationHandleCanReadCreationIdentity(t *testing.T) {
	native := nativeWindows{}
	handle, err := native.openProcessForTerminate(uint32(os.Getpid()))
	if err != nil {
		t.Fatalf("openProcessForTerminate(current process) error = %v", err)
	}
	defer native.closeHandle(handle)

	if _, err := native.processCreatedAt(handle); err != nil {
		t.Fatalf("processCreatedAt(termination handle) error = %v", err)
	}
}

func TestPruneCPUSamplesRemovesAbsentPIDs(t *testing.T) {
	created := time.Unix(100, 0).UTC()
	snapshotter := &ProcessSnapshotter{previous: map[uint32]cpuSample{
		10: {createdAt: created},
		20: {createdAt: created},
	}}

	snapshotter.pruneCPUSamples(map[uint32]struct{}{20: {}})
	if _, exists := snapshotter.previous[10]; exists {
		t.Fatal("pruneCPUSamples() retained absent PID 10")
	}
	if _, exists := snapshotter.previous[20]; !exists {
		t.Fatal("pruneCPUSamples() removed live PID 20")
	}
}

func index(values []string, value string) int {
	for i, candidate := range values {
		if candidate == value {
			return i
		}
	}
	return -1
}
