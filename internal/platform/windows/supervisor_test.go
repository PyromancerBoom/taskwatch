package windows

import (
	"errors"
	"os"
	"reflect"
	"syscall"
	"testing"
	"time"
	"unsafe"

	winapi "golang.org/x/sys/windows"

	"github.com/PyromancerBoom/taskwatch/internal/core"
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
	failKeeper         bool
	jobPIDs            []uint32
	jobHandles         map[string]nativeHandle
	openJobErrors      map[string]error
	openedJobNames     []string
	jobPIDsByHandle    map[nativeHandle][]uint32
	jobPIDErrors       map[nativeHandle]error
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
func (native *fakeNative) openJob(name string) (nativeHandle, error) {
	native.events = append(native.events, "open-job")
	native.openedJobNames = append(native.openedJobNames, name)
	if err := native.openJobErrors[name]; err != nil {
		return 0, err
	}
	if handle, exists := native.jobHandles[name]; exists {
		return handle, nil
	}
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
func (native *fakeNative) startJobKeeper(nativeHandle) error {
	native.events = append(native.events, "start-job-keeper")
	if native.failKeeper {
		return errors.New("keeper failed")
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
func (native *fakeNative) jobProcessIDs(handle nativeHandle) ([]uint32, error) {
	native.events = append(native.events, "job-pids")
	if err := native.jobPIDErrors[handle]; err != nil {
		return nil, err
	}
	if pids, exists := native.jobPIDsByHandle[handle]; exists {
		return append([]uint32(nil), pids...), nil
	}
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
	calls     int
}

func observedSession(id string, root core.Process) core.Session {
	return core.Session{
		ID:         id,
		Tool:       core.ToolCodexDesktop,
		Confidence: core.ConfidenceObserved,
		Root:       root.Identity(),
	}
}

func TestCurrentProcessIdentityMatchesSnapshot(t *testing.T) {
	identity, err := CurrentProcessIdentity()
	if err != nil {
		t.Fatalf("CurrentProcessIdentity() error = %v", err)
	}

	snapshot, err := NewProcessSnapshotter().Snapshot()
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	for _, detail := range snapshot {
		if detail.Process.PID == identity.PID {
			if got := detail.Process.Identity(); got != identity {
				t.Fatalf("snapshot identity = %#v, want %#v", got, identity)
			}
			return
		}
	}
	t.Fatalf("current PID %d missing from snapshot", identity.PID)
}

func TestJobKeeperHandleHasOnlyQueryAccess(t *testing.T) {
	native := nativeWindows{}
	job, err := native.createJob("")
	if err != nil {
		t.Fatalf("createJob() error = %v", err)
	}
	defer native.closeHandle(job)

	keeper, err := duplicateJobKeeperHandle(job)
	if err != nil {
		t.Fatalf("duplicateJobKeeperHandle() error = %v", err)
	}
	defer native.closeHandle(keeper)
	if _, err := native.jobProcessIDs(keeper); err != nil {
		t.Fatalf("jobProcessIDs() error = %v, want query access", err)
	}
	if granted := grantedAccess(t, keeper); granted != jobObjectQuery {
		t.Fatalf("keeper granted access = %#x, want query-only %#x", granted, jobObjectQuery)
	}

	limits := winapi.JOBOBJECT_BASIC_LIMIT_INFORMATION{}
	_, err = winapi.SetInformationJobObject(
		winapi.Handle(keeper),
		winapi.JobObjectBasicLimitInformation,
		uintptr(unsafe.Pointer(&limits)),
		uint32(unsafe.Sizeof(limits)),
	)
	if !errors.Is(err, syscall.ERROR_ACCESS_DENIED) {
		t.Fatalf("SetInformationJobObject() error = %v, want access denied", err)
	}
}

func grantedAccess(t *testing.T, handle nativeHandle) uint32 {
	t.Helper()
	type objectBasicInformation struct {
		Attributes    uint32
		GrantedAccess uint32
		HandleCount   uint32
		PointerCount  uint32
		Reserved      [10]uint32
	}
	information := objectBasicInformation{}
	var returned uint32
	procNtQueryObject := winapi.NewLazySystemDLL("ntdll.dll").NewProc("NtQueryObject")
	status, _, _ := procNtQueryObject.Call(
		uintptr(handle),
		0,
		uintptr(unsafe.Pointer(&information)),
		unsafe.Sizeof(information),
		uintptr(unsafe.Pointer(&returned)),
	)
	if int32(status) < 0 {
		t.Fatalf("NtQueryObject() status = %#x", uint32(status))
	}
	return information.GrantedAccess
}

func (snapshotter *fakeSnapshotter) Snapshot() ([]ProcessDetails, error) {
	snapshotter.calls++
	return append([]ProcessDetails(nil), snapshotter.processes...), snapshotter.err
}

func TestLaunchResolvesOnlySupportedToolAndStartsKeeperBeforeResume(t *testing.T) {
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
	if index(native.events, "assign") > index(native.events, "start-job-keeper") || index(native.events, "start-job-keeper") > index(native.events, "resume") {
		t.Fatalf("native events = %v, assignment and keeper start must precede resume", native.events)
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

func TestLaunchJobKeeperFailureTerminatesSuspendedProcess(t *testing.T) {
	resolver := &fakeResolver{paths: map[string]string{"claude": `C:\tools\claude.exe`}}
	native := &fakeNative{failKeeper: true, createdAt: time.Unix(100, 0).UTC()}
	supervisor := newSupervisor(resolver, native, &fakeSnapshotter{})

	if _, err := supervisor.Launch(core.ToolClaudeCLI, `C:\work`); err == nil {
		t.Fatal("Launch() accepted failed job keeper startup")
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

func TestRefreshManyUsesOneSnapshotForMultipleJobs(t *testing.T) {
	created := time.Unix(100, 0).UTC()
	native := &fakeNative{
		jobHandles: map[string]nativeHandle{"job-active": 1, "job-leftovers": 2, "job-empty": 3},
		jobPIDsByHandle: map[nativeHandle][]uint32{
			1: {10},
			2: {21},
			3: {},
		},
	}
	snapshotter := &fakeSnapshotter{processes: []ProcessDetails{
		{Process: core.Process{PID: 10, CreatedAt: created, CPUAvailable: true, MemoryAvailable: true}, MetadataAccessible: true},
		{Process: core.Process{PID: 21, ParentPID: 20, CreatedAt: created.Add(time.Second), CPUAvailable: true, MemoryAvailable: true}, MetadataAccessible: true},
	}}
	supervisor := newSupervisor(&fakeResolver{}, native, snapshotter)
	sessions := []core.Session{
		{ID: "active", JobName: "job-active", Root: core.ProcessIdentity{PID: 10, CreatedAt: created}},
		{ID: "leftovers", JobName: "job-leftovers", Root: core.ProcessIdentity{PID: 20, CreatedAt: created}},
		{ID: "empty", JobName: "job-empty", Root: core.ProcessIdentity{PID: 30, CreatedAt: created}},
	}

	got, errs := supervisor.RefreshMany(sessions)
	if len(got) != len(sessions) || len(errs) != len(sessions) {
		t.Fatalf("result lengths = %d/%d, want %d/%d", len(got), len(errs), len(sessions), len(sessions))
	}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("errors[%d] = %v, want nil", i, err)
		}
		if got[i].ID != sessions[i].ID {
			t.Fatalf("results[%d].ID = %q, want %q", i, got[i].ID, sessions[i].ID)
		}
	}
	if got[0].State != core.StateRunning || got[1].State != core.StateLeftovers || got[2].State != core.StateExited {
		t.Fatalf("states = %q/%q/%q, want running/leftovers/exited", got[0].State, got[1].State, got[2].State)
	}
	if snapshotter.calls != 1 {
		t.Fatalf("snapshot calls = %d, want 1", snapshotter.calls)
	}
}

func TestRefreshManyTreatsMissingJobAsExitedWhileRefreshingLiveJobs(t *testing.T) {
	created := time.Unix(100, 0).UTC()
	native := &fakeNative{
		jobHandles: map[string]nativeHandle{"job-first": 1, "job-last": 2},
		openJobErrors: map[string]error{
			"job-missing": syscall.ERROR_FILE_NOT_FOUND,
		},
		jobPIDsByHandle: map[nativeHandle][]uint32{
			1: {10},
			2: {20},
		},
	}
	snapshotter := &fakeSnapshotter{processes: []ProcessDetails{
		{Process: core.Process{PID: 10, CreatedAt: created, CPUAvailable: true, MemoryAvailable: true}, MetadataAccessible: true},
		{Process: core.Process{PID: 20, CreatedAt: created.Add(time.Second), CPUAvailable: true, MemoryAvailable: true}, MetadataAccessible: true},
	}}
	supervisor := newSupervisor(&fakeResolver{}, native, snapshotter)
	sessions := []core.Session{
		{ID: "first", Tool: core.ToolCodexCLI, Confidence: core.ConfidenceSupervised, JobName: "job-first", Workspace: "first-workspace", Root: core.ProcessIdentity{PID: 10, CreatedAt: created}, StartedAt: created},
		{
			ID:         "missing",
			Tool:       core.ToolClaudeCLI,
			Confidence: core.ConfidenceSupervised,
			JobName:    "job-missing",
			Workspace:  "missing-workspace",
			Root:       core.ProcessIdentity{PID: 99, CreatedAt: created.Add(-time.Minute)},
			StartedAt:  created.Add(-time.Minute),
			Processes:  []core.Process{{PID: 99, CPUAvailable: true, MemoryAvailable: true}},
			Resources:  core.ResourceUsage{CPUPercent: 5, CPUAvailable: true, MemoryBytes: 100, MemoryAvailable: true},
			State:      core.StateRunning,
		},
		{ID: "last", Tool: core.ToolCodexCLI, Confidence: core.ConfidenceSupervised, JobName: "job-last", Workspace: "last-workspace", Root: core.ProcessIdentity{PID: 20, CreatedAt: created.Add(time.Second)}, StartedAt: created.Add(time.Second)},
	}

	got, errs := supervisor.RefreshMany(sessions)

	if len(got) != len(sessions) || len(errs) != len(sessions) {
		t.Fatalf("result lengths = %d/%d, want %d/%d", len(got), len(errs), len(sessions), len(sessions))
	}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("errors[%d] = %v, want nil", i, err)
		}
		if got[i].ID != sessions[i].ID {
			t.Fatalf("results[%d].ID = %q, want %q", i, got[i].ID, sessions[i].ID)
		}
	}
	if got[0].State != core.StateRunning || got[0].Processes[0].PID != 10 {
		t.Fatalf("first result = %#v, want refreshed active session", got[0])
	}
	missing := sessions[1]
	missing.Processes = nil
	missing.Resources = core.ResourceUsage{}
	missing.State = core.StateExited
	if !reflect.DeepEqual(got[1], missing) {
		t.Fatalf("missing result = %#v, want %#v", got[1], missing)
	}
	if got[2].State != core.StateRunning || got[2].Processes[0].PID != 20 {
		t.Fatalf("last result = %#v, want refreshed active session", got[2])
	}
	if snapshotter.calls != 1 {
		t.Fatalf("snapshot calls = %d, want 1 shared snapshot for live jobs only", snapshotter.calls)
	}
}

func TestRefreshTreatsMissingJobAsExited(t *testing.T) {
	native := &fakeNative{openJobErrors: map[string]error{"job-missing": syscall.ERROR_FILE_NOT_FOUND}}
	snapshotter := &fakeSnapshotter{}
	supervisor := newSupervisor(&fakeResolver{}, native, snapshotter)
	session := core.Session{
		ID:         "missing",
		Tool:       core.ToolCodexCLI,
		Confidence: core.ConfidenceSupervised,
		JobName:    "job-missing",
		Workspace:  "workspace",
		Processes:  []core.Process{{PID: 10, CPUAvailable: true, MemoryAvailable: true}},
		Resources:  core.ResourceUsage{CPUPercent: 5, CPUAvailable: true, MemoryBytes: 100, MemoryAvailable: true},
		State:      core.StateRunning,
	}

	got, err := supervisor.Refresh(session)

	if err != nil {
		t.Fatalf("Refresh() error = %v, want nil", err)
	}
	want := session
	want.Processes = nil
	want.Resources = core.ResourceUsage{}
	want.State = core.StateExited
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Refresh() = %#v, want %#v", got, want)
	}
	if snapshotter.calls != 0 {
		t.Fatalf("snapshot calls = %d, want 0 for missing job", snapshotter.calls)
	}
}

func TestRefreshManyAlignsJobAndGlobalSnapshotErrors(t *testing.T) {
	native := &fakeNative{
		jobHandles:      map[string]nativeHandle{"job-a": 1, "job-b": 2, "job-c": 3},
		jobPIDsByHandle: map[nativeHandle][]uint32{1: {10}, 3: {30}},
		jobPIDErrors:    map[nativeHandle]error{2: errors.New("job query failed")},
	}
	snapshotter := &fakeSnapshotter{err: errors.New("snapshot failed")}
	supervisor := newSupervisor(&fakeResolver{}, native, snapshotter)
	sessions := []core.Session{{ID: "a", JobName: "job-a"}, {ID: "b", JobName: "job-b"}, {ID: "c", JobName: "job-c"}}

	got, errs := supervisor.RefreshMany(sessions)
	if len(got) != 3 || len(errs) != 3 {
		t.Fatalf("result lengths = %d/%d, want 3/3", len(got), len(errs))
	}
	if !reflect.DeepEqual(got, sessions) {
		t.Fatalf("results = %#v, want inputs preserved on errors", got)
	}
	if errs[0] == nil || errs[1] == nil || errs[2] == nil {
		t.Fatalf("errors = %v, want an aligned error at every index", errs)
	}
	if errs[1].Error() != "query job membership: job query failed" {
		t.Fatalf("errors[1] = %v, want per-job query error preserved", errs[1])
	}
	if errs[0].Error() != "snapshot processes: snapshot failed" || errs[2].Error() != "snapshot processes: snapshot failed" {
		t.Fatalf("snapshot errors = %v/%v, want global error at otherwise-refreshable indices", errs[0], errs[2])
	}
	if snapshotter.calls != 1 {
		t.Fatalf("snapshot calls = %d, want 1", snapshotter.calls)
	}
}

func TestRecoverManyOpensJobsAndUsesOneSharedSnapshot(t *testing.T) {
	created := time.Unix(100, 0).UTC()
	native := &fakeNative{
		jobHandles: map[string]nativeHandle{"job-active": 1, "job-leftovers": 2, "job-empty": 3},
		jobPIDsByHandle: map[nativeHandle][]uint32{
			1: {10},
			2: {21},
			3: {},
		},
	}
	snapshotter := &fakeSnapshotter{processes: []ProcessDetails{
		{Process: core.Process{PID: 10, CreatedAt: created}, MetadataAccessible: true},
		{Process: core.Process{PID: 21, CreatedAt: created.Add(time.Second)}, MetadataAccessible: true},
	}}
	supervisor := newSupervisor(&fakeResolver{}, native, snapshotter)
	metadata := []core.Session{
		{ID: "active", Confidence: core.ConfidenceSupervised, JobName: "job-active", Root: core.ProcessIdentity{PID: 10, CreatedAt: created}},
		{ID: "leftovers", Confidence: core.ConfidenceSupervised, JobName: "job-leftovers", Root: core.ProcessIdentity{PID: 20, CreatedAt: created}},
		{ID: "empty", Confidence: core.ConfidenceSupervised, JobName: "job-empty", Root: core.ProcessIdentity{PID: 30, CreatedAt: created}},
	}

	got, errs := supervisor.RecoverMany(metadata)
	if len(got) != len(metadata) || len(errs) != len(metadata) {
		t.Fatalf("result lengths = %d/%d, want %d/%d", len(got), len(errs), len(metadata), len(metadata))
	}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("errors[%d] = %v, want nil", i, err)
		}
		if got[i].ID != metadata[i].ID {
			t.Fatalf("results[%d].ID = %q, want %q", i, got[i].ID, metadata[i].ID)
		}
	}
	if got[0].State != core.StateRunning || got[1].State != core.StateLeftovers || got[2].State != core.StateExited {
		t.Fatalf("states = %q/%q/%q, want running/leftovers/exited", got[0].State, got[1].State, got[2].State)
	}
	if !reflect.DeepEqual(native.openedJobNames, []string{"job-active", "job-leftovers", "job-empty"}) {
		t.Fatalf("opened jobs = %v, want every valid named job in order", native.openedJobNames)
	}
	if snapshotter.calls != 1 {
		t.Fatalf("snapshot calls = %d, want 1", snapshotter.calls)
	}
}

func TestRecoverManyTreatsMissingJobAsExitedAndPreservesOtherErrors(t *testing.T) {
	native := &fakeNative{openJobErrors: map[string]error{
		"job-missing": syscall.ERROR_FILE_NOT_FOUND,
		"job-denied":  syscall.ERROR_ACCESS_DENIED,
	}}
	snapshotter := &fakeSnapshotter{}
	supervisor := newSupervisor(&fakeResolver{}, native, snapshotter)
	metadata := []core.Session{
		{
			ID:         "missing",
			Confidence: core.ConfidenceSupervised,
			JobName:    "job-missing",
			Processes:  []core.Process{{PID: 10}},
			Resources:  core.ResourceUsage{CPUPercent: 10, CPUAvailable: true, MemoryBytes: 100, MemoryAvailable: true},
			State:      core.StateRunning,
		},
		{ID: "denied", Confidence: core.ConfidenceSupervised, JobName: "job-denied"},
		{ID: "invalid", Confidence: core.ConfidenceObserved, JobName: "job-invalid"},
	}

	got, errs := supervisor.RecoverMany(metadata)
	if len(got) != 3 || len(errs) != 3 {
		t.Fatalf("result lengths = %d/%d, want 3/3", len(got), len(errs))
	}
	if errs[0] != nil {
		t.Fatalf("missing job error = %v, want nil", errs[0])
	}
	if got[0].ID != metadata[0].ID || got[0].State != core.StateExited || len(got[0].Processes) != 0 || got[0].Resources != (core.ResourceUsage{}) {
		t.Fatalf("missing job result = %#v, want cleared exited metadata", got[0])
	}
	if errs[1] == nil || !errors.Is(errs[1], syscall.ERROR_ACCESS_DENIED) {
		t.Fatalf("hard open error = %v, want aligned access-denied error", errs[1])
	}
	if !reflect.DeepEqual(got[1], core.Session{}) {
		t.Fatalf("hard open result = %#v, want zero session for wrapper compatibility", got[1])
	}
	if errs[2] == nil || errs[2].Error() != "invalid supervised session metadata" {
		t.Fatalf("validation error = %v, want aligned metadata error", errs[2])
	}
	if !reflect.DeepEqual(got[2], core.Session{}) {
		t.Fatalf("invalid metadata result = %#v, want zero session for wrapper compatibility", got[2])
	}
	if snapshotter.calls != 0 {
		t.Fatalf("snapshot calls = %d, want 0 without refreshable jobs", snapshotter.calls)
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

func TestRefreshKeepsIdentitySafeJobMembersAccessibleWhenOptionalMetadataFails(t *testing.T) {
	created := time.Unix(100, 0).UTC()
	snapshotter := &fakeSnapshotter{processes: []ProcessDetails{{
		Process:            core.Process{PID: 42, ParentPID: 1, CreatedAt: created},
		MetadataAccessible: true,
		MetadataError:      "path: access denied; command line: access denied; memory: access denied",
	}}}
	native := &fakeNative{jobPIDs: []uint32{42}}
	supervisor := newSupervisor(&fakeResolver{}, native, snapshotter)
	session := core.Session{ID: "s1", Confidence: core.ConfidenceSupervised, JobName: "TaskWatch-test", Root: core.ProcessIdentity{PID: 42, CreatedAt: created}}

	got, err := supervisor.Refresh(session)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if got.State != core.StateRunning {
		t.Fatalf("Refresh() state = %q, want running", got.State)
	}
}

func TestTerminateObservedPreflightsAllTargetsBeforeLeafFirstKill(t *testing.T) {
	created := time.Unix(100, 0).UTC()
	current := []ProcessDetails{
		{Process: core.Process{PID: 10, ParentPID: 1, CreatedAt: created}, MetadataAccessible: true},
		{Process: core.Process{PID: 11, ParentPID: 10, CreatedAt: created.Add(time.Second)}, MetadataAccessible: true},
		{Process: core.Process{PID: 12, ParentPID: 11, CreatedAt: created.Add(2 * time.Second)}, MetadataAccessible: true},
	}
	native := &fakeNative{createdByPID: map[uint32]time.Time{
		10: created,
		11: created.Add(time.Second),
		12: created.Add(2 * time.Second),
	}}
	supervisor := newSupervisor(&fakeResolver{}, native, &fakeSnapshotter{processes: current})
	request := core.NewTerminationRequest("observed-10", detailsProcesses(current)).Confirm()

	if err := supervisor.TerminateObserved(request, observedSession("observed-10", current[0].Process)); err != nil {
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
		{Process: core.Process{PID: 10, ParentPID: 1, CreatedAt: created}, MetadataAccessible: true},
		{Process: core.Process{PID: 11, ParentPID: 10, CreatedAt: created.Add(time.Second)}, MetadataAccessible: true},
	}
	native := &fakeNative{failOpenPID: 11, createdByPID: map[uint32]time.Time{10: created}}
	supervisor := newSupervisor(&fakeResolver{}, native, &fakeSnapshotter{processes: current})
	request := core.NewTerminationRequest("observed-10", detailsProcesses(current)).Confirm()

	if err := supervisor.TerminateObserved(request, observedSession("observed-10", current[0].Process)); err == nil {
		t.Fatal("TerminateObserved() accepted failed preflight")
	}
	if len(native.terminatedPIDs) != 0 {
		t.Fatalf("terminated PIDs = %v, want zero", native.terminatedPIDs)
	}
}

func TestTerminateObservedReusedPIDDuringPreflightKillsNothing(t *testing.T) {
	created := time.Unix(100, 0).UTC()
	current := []ProcessDetails{
		{Process: core.Process{PID: 10, ParentPID: 1, CreatedAt: created}, MetadataAccessible: true},
		{Process: core.Process{PID: 11, ParentPID: 10, CreatedAt: created.Add(time.Second)}, MetadataAccessible: true},
	}
	native := &fakeNative{createdByPID: map[uint32]time.Time{
		10: created,
		11: created.Add(time.Hour),
	}}
	supervisor := newSupervisor(&fakeResolver{}, native, &fakeSnapshotter{processes: current})
	request := core.NewTerminationRequest("observed-10", detailsProcesses(current)).Confirm()

	if err := supervisor.TerminateObserved(request, observedSession("observed-10", current[0].Process)); err == nil {
		t.Fatal("TerminateObserved() accepted a PID reused after the snapshot")
	}
	if len(native.terminatedPIDs) != 0 {
		t.Fatalf("terminated PIDs = %v, want zero", native.terminatedPIDs)
	}
}

func TestTerminateObservedDoesNotDependOnConfirmationOrder(t *testing.T) {
	created := time.Unix(100, 0).UTC()
	current := []ProcessDetails{
		{Process: core.Process{PID: 10, ParentPID: 1, CreatedAt: created}, MetadataAccessible: true},
		{Process: core.Process{PID: 11, ParentPID: 10, CreatedAt: created.Add(time.Second)}, MetadataAccessible: true},
	}
	native := &fakeNative{createdByPID: map[uint32]time.Time{
		10: created,
		11: created.Add(time.Second),
	}}
	supervisor := newSupervisor(&fakeResolver{}, native, &fakeSnapshotter{processes: current})
	request := core.NewTerminationRequest("observed-10", []core.Process{current[1].Process, current[0].Process}).Confirm()

	if err := supervisor.TerminateObserved(request, observedSession("observed-10", current[0].Process)); err != nil {
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

	if err := supervisor.TerminateObserved(request, observedSession("observed-10", current[0].Process)); err == nil {
		t.Fatal("TerminateObserved() accepted an older process as the root's descendant")
	}
	if len(native.openedForTerminate) != 0 || len(native.terminatedPIDs) != 0 {
		t.Fatalf("opened/terminated = %v/%v, want zero", native.openedForTerminate, native.terminatedPIDs)
	}
}

func TestTerminateObservedRejectsConfirmationForDifferentSession(t *testing.T) {
	created := time.Unix(100, 0).UTC()
	current := []ProcessDetails{{
		Process:            core.Process{PID: 10, ParentPID: 1, CreatedAt: created},
		MetadataAccessible: true,
	}}
	snapshotter := &fakeSnapshotter{processes: current}
	native := &fakeNative{createdByPID: map[uint32]time.Time{10: created}}
	supervisor := newSupervisor(&fakeResolver{}, native, snapshotter)
	request := core.NewTerminationRequest("different-session", detailsProcesses(current)).Confirm()

	if err := supervisor.TerminateObserved(request, observedSession("observed-10", current[0].Process)); err == nil {
		t.Fatal("TerminateObserved() accepted confirmation for a different session")
	}
	if snapshotter.calls != 0 || len(native.openedForTerminate) != 0 || len(native.terminatedPIDs) != 0 {
		t.Fatalf("snapshot/opened/terminated = %d/%v/%v, want no work", snapshotter.calls, native.openedForTerminate, native.terminatedPIDs)
	}
}

func TestTerminateObservedRejectsFreshInaccessibleMetadata(t *testing.T) {
	created := time.Unix(100, 0).UTC()
	tests := []struct {
		name   string
		detail ProcessDetails
	}{
		{
			name:   "unreadable process",
			detail: ProcessDetails{Process: core.Process{PID: 10, ParentPID: 1, CreatedAt: created}, MetadataError: "access denied"},
		},
		{
			name:   "metadata probe failed",
			detail: ProcessDetails{Process: core.Process{PID: 10, ParentPID: 1, CreatedAt: created}, MetadataAccessible: true, MetadataError: "command line: access denied"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshotter := &fakeSnapshotter{processes: []ProcessDetails{test.detail}}
			native := &fakeNative{createdByPID: map[uint32]time.Time{10: created}}
			supervisor := newSupervisor(&fakeResolver{}, native, snapshotter)
			request := core.NewTerminationRequest("observed-10", []core.Process{test.detail.Process}).Confirm()

			if err := supervisor.TerminateObserved(request, observedSession("observed-10", test.detail.Process)); err == nil {
				t.Fatal("TerminateObserved() accepted inaccessible metadata")
			}
			if len(native.openedForTerminate) != 0 || len(native.terminatedPIDs) != 0 {
				t.Fatalf("opened/terminated = %v/%v, want zero", native.openedForTerminate, native.terminatedPIDs)
			}
		})
	}
}

func TestTerminateObservedRejectsFreshIdentitylessDescendant(t *testing.T) {
	created := time.Unix(100, 0).UTC()
	root := ProcessDetails{Process: core.Process{PID: 10, ParentPID: 1, CreatedAt: created}, MetadataAccessible: true}
	current := []ProcessDetails{
		root,
		{Process: core.Process{PID: 11, ParentPID: 10}, MetadataError: "creation time unavailable"},
	}
	native := &fakeNative{createdByPID: map[uint32]time.Time{10: created}}
	supervisor := newSupervisor(&fakeResolver{}, native, &fakeSnapshotter{processes: current})
	request := core.NewTerminationRequest("observed-10", []core.Process{root.Process}).Confirm()

	if err := supervisor.TerminateObserved(request, observedSession("observed-10", root.Process)); err == nil {
		t.Fatal("TerminateObserved() accepted a fresh identity-less descendant")
	}
	if len(native.openedForTerminate) != 0 || len(native.terminatedPIDs) != 0 {
		t.Fatalf("opened/terminated = %v/%v, want zero", native.openedForTerminate, native.terminatedPIDs)
	}
}

func TestTerminateObservedRejectsNonObservedSession(t *testing.T) {
	created := time.Unix(100, 0).UTC()
	root := core.Process{PID: 10, ParentPID: 1, CreatedAt: created}
	snapshotter := &fakeSnapshotter{}
	native := &fakeNative{}
	supervisor := newSupervisor(&fakeResolver{}, native, snapshotter)
	request := core.NewTerminationRequest("session-10", []core.Process{root}).Confirm()
	session := core.Session{ID: "session-10", Confidence: core.ConfidenceSupervised, Root: root.Identity()}

	if err := supervisor.TerminateObserved(request, session); err == nil {
		t.Fatal("TerminateObserved() accepted a non-observed session")
	}
	if snapshotter.calls != 0 {
		t.Fatalf("snapshot calls = %d, want zero", snapshotter.calls)
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

func TestProcessSnapshotterStoresResourceAvailabilityOnProcess(t *testing.T) {
	snapshotter := NewProcessSnapshotter()
	entry := winapi.ProcessEntry32{ProcessID: uint32(os.Getpid())}
	first := snapshotter.detailsForEntry(entry, time.Unix(100, 0).UTC())
	if first.Process.CPUAvailable {
		t.Fatal("first CPU sample is available, want unavailable")
	}
	if !first.Process.MemoryAvailable {
		t.Fatal("current process memory is unavailable, want available")
	}

	second := snapshotter.detailsForEntry(entry, time.Unix(101, 0).UTC())
	if !second.Process.CPUAvailable || !second.Process.MemoryAvailable {
		t.Fatalf("second availability = CPU %t, memory %t; want both available", second.Process.CPUAvailable, second.Process.MemoryAvailable)
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
