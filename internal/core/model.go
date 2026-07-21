package core

import "time"

// Tool identifies the TaskWatch-supported agent application.
type Tool string

const (
	ToolClaudeCLI    Tool = "claude-cli"
	ToolCodexCLI     Tool = "codex-cli"
	ToolCodexDesktop Tool = "codex-desktop"
)

// Confidence records how TaskWatch learned about a session.
type Confidence string

const (
	ConfidenceSupervised Confidence = "supervised"
	ConfidenceObserved   Confidence = "observed"
)

// State is the derived lifecycle state of a session.
type State string

const (
	StateRunning      State = "running"
	StateExited       State = "exited"
	StateLeftovers    State = "leftovers"
	StateInaccessible State = "inaccessible"
	StateStale        State = "stale"
)

// ActivityState is semantic Codex activity derived from an ephemeral rollout.
type ActivityState string

const (
	ActivityUnknown   ActivityState = "unknown"
	ActivityThinking  ActivityState = "thinking"
	ActivityExecuting ActivityState = "executing"
	ActivityWaiting   ActivityState = "waiting"
)

// ActivityCorrelation describes how activity was associated with a process session.
type ActivityCorrelation string

const (
	CorrelationDirect      ActivityCorrelation = "direct"
	CorrelationApplication ActivityCorrelation = "app-level"
	CorrelationInferred    ActivityCorrelation = "inferred"
)

// AgentActivity is a redacted, ephemeral projection of one Codex rollout.
type AgentActivity struct {
	ThreadID      string
	Tool          Tool
	Workspace     string
	Model         string
	LastTool      string
	State         ActivityState
	Correlation   ActivityCorrelation
	StartedAt     time.Time
	UpdatedAt     time.Time
	ContextTokens int64
	ContextWindow int64
}

// ActivityMatchHint contains only explicit process arguments useful for correlation.
type ActivityMatchHint struct {
	ThreadID  string
	Workspace string
}

// ProcessIdentity protects process operations from PID reuse.
type ProcessIdentity struct {
	PID       uint32
	CreatedAt time.Time
}

// Process is a point-in-time process snapshot.
type Process struct {
	PID             uint32
	ParentPID       uint32
	Name            string
	CommandLine     string
	CreatedAt       time.Time
	CPUPercent      float64
	MemoryBytes     uint64
	CPUAvailable    bool
	MemoryAvailable bool
}

func (p Process) Identity() ProcessIdentity {
	return ProcessIdentity{PID: p.PID, CreatedAt: p.CreatedAt}
}

// ResourceUsage is the aggregate resource use of a process collection.
type ResourceUsage struct {
	CPUPercent      float64
	MemoryBytes     uint64
	CPUAvailable    bool
	MemoryAvailable bool
}

// Session is the in-memory representation of a tracked tool session.
type Session struct {
	ID                string
	Tool              Tool
	Confidence        Confidence
	JobName           string
	Workspace         string
	Root              ProcessIdentity
	StartedAt         time.Time
	Processes         []Process
	State             State
	Resources         ResourceUsage
	Activities        []AgentActivity
	ActivityMatchHint ActivityMatchHint
}

// DeriveState derives a session state from its current job process snapshot.
func DeriveState(root ProcessIdentity, processes []Process, inaccessible bool) State {
	if inaccessible {
		return StateInaccessible
	}
	if len(processes) == 0 {
		return StateExited
	}
	for _, process := range processes {
		if SameProcess(root, process.Identity()) {
			return StateRunning
		}
	}
	return StateLeftovers
}

// AggregateResources sums the resource use of all supplied processes.
func AggregateResources(processes []Process) ResourceUsage {
	total := ResourceUsage{CPUAvailable: len(processes) > 0, MemoryAvailable: len(processes) > 0}
	for _, process := range processes {
		total.CPUPercent += process.CPUPercent
		total.MemoryBytes += process.MemoryBytes
		total.CPUAvailable = total.CPUAvailable && process.CPUAvailable
		total.MemoryAvailable = total.MemoryAvailable && process.MemoryAvailable
	}
	return total
}

// SameProcess reports whether two identities refer to the same PID incarnation.
func SameProcess(left, right ProcessIdentity) bool {
	return left.PID != 0 &&
		left.PID == right.PID &&
		!left.CreatedAt.IsZero() &&
		!right.CreatedAt.IsZero() &&
		left.CreatedAt.Equal(right.CreatedAt)
}
