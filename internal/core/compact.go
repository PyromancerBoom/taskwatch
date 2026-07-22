package core

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// CompactProcessRow is one display-only row derived from one or more exact
// processes. PID and ParentPID are set only when ProcessCount is one.
type CompactProcessRow struct {
	Label        string
	Depth        int
	ProcessCount int
	PID          uint32
	ParentPID    uint32
	StartedAt    time.Time
	Resources    ResourceUsage
}

type compactProcessNode struct {
	members  []Process
	label    string
	friendly bool
	root     bool
	children []*compactProcessNode
}

// BuildCompactProcessForest derives display rows from the already-validated
// raw forest. It never changes the session's exact process collection.
func BuildCompactProcessForest(session Session, raw []ProcessTreeRow) []CompactProcessRow {
	roots := compactNodes(session, raw)
	for i := range roots {
		roots[i] = collapseWrapperChains(roots[i], session.Tool)
		bucketTinyHelpers(roots[i])
	}
	sortCompactNodes(roots)

	rows := make([]CompactProcessRow, 0, len(raw))
	var appendRows func(*compactProcessNode, int)
	appendRows = func(node *compactProcessNode, depth int) {
		rows = append(rows, compactRow(node, depth))
		for _, child := range node.children {
			appendRows(child, depth+1)
		}
	}
	for _, root := range roots {
		appendRows(root, 0)
	}
	return rows
}

func sortCompactNodes(nodes []*compactProcessNode) {
	for _, node := range nodes {
		sortCompactNodes(node.children)
	}
	sort.SliceStable(nodes, func(i, j int) bool {
		leftBytes, leftAvailable := compactSubtreeMemory(nodes[i])
		rightBytes, rightAvailable := compactSubtreeMemory(nodes[j])
		if leftAvailable != rightAvailable {
			return leftAvailable
		}
		return leftAvailable && leftBytes > rightBytes
	})
}

func compactSubtreeMemory(node *compactProcessNode) (uint64, bool) {
	resources := AggregateResources(node.members)
	bytes := resources.MemoryBytes
	available := resources.MemoryAvailable
	for _, child := range node.children {
		childBytes, childAvailable := compactSubtreeMemory(child)
		bytes += childBytes
		available = available && childAvailable
	}
	return bytes, available
}

const tinyHelperMemoryLimit = 5 * 1024 * 1024

func bucketTinyHelpers(root *compactProcessNode) {
	var candidates []*compactProcessNode
	var collect func(*compactProcessNode)
	collect = func(node *compactProcessNode) {
		for _, child := range node.children {
			if tinyHelperLeaf(child) {
				candidates = append(candidates, child)
				continue
			}
			collect(child)
		}
	}
	collect(root)

	processCount := 0
	for _, candidate := range candidates {
		processCount += len(candidate.members)
	}
	if processCount < 2 {
		return
	}

	removed := make(map[*compactProcessNode]struct{}, len(candidates))
	members := make([]Process, 0, processCount)
	for _, candidate := range candidates {
		removed[candidate] = struct{}{}
		members = append(members, candidate.members...)
	}
	var remove func(*compactProcessNode)
	remove = func(node *compactProcessNode) {
		kept := node.children[:0]
		for _, child := range node.children {
			if _, exists := removed[child]; exists {
				continue
			}
			remove(child)
			kept = append(kept, child)
		}
		node.children = kept
	}
	remove(root)
	root.children = append(root.children, &compactProcessNode{
		members:  members,
		label:    "+" + strconv.Itoa(processCount) + " shell/helper processes",
		friendly: true,
	})
}

func tinyHelperLeaf(node *compactProcessNode) bool {
	if node.root || node.friendly || len(node.children) != 0 {
		return false
	}
	for _, process := range node.members {
		if !isHelperExecutable(process.Name) || !process.MemoryAvailable || process.MemoryBytes > tinyHelperMemoryLimit {
			return false
		}
	}
	return true
}

func isHelperExecutable(name string) bool {
	name = strings.ToLower(filepath.Base(name))
	return isWrapperExecutable(name) || isNodeExecutable(name)
}

func compactNodes(session Session, raw []ProcessTreeRow) []*compactProcessNode {
	var roots []*compactProcessNode
	var stack []*compactProcessNode
	for _, row := range raw {
		root := SameProcess(session.Root, row.Process.Identity())
		label, friendly := friendlyProcessLabel(row.Process, session.Tool, root)
		node := &compactProcessNode{members: []Process{row.Process}, label: label, friendly: friendly, root: root}
		depth := row.Depth
		if depth < 0 || depth > len(stack) {
			depth = 0
		}
		stack = stack[:depth]
		if depth == 0 {
			roots = append(roots, node)
		} else {
			stack[depth-1].children = append(stack[depth-1].children, node)
		}
		stack = append(stack, node)
	}
	return roots
}

func collapseWrapperChains(node *compactProcessNode, tool Tool) *compactProcessNode {
	for i := range node.children {
		node.children[i] = collapseWrapperChains(node.children[i], tool)
	}
	if len(node.children) != 1 || !isWrapperExecutable(node.members[0].Name) {
		return node
	}

	child := node.children[0]
	child.members = append(append([]Process(nil), node.members...), child.members...)
	child.root = child.root || node.root
	if node.root {
		child.label, child.friendly = friendlyProcessLabel(node.members[0], tool, true)
	}
	return child
}

func isWrapperExecutable(name string) bool {
	switch strings.ToLower(filepath.Base(name)) {
	case "cmd", "cmd.exe", "conhost", "conhost.exe":
		return true
	default:
		return false
	}
}

func compactRow(node *compactProcessNode, depth int) CompactProcessRow {
	row := CompactProcessRow{
		Label:        node.label,
		Depth:        depth,
		ProcessCount: len(node.members),
		StartedAt:    earliestProcessStart(node.members),
		Resources:    AggregateResources(node.members),
	}
	if len(node.members) == 1 {
		row.PID = node.members[0].PID
		row.ParentPID = node.members[0].ParentPID
	}
	return row
}

func earliestProcessStart(processes []Process) time.Time {
	var earliest time.Time
	for _, process := range processes {
		if process.CreatedAt.IsZero() || (!earliest.IsZero() && !process.CreatedAt.Before(earliest)) {
			continue
		}
		earliest = process.CreatedAt
	}
	return earliest
}

// friendlyProcessLabel derives a compact display label from the same
// classifyProcessRole result used for role summaries, so the two views can
// never describe a process differently. Roles that admit more than one
// compact label are refined by the original, more specific command-line
// checks; every refinement stays within its classified role.
func friendlyProcessLabel(process Process, tool Tool, root bool) (string, bool) {
	if root {
		switch tool {
		case ToolClaudeCLI:
			return "Claude agent host", true
		case ToolCodexCLI, ToolCodexDesktop:
			return "Codex agent host", true
		}
	}

	name := strings.ToLower(filepath.Base(process.Name))
	commandLine := strings.ToLower(process.CommandLine)

	switch classifyProcessRole(process, tool, root) {
	case RoleAgentRuntime:
		if strings.Contains(commandLine, "code-mode") || strings.Contains(commandLine, "code_mode") {
			return "Code-mode host", true
		}
	case RoleBrowserAutomation:
		return "Browser tests", true
	case RoleMCPToolServer:
		return "MCP server", true
	case RoleTestRunner:
		return "Test run", true
	case RoleCodeIntelligence:
		return "Code intel", true
	case RoleShellCommand:
		if name == "git" || name == "git.exe" {
			return "Git", true
		}
		if isPackageExecutable(name) || containsAny(commandLine, "npm-cli.js", "npx-cli.js", "pnpm.cjs", "yarn.js") {
			return "Package script", true
		}
	}

	if isNodeExecutable(name) && commandNamesExecutableOnly(process.CommandLine, name) {
		return "Node REPL", true
	}
	return process.Name, false
}

func isNodeExecutable(name string) bool {
	return name == "node" || name == "node.exe"
}

func isPackageExecutable(name string) bool {
	switch strings.TrimSuffix(name, ".exe") {
	case "npm", "npm.cmd", "npx", "npx.cmd", "pnpm", "pnpm.cmd", "yarn", "yarn.cmd":
		return true
	default:
		return false
	}
}

func commandNamesExecutableOnly(commandLine, name string) bool {
	commandLine = strings.TrimSpace(commandLine)
	if len(commandLine) >= 2 && commandLine[0] == '"' && commandLine[len(commandLine)-1] == '"' {
		commandLine = commandLine[1 : len(commandLine)-1]
	}
	return strings.EqualFold(filepath.Base(commandLine), name)
}

func containsAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}
