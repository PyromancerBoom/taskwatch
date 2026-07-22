package core

import (
	"path/filepath"
	"sort"
	"strings"
)

// ProcessRole is a human-meaningful category assigned to exactly one process.
type ProcessRole string

const (
	RoleAgentRuntime      ProcessRole = "agent-runtime"
	RoleShellCommand      ProcessRole = "shell-command"
	RoleTestRunner        ProcessRole = "test-runner"
	RoleDevelopmentServer ProcessRole = "development-server"
	RoleBrowserAutomation ProcessRole = "browser-automation"
	RoleMCPToolServer     ProcessRole = "mcp-tool-server"
	RoleCodeIntelligence  ProcessRole = "code-intelligence"
	RoleBackgroundHelper  ProcessRole = "background-helper"
)

// ProcessRoleSummary aggregates one role's process count and resource use.
type ProcessRoleSummary struct {
	Role         ProcessRole
	ProcessCount int
	Resources    ResourceUsage
}

// roleOrder is the stable role-name tie-break used when memory data is equal
// or unavailable for multiple roles.
var roleOrder = []ProcessRole{
	RoleAgentRuntime,
	RoleShellCommand,
	RoleTestRunner,
	RoleDevelopmentServer,
	RoleBrowserAutomation,
	RoleMCPToolServer,
	RoleCodeIntelligence,
	RoleBackgroundHelper,
}

// SummarizeProcessRoles buckets every process in the session's already
// validated collection into exactly one semantic role and aggregates each
// role's resource use. It never mutates the source session or process slice.
func SummarizeProcessRoles(session Session) []ProcessRoleSummary {
	byRole := make(map[ProcessRole][]Process, len(roleOrder))
	for _, process := range session.Processes {
		root := SameProcess(session.Root, process.Identity())
		role := classifyProcessRole(process, session.Tool, root)
		byRole[role] = append(byRole[role], process)
	}

	summaries := make([]ProcessRoleSummary, 0, len(byRole))
	for role, processes := range byRole {
		summaries = append(summaries, ProcessRoleSummary{
			Role:         role,
			ProcessCount: len(processes),
			Resources:    AggregateResources(processes),
		})
	}

	roleRank := make(map[ProcessRole]int, len(roleOrder))
	for i, role := range roleOrder {
		roleRank[role] = i
	}

	sort.SliceStable(summaries, func(i, j int) bool {
		left, right := summaries[i], summaries[j]
		if (left.Role == RoleAgentRuntime) != (right.Role == RoleAgentRuntime) {
			return left.Role == RoleAgentRuntime
		}
		if left.Resources.MemoryAvailable != right.Resources.MemoryAvailable {
			return left.Resources.MemoryAvailable
		}
		if left.Resources.MemoryAvailable && left.Resources.MemoryBytes != right.Resources.MemoryBytes {
			return left.Resources.MemoryBytes > right.Resources.MemoryBytes
		}
		return roleRank[left.Role] < roleRank[right.Role]
	})

	return summaries
}

// ProcessRoleLabel returns the human-readable label for a role.
func ProcessRoleLabel(role ProcessRole) string {
	switch role {
	case RoleAgentRuntime:
		return "Agent runtime"
	case RoleShellCommand:
		return "Shell commands"
	case RoleTestRunner:
		return "Test runners"
	case RoleDevelopmentServer:
		return "Development servers"
	case RoleBrowserAutomation:
		return "Browser automation"
	case RoleMCPToolServer:
		return "MCP/tool servers"
	case RoleCodeIntelligence:
		return "Code intelligence"
	case RoleBackgroundHelper:
		return "Background helpers"
	default:
		return string(role)
	}
}

// classifyProcessRole is the single source of truth for semantic process
// classification, shared by role summaries and compact process labels. It
// inspects only the executable base name and command line already present in
// the process snapshot: no file, port, network, or disk access.
func classifyProcessRole(process Process, tool Tool, root bool) ProcessRole {
	name := strings.ToLower(filepath.Base(process.Name))
	commandLine := strings.ToLower(process.CommandLine)
	// Evidence is either the command line or, when it is missing/empty, the
	// bare executable name, so a recognizable tool launched with no visible
	// arguments (e.g. "gopls.exe") still classifies correctly.
	evidence := commandLine
	if evidence == "" {
		evidence = name
	}

	if root || strings.Contains(commandLine, "code-mode") || strings.Contains(commandLine, "code_mode") {
		return RoleAgentRuntime
	}

	switch {
	case containsAny(evidence, "playwright", "puppeteer", "chrome-devtools-mcp", "remote-debugging-port"):
		return RoleBrowserAutomation
	case containsAny(evidence, "mcp-server", "mcp_server", "@modelcontextprotocol/server-", "@modelcontextprotocol\\server-"):
		return RoleMCPToolServer
	case containsAny(evidence, "pytest", "jest", "vitest", "go test", "cargo test", "dotnet test", "npm test", "npm run test", "yarn test", "pnpm test"):
		return RoleTestRunner
	case containsAny(evidence, "vite", "next dev", "webpack serve", "npm run dev", "dotnet watch"):
		return RoleDevelopmentServer
	case containsAny(evidence, "tsserver", "gopls", "rust-analyzer", "pyright", "pylance", "omnisharp"):
		return RoleCodeIntelligence
	case isShellExecutable(name) || isPackageExecutable(name) || containsAny(commandLine, "npm-cli.js", "npx-cli.js", "pnpm.cjs", "yarn.js"):
		return RoleShellCommand
	default:
		return RoleBackgroundHelper
	}
}

func isShellExecutable(name string) bool {
	switch strings.TrimSuffix(name, ".exe") {
	case "powershell", "pwsh", "cmd", "conhost", "git", "bash", "sh":
		return true
	default:
		return false
	}
}
