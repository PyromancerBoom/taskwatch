package windows

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/PyromancerBoom/taskwatch/internal/core"
)

// Observer detects Codex desktop trees from process snapshots.
type Observer struct {
	snapshotter Snapshotter
}

func NewObserver(snapshotter Snapshotter) *Observer {
	if snapshotter == nil {
		snapshotter = NewProcessSnapshotter()
	}
	return &Observer{snapshotter: snapshotter}
}

func (observer *Observer) Observe(supervisedProcesses map[uint32]core.ProcessIdentity) ([]core.Session, error) {
	snapshot, err := observer.snapshotter.Snapshot()
	if err != nil {
		return nil, err
	}
	return GroupCodexDesktop(snapshot, supervisedProcesses), nil
}

// GroupCodexDesktop is the pure grouping step used by Observer. A Codex.exe
// nested below another Codex.exe belongs to the outer tree and is not emitted
// as a duplicate root.
func GroupCodexDesktop(snapshot []ProcessDetails, supervisedProcesses map[uint32]core.ProcessIdentity) []core.Session {
	processes := make([]core.Process, 0, len(snapshot))
	detailsByPID := make(map[uint32]ProcessDetails, len(snapshot))
	for _, detail := range snapshot {
		if supervised, exists := supervisedProcesses[detail.Process.PID]; exists && core.SameProcess(supervised, detail.Process.Identity()) {
			continue
		}
		processes = append(processes, detail.Process)
		detailsByPID[detail.Process.PID] = detail
	}

	rootCandidates := make([]core.Process, 0)
	for _, process := range processes {
		if !isCodexDesktop(detailsByPID[process.PID]) || hasCodexAncestor(process, detailsByPID) {
			continue
		}
		rootCandidates = append(rootCandidates, process)
	}
	sort.Slice(rootCandidates, func(i, j int) bool { return rootCandidates[i].PID < rootCandidates[j].PID })

	sessions := make([]core.Session, 0, len(rootCandidates))
	for _, root := range rootCandidates {
		tree := core.CollectTree(processes, root.Identity())
		state := core.StateRunning
		if observedTreeInaccessible(root.PID, detailsByPID, tree) {
			state = core.StateInaccessible
		}
		sessions = append(sessions, core.Session{
			ID:         fmt.Sprintf("observed-codex-%d-%d", root.PID, root.CreatedAt.UnixNano()),
			Tool:       core.ToolCodexDesktop,
			Confidence: core.ConfidenceObserved,
			Root:       root.Identity(),
			StartedAt:  root.CreatedAt,
			Processes:  tree,
			State:      state,
			Resources:  core.AggregateResources(tree),
		})
	}
	return sessions
}

func observedTreeInaccessible(rootPID uint32, detailsByPID map[uint32]ProcessDetails, safeTree []core.Process) bool {
	safe := make(map[uint32]struct{}, len(safeTree))
	for _, process := range safeTree {
		safe[process.PID] = struct{}{}
	}
	queue := []uint32{rootPID}
	seen := make(map[uint32]struct{}, len(detailsByPID))
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if _, visited := seen[pid]; visited {
			continue
		}
		seen[pid] = struct{}{}
		detail, exists := detailsByPID[pid]
		if !exists {
			continue
		}
		if !detail.MetadataAccessible || detail.Process.CreatedAt.IsZero() {
			return true
		}
		if _, validated := safe[pid]; !validated {
			return true
		}
		for childPID, child := range detailsByPID {
			if child.Process.ParentPID != detail.Process.PID {
				continue
			}
			if !child.Process.CreatedAt.IsZero() && child.Process.CreatedAt.Before(detail.Process.CreatedAt) {
				continue
			}
			if child.Process.CreatedAt.IsZero() {
				return true
			}
			if validObservedEdge(detail.Process, child.Process) {
				queue = append(queue, childPID)
			}
		}
	}
	return false
}

func isCodexDesktop(detail ProcessDetails) bool {
	if !strings.EqualFold(filepath.Base(detail.Process.Name), "Codex.exe") || detail.ExecutablePath == "" {
		return false
	}
	path := strings.ToLower(strings.ReplaceAll(filepath.Clean(detail.ExecutablePath), "/", `\`))
	const packageRoot = `:\program files\windowsapps\openai.codex_`
	rootIndex := strings.Index(path, packageRoot)
	if rootIndex != 1 {
		return false
	}
	packageStart := rootIndex + len(packageRoot)
	packageEndOffset := strings.IndexByte(path[packageStart:], '\\')
	if packageEndOffset == -1 {
		return false
	}
	packageEnd := packageStart + packageEndOffset
	packageID := path[packageStart:packageEnd]
	if !strings.HasSuffix(packageID, "__2p2nqsd0c76g0") {
		return false
	}
	return path[packageEnd:] == `\app\resources\codex.exe`
}

func hasCodexAncestor(process core.Process, detailsByPID map[uint32]ProcessDetails) bool {
	seen := map[uint32]struct{}{process.PID: {}}
	child := process
	parentPID := process.ParentPID
	for parentPID != 0 {
		if _, cycle := seen[parentPID]; cycle {
			return false
		}
		seen[parentPID] = struct{}{}
		parentDetail, exists := detailsByPID[parentPID]
		if !exists {
			return false
		}
		parent := parentDetail.Process
		if !validObservedEdge(parent, child) {
			return false
		}
		if isCodexDesktop(parentDetail) {
			return true
		}
		child = parent
		parentPID = parent.ParentPID
	}
	return false
}

// validObservedEdge applies the same process-identity ordering invariant as
// core.CollectTree: a child must have an identity and cannot predate its
// alleged parent. This prevents reused parent PIDs from creating false trees.
func validObservedEdge(parent, child core.Process) bool {
	return child.ParentPID == parent.PID &&
		!parent.CreatedAt.IsZero() &&
		!child.CreatedAt.IsZero() &&
		!child.CreatedAt.Before(parent.CreatedAt)
}
