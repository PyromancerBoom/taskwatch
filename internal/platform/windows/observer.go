package windows

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	winapi "golang.org/x/sys/windows"

	"github.com/PyromancerBoom/taskwatch/internal/core"
)

// Observer detects Codex desktop and external CLI trees from process snapshots.
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
	return GroupCodexAgents(snapshot, supervisedProcesses), nil
}

// GroupCodexDesktop is the pure grouping step used by Observer. A Codex.exe
// nested below another Codex.exe belongs to the outer tree and is not emitted
// as a duplicate root.
func GroupCodexDesktop(snapshot []ProcessDetails, supervisedProcesses map[uint32]core.ProcessIdentity) []core.Session {
	return groupCodex(snapshot, supervisedProcesses, false)
}

// GroupCodexAgents groups packaged Desktop app servers and external Codex CLI
// agent invocations while leaving supervised process identities excluded.
func GroupCodexAgents(snapshot []ProcessDetails, supervisedProcesses map[uint32]core.ProcessIdentity) []core.Session {
	return groupCodex(snapshot, supervisedProcesses, true)
}

type codexCandidate struct {
	process core.Process
	tool    core.Tool
	hint    core.ActivityMatchHint
}

func groupCodex(snapshot []ProcessDetails, supervisedProcesses map[uint32]core.ProcessIdentity, includeCLI bool) []core.Session {
	processes := make([]core.Process, 0, len(snapshot))
	detailsByPID := make(map[uint32]ProcessDetails, len(snapshot))
	for _, detail := range snapshot {
		if supervised, exists := supervisedProcesses[detail.Process.PID]; exists && core.SameProcess(supervised, detail.Process.Identity()) {
			continue
		}
		processes = append(processes, detail.Process)
		detailsByPID[detail.Process.PID] = detail
	}

	candidatesByPID := make(map[uint32]codexCandidate)
	for _, process := range processes {
		detail := detailsByPID[process.PID]
		if includeCLI {
			if tool, hint, ok := classifyCodexInvocation(detail); ok {
				candidatesByPID[process.PID] = codexCandidate{process: process, tool: tool, hint: hint}
			}
		} else if isCodexDesktop(detail) {
			candidatesByPID[process.PID] = codexCandidate{process: process, tool: core.ToolCodexDesktop}
		}
	}
	rootCandidates := make([]codexCandidate, 0, len(candidatesByPID))
	for _, candidate := range candidatesByPID {
		if hasCodexCandidateAncestor(candidate.process, detailsByPID, candidatesByPID) {
			continue
		}
		rootCandidates = append(rootCandidates, candidate)
	}
	sort.Slice(rootCandidates, func(i, j int) bool { return rootCandidates[i].process.PID < rootCandidates[j].process.PID })

	sessions := make([]core.Session, 0, len(rootCandidates))
	for _, candidate := range rootCandidates {
		root := candidate.process
		tree := core.CollectTree(processes, root.Identity())
		state := core.StateRunning
		if observedTreeInaccessible(root.PID, detailsByPID, tree) {
			state = core.StateInaccessible
		}
		prefix := "observed-codex"
		if candidate.tool == core.ToolCodexCLI {
			prefix = "observed-codex-cli"
		}
		sessions = append(sessions, core.Session{
			ID:                fmt.Sprintf("%s-%d-%d", prefix, root.PID, root.CreatedAt.UnixNano()),
			Tool:              candidate.tool,
			Confidence:        core.ConfidenceObserved,
			Root:              root.Identity(),
			StartedAt:         root.CreatedAt,
			Processes:         tree,
			State:             state,
			Resources:         core.AggregateResources(tree),
			ActivityMatchHint: candidate.hint,
		})
	}
	return sessions
}

func classifyCodexInvocation(detail ProcessDetails) (core.Tool, core.ActivityMatchHint, bool) {
	if !detail.MetadataAccessible || detail.Process.CreatedAt.IsZero() || detail.ExecutablePath == "" || detail.Process.CommandLine == "" ||
		!strings.EqualFold(filepath.Base(detail.Process.Name), "codex.exe") ||
		!strings.EqualFold(filepath.Base(detail.ExecutablePath), "codex.exe") {
		return "", core.ActivityMatchHint{}, false
	}
	args, err := winapi.DecomposeCommandLine(detail.Process.CommandLine)
	if err != nil || len(args) == 0 {
		return "", core.ActivityMatchHint{}, false
	}
	mode, modeIndex := codexMode(args[1:])
	if mode == "app-server" && isPackagedCodexPath(detail.ExecutablePath) {
		return core.ToolCodexDesktop, core.ActivityMatchHint{}, true
	}
	switch mode {
	case "", "exec", "review", "resume", "fork":
		return core.ToolCodexCLI, activityHint(args[1:], mode, modeIndex), true
	default:
		return "", core.ActivityMatchHint{}, false
	}
}

func codexMode(args []string) (string, int) {
	known := map[string]struct{}{
		"exec": {}, "review": {}, "resume": {}, "fork": {},
		"login": {}, "logout": {}, "mcp": {}, "plugin": {}, "mcp-server": {},
		"app-server": {}, "remote-control": {}, "app": {}, "completion": {},
		"update": {}, "doctor": {}, "sandbox": {}, "debug": {}, "execpolicy": {},
		"apply": {}, "archive": {}, "delete": {}, "unarchive": {}, "cloud": {},
		"cloud-tasks": {}, "responses-api-proxy": {}, "stdio-to-uds": {},
		"exec-server": {}, "features": {},
	}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		value := strings.ToLower(arg)
		if value == "--" {
			return "", index
		}
		if strings.HasPrefix(value, "-") {
			if codexFlagTakesValue(value) && !strings.Contains(value, "=") && index+1 < len(args) {
				index++
			}
			continue
		}
		if _, exists := known[value]; exists {
			return value, index
		}
		// The interactive command accepts a positional prompt. Once that prompt
		// begins, none of its words may be interpreted as a subcommand.
		return "", index
	}
	return "", len(args)
}

func codexFlagTakesValue(flag string) bool {
	if len(flag) > 2 && !strings.HasPrefix(flag, "--") {
		flag = flag[:2]
	}
	switch flag {
	case "-C", "--cd", "-m", "--model", "-c", "--config", "-p", "--profile", "-s", "--sandbox", "-a", "--ask-for-approval", "-i", "--image", "--add-dir", "--color", "--output-schema", "--local-provider", "--enable", "--disable":
		return true
	default:
		return false
	}
}

func activityHint(args []string, mode string, modeIndex int) core.ActivityMatchHint {
	var hint core.ActivityMatchHint
	scanOptions := func(start, end int) int {
		for index := start; index < end; index++ {
			arg := args[index]
			if arg == "--" {
				return index
			}
			if !strings.HasPrefix(arg, "-") {
				return index
			}
			switch {
			case (arg == "-C" || arg == "--cd") && index+1 < end:
				if filepath.IsAbs(args[index+1]) {
					hint.Workspace = filepath.Clean(args[index+1])
				}
			case strings.HasPrefix(arg, "--cd="):
				if value := strings.TrimPrefix(arg, "--cd="); filepath.IsAbs(value) {
					hint.Workspace = filepath.Clean(value)
				}
			}
			value := strings.ToLower(arg)
			if strings.HasPrefix(value, "-") && codexFlagTakesValue(value) && !strings.Contains(value, "=") && index+1 < end {
				index++
			}
		}
		return end
	}
	scanOptions(0, min(modeIndex, len(args)))
	if mode == "" || modeIndex >= len(args) {
		return hint
	}

	for index := modeIndex + 1; index < len(args); index++ {
		arg := args[index]
		if arg == "--" {
			break
		}
		if strings.HasPrefix(arg, "-") {
			next := scanOptions(index, len(args))
			if next <= index {
				break
			}
			index = next - 1
			continue
		}
		switch {
		case (mode == "resume" || mode == "fork") && isUUID(arg):
			hint.ThreadID = strings.ToLower(arg)
		case mode == "exec" && (strings.EqualFold(arg, "resume") || strings.EqualFold(arg, "fork")) && index+1 < len(args) && isUUID(args[index+1]):
			hint.ThreadID = strings.ToLower(args[index+1])
		}
		break
	}
	return hint
}

func isUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, r := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if r != '-' {
				return false
			}
			continue
		}
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
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
	return isPackagedCodexPath(detail.ExecutablePath)
}

func isPackagedCodexPath(executablePath string) bool {
	path := strings.ToLower(strings.ReplaceAll(filepath.Clean(executablePath), "/", `\`))
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

func hasCodexCandidateAncestor(process core.Process, detailsByPID map[uint32]ProcessDetails, candidates map[uint32]codexCandidate) bool {
	seen := map[uint32]struct{}{process.PID: {}}
	child := process
	for parentPID := process.ParentPID; parentPID != 0; {
		if _, cycle := seen[parentPID]; cycle {
			return false
		}
		seen[parentPID] = struct{}{}
		parentDetail, exists := detailsByPID[parentPID]
		if !exists || !validObservedEdge(parentDetail.Process, child) {
			return false
		}
		if _, exists := candidates[parentPID]; exists {
			return true
		}
		child = parentDetail.Process
		parentPID = parentDetail.Process.ParentPID
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
