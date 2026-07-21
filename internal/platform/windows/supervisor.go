package windows

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"sync"
	"syscall"
	"time"

	"taskwatch/internal/core"
)

type nativeHandle uintptr

type pathResolver interface {
	LookPath(string) (string, error)
}

type execResolver struct{}

func (execResolver) LookPath(name string) (string, error) { return exec.LookPath(name) }

type nativeOperations interface {
	createJob(name string) (nativeHandle, error)
	openJob(name string) (nativeHandle, error)
	createSuspended(executable, workspace string) (process, thread nativeHandle, pid uint32, err error)
	assignProcess(job, process nativeHandle) error
	startJobKeeper(job nativeHandle) error
	resumeThread(thread nativeHandle) error
	processCreatedAt(process nativeHandle) (time.Time, error)
	terminateProcess(process nativeHandle) error
	closeHandle(nativeHandle)
	jobProcessIDs(job nativeHandle) ([]uint32, error)
	openProcessForTerminate(pid uint32) (nativeHandle, error)
	terminateOpenedProcess(process nativeHandle) error
}

// Supervisor owns live handles to TaskWatch-created jobs. Closing Supervisor
// does not kill job members because TaskWatch never enables kill-on-close.
type Supervisor struct {
	resolver    pathResolver
	native      nativeOperations
	snapshotter Snapshotter

	mu      sync.Mutex
	handles map[string]nativeHandle
}

func NewSupervisor() *Supervisor {
	return newSupervisor(execResolver{}, nativeWindows{}, NewProcessSnapshotter())
}

func newSupervisor(resolver pathResolver, native nativeOperations, snapshotter Snapshotter) *Supervisor {
	return &Supervisor{
		resolver:    resolver,
		native:      native,
		snapshotter: snapshotter,
		handles:     make(map[string]nativeHandle),
	}
}

func (supervisor *Supervisor) Launch(tool core.Tool, workspace string) (core.Session, error) {
	command, err := commandForTool(tool)
	if err != nil {
		return core.Session{}, err
	}
	executable, err := supervisor.resolver.LookPath(command)
	if err != nil {
		return core.Session{}, fmt.Errorf("resolve %s from PATH: %w", command, err)
	}

	id, jobName, err := newSessionNames()
	if err != nil {
		return core.Session{}, err
	}
	job, err := supervisor.native.createJob(jobName)
	if err != nil {
		return core.Session{}, fmt.Errorf("create job object: %w", err)
	}
	keepJob := false
	defer func() {
		if !keepJob {
			supervisor.native.closeHandle(job)
		}
	}()

	process, thread, pid, err := supervisor.native.createSuspended(executable, workspace)
	if err != nil {
		return core.Session{}, fmt.Errorf("create suspended %s process: %w", command, err)
	}
	resumed := false
	defer func() {
		if !resumed {
			_ = supervisor.native.terminateProcess(process)
		}
		supervisor.native.closeHandle(thread)
		supervisor.native.closeHandle(process)
	}()

	createdAt, err := supervisor.native.processCreatedAt(process)
	if err != nil {
		return core.Session{}, fmt.Errorf("read root process creation time: %w", err)
	}
	if err := supervisor.native.assignProcess(job, process); err != nil {
		return core.Session{}, fmt.Errorf("assign suspended process to job: %w", err)
	}
	if err := supervisor.native.startJobKeeper(job); err != nil {
		return core.Session{}, fmt.Errorf("start job keeper: %w", err)
	}
	if err := supervisor.native.resumeThread(thread); err != nil {
		return core.Session{}, fmt.Errorf("resume supervised process: %w", err)
	}
	resumed = true
	keepJob = true

	supervisor.mu.Lock()
	supervisor.handles[jobName] = job
	supervisor.mu.Unlock()

	root := core.Process{PID: pid, Name: command, CreatedAt: createdAt}
	return core.Session{
		ID:         id,
		Tool:       tool,
		Confidence: core.ConfidenceSupervised,
		JobName:    jobName,
		Workspace:  workspace,
		Root:       root.Identity(),
		StartedAt:  createdAt,
		Processes:  []core.Process{root},
		State:      core.StateRunning,
	}, nil
}

func commandForTool(tool core.Tool) (string, error) {
	switch tool {
	case core.ToolClaudeCLI:
		return "claude", nil
	case core.ToolCodexCLI:
		return "codex", nil
	default:
		return "", fmt.Errorf("tool %q cannot be supervised", tool)
	}
}

func newSessionNames() (string, string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", "", fmt.Errorf("generate session ID: %w", err)
	}
	id := hex.EncodeToString(random[:])
	return id, `Local\TaskWatch-` + id, nil
}

// Recover reopens a persisted named job and refreshes its current membership.
func (supervisor *Supervisor) Recover(metadata core.Session) (core.Session, error) {
	results, errs := supervisor.RecoverMany([]core.Session{metadata})
	return results[0], errs[0]
}

// RecoverMany reopens valid persisted named jobs and refreshes all successfully
// opened jobs together.
func (supervisor *Supervisor) RecoverMany(metadata []core.Session) ([]core.Session, []error) {
	results := make([]core.Session, len(metadata))
	errs := make([]error, len(metadata))
	opened := make([]core.Session, 0, len(metadata))
	indices := make([]int, 0, len(metadata))
	for i := range metadata {
		if metadata[i].JobName == "" || metadata[i].Confidence != core.ConfidenceSupervised {
			errs[i] = errors.New("invalid supervised session metadata")
			continue
		}
		if _, err := supervisor.jobHandle(metadata[i].JobName); err != nil {
			if errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) {
				exited := metadata[i]
				exited.Processes = nil
				exited.Resources = core.ResourceUsage{}
				exited.State = core.StateExited
				results[i] = exited
				continue
			}
			errs[i] = fmt.Errorf("open job %q: %w", metadata[i].JobName, err)
			continue
		}
		opened = append(opened, metadata[i])
		indices = append(indices, i)
	}
	if len(opened) == 0 {
		return results, errs
	}
	refreshed, refreshErrs := supervisor.RefreshMany(opened)
	for i, index := range indices {
		results[index] = refreshed[i]
		errs[index] = refreshErrs[i]
	}
	return results, errs
}

// Refresh queries live job membership and joins it to a best-effort process
// snapshot. Missing process metadata makes the session inaccessible, not empty.
func (supervisor *Supervisor) Refresh(session core.Session) (core.Session, error) {
	results, errs := supervisor.RefreshMany([]core.Session{session})
	return results[0], errs[0]
}

type refreshCandidate struct {
	index int
	pids  []uint32
}

// RefreshMany refreshes each supplied session in input order. Job membership
// is queried per session, while all successful queries share one snapshot.
func (supervisor *Supervisor) RefreshMany(sessions []core.Session) ([]core.Session, []error) {
	results := append([]core.Session(nil), sessions...)
	errs := make([]error, len(sessions))
	candidates := make([]refreshCandidate, 0, len(sessions))
	for i := range sessions {
		job, err := supervisor.jobHandle(sessions[i].JobName)
		if err != nil {
			if errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) {
				exited := sessions[i]
				exited.Processes = nil
				exited.Resources = core.ResourceUsage{}
				exited.State = core.StateExited
				results[i] = exited
				continue
			}
			errs[i] = err
			continue
		}
		pids, err := supervisor.native.jobProcessIDs(job)
		if err != nil {
			errs[i] = fmt.Errorf("query job membership: %w", err)
			continue
		}
		candidates = append(candidates, refreshCandidate{index: i, pids: pids})
	}
	if len(candidates) == 0 {
		return results, errs
	}
	snapshot, err := supervisor.snapshotter.Snapshot()
	if err != nil {
		for _, candidate := range candidates {
			errs[candidate.index] = fmt.Errorf("snapshot processes: %w", err)
		}
		return results, errs
	}
	for _, candidate := range candidates {
		results[candidate.index] = refreshSession(sessions[candidate.index], candidate.pids, snapshot)
	}
	return results, errs
}

func refreshSession(session core.Session, pids []uint32, snapshot []ProcessDetails) core.Session {
	wanted := make(map[uint32]struct{}, len(pids))
	for _, pid := range pids {
		wanted[pid] = struct{}{}
	}
	processes := make([]core.Process, 0, len(pids))
	inaccessible := false
	for _, detail := range snapshot {
		if _, exists := wanted[detail.Process.PID]; exists {
			processes = append(processes, detail.Process)
			if !detail.MetadataAccessible || detail.Process.CreatedAt.IsZero() {
				inaccessible = true
			}
		}
	}
	sort.Slice(processes, func(i, j int) bool { return processes[i].PID < processes[j].PID })
	inaccessible = inaccessible || len(processes) != len(wanted)
	session.Processes = processes
	session.State = core.DeriveState(session.Root, processes, inaccessible)
	session.Resources = core.AggregateResources(processes)
	return session
}

func (supervisor *Supervisor) jobHandle(name string) (nativeHandle, error) {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	if handle, exists := supervisor.handles[name]; exists {
		return handle, nil
	}
	handle, err := supervisor.native.openJob(name)
	if err != nil {
		return 0, err
	}
	supervisor.handles[name] = handle
	return handle, nil
}

// TerminateSupervised terminates only the exact job members captured by an
// explicitly confirmed request. Members that appear later are left running
// for another review.
func (supervisor *Supervisor) TerminateSupervised(request core.TerminationRequest, session core.Session) error {
	if !request.Confirmed() {
		return errors.New("termination request is not confirmed")
	}
	if len(request.Targets()) == 0 {
		return errors.New("termination request has no targets")
	}
	if request.SessionID() == "" || request.SessionID() != session.ID {
		return errors.New("termination request does not belong to session")
	}
	if session.Confidence != core.ConfidenceSupervised || session.JobName == "" {
		return errors.New("invalid supervised session")
	}
	current, err := supervisor.Refresh(session)
	if err != nil {
		return fmt.Errorf("refresh supervised session for termination: %w", err)
	}
	if current.State == core.StateInaccessible {
		return errors.New("current supervised session membership is inaccessible")
	}
	if err := core.RevalidateObserved(request, current.Processes); err != nil {
		return fmt.Errorf("revalidate supervised session targets: %w", err)
	}
	return supervisor.terminateExactProcesses(current.Processes)
}

type openedProcess struct {
	process core.Process
	handle  nativeHandle
}

func (supervisor *Supervisor) terminateExactProcesses(processes []core.Process) error {
	opened := make([]openedProcess, 0, len(processes))
	for _, process := range processes {
		handle, err := supervisor.native.openProcessForTerminate(process.PID)
		if err != nil {
			supervisor.closeOpenedProcesses(opened)
			return fmt.Errorf("preflight PID %d: %w", process.PID, err)
		}
		opened = append(opened, openedProcess{process: process, handle: handle})
		createdAt, err := supervisor.native.processCreatedAt(handle)
		if err != nil {
			supervisor.closeOpenedProcesses(opened)
			return fmt.Errorf("preflight identity for PID %d: %w", process.PID, err)
		}
		if !core.SameProcess(process.Identity(), core.ProcessIdentity{PID: process.PID, CreatedAt: createdAt}) {
			supervisor.closeOpenedProcesses(opened)
			return fmt.Errorf("preflight identity for PID %d changed", process.PID)
		}
	}
	defer supervisor.closeOpenedProcesses(opened)

	byPID := make(map[uint32]core.Process, len(processes))
	for _, process := range processes {
		byPID[process.PID] = process
	}
	sort.SliceStable(opened, func(i, j int) bool {
		leftDepth := processDepth(opened[i].process, byPID)
		rightDepth := processDepth(opened[j].process, byPID)
		if leftDepth != rightDepth {
			return leftDepth > rightDepth
		}
		return opened[i].process.PID > opened[j].process.PID
	})
	for _, target := range opened {
		if err := supervisor.native.terminateOpenedProcess(target.handle); err != nil {
			return fmt.Errorf("terminate supervised PID %d: %w", target.process.PID, err)
		}
	}
	return nil
}

func (supervisor *Supervisor) closeOpenedProcesses(processes []openedProcess) {
	for _, process := range processes {
		supervisor.native.closeHandle(process.handle)
	}
}

func processDepth(process core.Process, byPID map[uint32]core.Process) int {
	depth := 0
	seen := map[uint32]struct{}{process.PID: {}}
	child := process
	for {
		parent, exists := byPID[child.ParentPID]
		if !exists || !validObservedEdge(parent, child) {
			return depth
		}
		if _, cycle := seen[parent.PID]; cycle {
			return depth
		}
		seen[parent.PID] = struct{}{}
		depth++
		child = parent
	}
}

// TerminateObserved re-snapshots the tree, checks exact PID+creation identity,
// pre-opens every target, then terminates leaf-first. A failed preflight kills
// nothing.
func (supervisor *Supervisor) TerminateObserved(request core.TerminationRequest, session core.Session) error {
	if !request.Confirmed() {
		return errors.New("termination request is not confirmed")
	}
	if len(request.Targets()) == 0 {
		return errors.New("termination request has no targets")
	}
	if request.SessionID() == "" || request.SessionID() != session.ID {
		return errors.New("termination request does not belong to session")
	}
	if session.Confidence != core.ConfidenceObserved || session.Root.PID == 0 || session.Root.CreatedAt.IsZero() {
		return errors.New("invalid observed session")
	}
	snapshot, err := supervisor.snapshotter.Snapshot()
	if err != nil {
		return fmt.Errorf("snapshot observed tree: %w", err)
	}
	detailsByPID := make(map[uint32]ProcessDetails, len(snapshot))
	for _, detail := range snapshot {
		detailsByPID[detail.Process.PID] = detail
	}
	allProcesses := detailsProcesses(snapshot)
	current := core.CollectTree(allProcesses, session.Root)
	if observedTreeInaccessible(session.Root.PID, detailsByPID, current) {
		return errors.New("current observed session metadata is inaccessible")
	}
	for _, process := range current {
		if detailsByPID[process.PID].MetadataError != "" {
			return errors.New("current observed session metadata is inaccessible")
		}
	}
	if err := core.RevalidateObserved(request, current); err != nil {
		return fmt.Errorf("revalidate observed tree: %w", err)
	}
	return supervisor.terminateExactProcesses(current)
}

// Close releases TaskWatch's job handles without terminating their processes.
func (supervisor *Supervisor) Close() {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	for name, handle := range supervisor.handles {
		supervisor.native.closeHandle(handle)
		delete(supervisor.handles, name)
	}
}
