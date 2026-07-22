package core

import (
	"testing"
	"time"
)

func TestClassifyProcessRoleCoversEachRoleInPriorityOrder(t *testing.T) {
	tests := []struct {
		name    string
		process Process
		tool    Tool
		root    bool
		want    ProcessRole
	}{
		{name: "session root is agent runtime", process: Process{Name: "codex.exe"}, tool: ToolCodexCLI, root: true, want: RoleAgentRuntime},
		{name: "code-mode host without root", process: Process{Name: "node.exe", CommandLine: "node code_mode/server.js"}, want: RoleAgentRuntime},
		{name: "playwright is browser automation", process: Process{Name: "node.exe", CommandLine: "node playwright test"}, want: RoleBrowserAutomation},
		{name: "puppeteer is browser automation", process: Process{Name: "node.exe", CommandLine: "node puppeteer-script.js"}, want: RoleBrowserAutomation},
		{name: "chrome devtools mcp is browser automation", process: Process{Name: "node.exe", CommandLine: "node chrome-devtools-mcp"}, want: RoleBrowserAutomation},
		{name: "remote debugging port is browser automation", process: Process{Name: "chrome.exe", CommandLine: "chrome --remote-debugging-port=9222"}, want: RoleBrowserAutomation},
		{name: "node mcp server", process: Process{Name: "node.exe", CommandLine: "node example-mcp-server.js"}, want: RoleMCPToolServer},
		{name: "python mcp server", process: Process{Name: "python.exe", CommandLine: "python -m mcp_server_fetch"}, want: RoleMCPToolServer},
		{name: "model context protocol package", process: Process{Name: "node.exe", CommandLine: `node C:\tools\node_modules\@modelcontextprotocol\server-filesystem\dist\index.js`}, want: RoleMCPToolServer},
		{name: "pytest", process: Process{Name: "python.exe", CommandLine: "python -m pytest"}, want: RoleTestRunner},
		{name: "jest", process: Process{Name: "node.exe", CommandLine: "node jest --runInBand"}, want: RoleTestRunner},
		{name: "vitest", process: Process{Name: "node.exe", CommandLine: "node vitest run"}, want: RoleTestRunner},
		{name: "go test", process: Process{Name: "go.exe", CommandLine: "go test ./..."}, want: RoleTestRunner},
		{name: "cargo test", process: Process{Name: "cargo.exe", CommandLine: "cargo test"}, want: RoleTestRunner},
		{name: "dotnet test", process: Process{Name: "dotnet.exe", CommandLine: "dotnet test"}, want: RoleTestRunner},
		{name: "vite dev server", process: Process{Name: "node.exe", CommandLine: "node vite"}, want: RoleDevelopmentServer},
		{name: "next dev", process: Process{Name: "node.exe", CommandLine: "node next dev"}, want: RoleDevelopmentServer},
		{name: "webpack serve", process: Process{Name: "node.exe", CommandLine: "node webpack serve"}, want: RoleDevelopmentServer},
		{name: "npm run dev", process: Process{Name: "npm.cmd", CommandLine: "npm run dev"}, want: RoleDevelopmentServer},
		{name: "dotnet watch", process: Process{Name: "dotnet.exe", CommandLine: "dotnet watch run"}, want: RoleDevelopmentServer},
		{name: "tsserver", process: Process{Name: "node.exe", CommandLine: "node tsserver.js"}, want: RoleCodeIntelligence},
		{name: "gopls", process: Process{Name: "gopls.exe", CommandLine: "gopls serve"}, want: RoleCodeIntelligence},
		{name: "rust-analyzer", process: Process{Name: "rust-analyzer.exe"}, want: RoleCodeIntelligence},
		{name: "pyright", process: Process{Name: "node.exe", CommandLine: "node pyright-langserver"}, want: RoleCodeIntelligence},
		{name: "pylance", process: Process{Name: "python.exe", CommandLine: "python -m pylance"}, want: RoleCodeIntelligence},
		{name: "omnisharp", process: Process{Name: "omnisharp.exe"}, want: RoleCodeIntelligence},
		{name: "powershell is shell command", process: Process{Name: "powershell.exe", CommandLine: "powershell -File script.ps1"}, want: RoleShellCommand},
		{name: "cmd is shell command", process: Process{Name: "cmd.exe"}, want: RoleShellCommand},
		{name: "git is shell command", process: Process{Name: "git.exe", CommandLine: "git status"}, want: RoleShellCommand},
		{name: "npm package command", process: Process{Name: "npm.cmd", CommandLine: "npm install"}, want: RoleShellCommand},
		{name: "unknown executable is background helper", process: Process{Name: "worker.exe"}, want: RoleBackgroundHelper},
		{name: "missing command line is background helper", process: Process{Name: "node.exe"}, want: RoleBackgroundHelper},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := classifyProcessRole(test.process, test.tool, test.root); got != test.want {
				t.Fatalf("classifyProcessRole() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestClassifyProcessRolePriorityCollisions(t *testing.T) {
	tests := []struct {
		name    string
		process Process
		want    ProcessRole
	}{
		{
			name:    "playwright launched through npm is browser automation, not shell",
			process: Process{Name: "node.exe", CommandLine: "node npm-cli.js exec playwright test"},
			want:    RoleBrowserAutomation,
		},
		{
			name:    "mcp server launched through node is mcp, not background",
			process: Process{Name: "node.exe", CommandLine: "node example-mcp-server.js"},
			want:    RoleMCPToolServer,
		},
		{
			name:    "pytest wins over generic package command wording",
			process: Process{Name: "npm.cmd", CommandLine: "npm test -- pytest-style"},
			want:    RoleTestRunner,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := classifyProcessRole(test.process, "", false); got != test.want {
				t.Fatalf("classifyProcessRole() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestClassifyProcessRoleRootAlwaysAgentRuntime(t *testing.T) {
	// A root process must be agent runtime even if its command line would
	// otherwise match a more specific role.
	process := Process{Name: "node.exe", CommandLine: "node example-mcp-server.js"}
	if got := classifyProcessRole(process, ToolCodexCLI, true); got != RoleAgentRuntime {
		t.Fatalf("classifyProcessRole() = %q, want %q", got, RoleAgentRuntime)
	}
}

func roleTestProcess(pid uint32, name string, created time.Time, memory uint64, memoryAvailable bool, commandLine string) Process {
	return Process{
		PID:             pid,
		Name:            name,
		CommandLine:     commandLine,
		CreatedAt:       created,
		MemoryBytes:     memory,
		MemoryAvailable: memoryAvailable,
		CPUAvailable:    true,
	}
}

func TestSummarizeProcessRolesCountsAndAggregatesEveryProcess(t *testing.T) {
	created := time.Unix(1000, 0).UTC()
	root := roleTestProcess(1, "codex.exe", created, 100, true, "")
	shellOne := roleTestProcess(2, "cmd.exe", created.Add(time.Second), 10, true, "")
	shellTwo := roleTestProcess(3, "git.exe", created.Add(2*time.Second), 5, true, "git status")
	mcp := roleTestProcess(4, "node.exe", created.Add(3*time.Second), 20, true, "node example-mcp-server.js")
	background := roleTestProcess(5, "worker.exe", created.Add(4*time.Second), 1, true, "")

	processes := []Process{root, shellOne, shellTwo, mcp, background}
	original := append([]Process(nil), processes...)
	session := Session{Tool: ToolCodexCLI, Root: root.Identity(), Processes: processes}

	summaries := SummarizeProcessRoles(session)

	totalCount := 0
	var totalMemory uint64
	byRole := make(map[ProcessRole]ProcessRoleSummary, len(summaries))
	for _, summary := range summaries {
		totalCount += summary.ProcessCount
		totalMemory += summary.Resources.MemoryBytes
		byRole[summary.Role] = summary
	}
	if totalCount != len(processes) {
		t.Fatalf("total process count = %d, want %d", totalCount, len(processes))
	}
	wantTotalMemory := AggregateResources(processes).MemoryBytes
	if totalMemory != wantTotalMemory {
		t.Fatalf("total memory = %d, want %d", totalMemory, wantTotalMemory)
	}

	if summary := byRole[RoleAgentRuntime]; summary.ProcessCount != 1 || summary.Resources != AggregateResources([]Process{root}) {
		t.Fatalf("agent runtime summary = %#v", summary)
	}
	if summary := byRole[RoleShellCommand]; summary.ProcessCount != 2 || summary.Resources != AggregateResources([]Process{shellOne, shellTwo}) {
		t.Fatalf("shell command summary = %#v", summary)
	}
	if summary := byRole[RoleMCPToolServer]; summary.ProcessCount != 1 || summary.Resources != AggregateResources([]Process{mcp}) {
		t.Fatalf("mcp summary = %#v", summary)
	}
	if summary := byRole[RoleBackgroundHelper]; summary.ProcessCount != 1 || summary.Resources != AggregateResources([]Process{background}) {
		t.Fatalf("background summary = %#v", summary)
	}

	// Roles that received no processes must not appear.
	for _, role := range []ProcessRole{RoleTestRunner, RoleDevelopmentServer, RoleBrowserAutomation, RoleCodeIntelligence} {
		if _, exists := byRole[role]; exists {
			t.Fatalf("unexpected empty role %q present in summaries", role)
		}
	}

	if len(session.Processes) != len(original) {
		t.Fatalf("session.Processes length changed: got %d, want %d", len(session.Processes), len(original))
	}
	for i := range original {
		if session.Processes[i] != original[i] {
			t.Fatalf("session.Processes mutated at index %d: got %#v, want %#v", i, session.Processes[i], original[i])
		}
	}
}

func TestSummarizeProcessRolesOrdering(t *testing.T) {
	created := time.Unix(2000, 0).UTC()
	root := roleTestProcess(1, "codex.exe", created, 1, true, "")
	heavyShell := roleTestProcess(2, "cmd.exe", created.Add(time.Second), 300, true, "")
	lightMCP := roleTestProcess(3, "node.exe", created.Add(2*time.Second), 100, true, "node example-mcp-server.js")
	noMemoryBackground := roleTestProcess(4, "worker.exe", created.Add(3*time.Second), 0, false, "")
	noMemoryCodeIntel := roleTestProcess(5, "gopls.exe", created.Add(4*time.Second), 0, false, "gopls serve")

	session := Session{
		Tool:      ToolCodexCLI,
		Root:      root.Identity(),
		Processes: []Process{noMemoryCodeIntel, noMemoryBackground, lightMCP, heavyShell, root},
	}

	got := SummarizeProcessRoles(session)

	wantOrder := []ProcessRole{RoleAgentRuntime, RoleShellCommand, RoleMCPToolServer, RoleCodeIntelligence, RoleBackgroundHelper}
	if len(got) != len(wantOrder) {
		t.Fatalf("SummarizeProcessRoles() returned %d summaries, want %d: %#v", len(got), len(wantOrder), got)
	}
	for i, role := range wantOrder {
		if got[i].Role != role {
			t.Fatalf("summary[%d].Role = %q, want %q; got order %#v", i, got[i].Role, role, got)
		}
	}
}

func TestSummarizeProcessRolesTiesBreakByRoleName(t *testing.T) {
	created := time.Unix(3000, 0).UTC()
	root := roleTestProcess(1, "codex.exe", created, 1, true, "")
	// Shell command and code intelligence both unavailable for memory: tie
	// broken by roleOrder position (shell command comes before code
	// intelligence in roleOrder).
	shell := roleTestProcess(2, "cmd.exe", created.Add(time.Second), 0, false, "")
	codeIntel := roleTestProcess(3, "gopls.exe", created.Add(2*time.Second), 0, false, "gopls serve")

	session := Session{
		Tool:      ToolCodexCLI,
		Root:      root.Identity(),
		Processes: []Process{codeIntel, shell, root},
	}

	got := SummarizeProcessRoles(session)
	wantOrder := []ProcessRole{RoleAgentRuntime, RoleShellCommand, RoleCodeIntelligence}
	for i, role := range wantOrder {
		if got[i].Role != role {
			t.Fatalf("summary[%d].Role = %q, want %q; got order %#v", i, got[i].Role, role, got)
		}
	}
}

func TestProcessRoleLabelReturnsHumanReadableText(t *testing.T) {
	tests := []struct {
		role ProcessRole
		want string
	}{
		{RoleAgentRuntime, "Agent runtime"},
		{RoleShellCommand, "Shell commands"},
		{RoleTestRunner, "Test runners"},
		{RoleDevelopmentServer, "Development servers"},
		{RoleBrowserAutomation, "Browser automation"},
		{RoleMCPToolServer, "MCP/tool servers"},
		{RoleCodeIntelligence, "Code intelligence"},
		{RoleBackgroundHelper, "Background helpers"},
	}
	for _, test := range tests {
		if got := ProcessRoleLabel(test.role); got != test.want {
			t.Fatalf("ProcessRoleLabel(%q) = %q, want %q", test.role, got, test.want)
		}
	}
}

// TestCompactLabelsShareRoleClassification asserts compact process labels and
// semantic roles cannot drift: any process resolved to one role must never
// present a compact label associated with a different role's identity.
func TestCompactLabelsShareRoleClassification(t *testing.T) {
	tests := []struct {
		name    string
		process Process
		tool    Tool
		root    bool
		want    ProcessRole
	}{
		{name: "browser automation via npm", process: Process{Name: "node.exe", CommandLine: "node npm-cli.js exec playwright test"}, want: RoleBrowserAutomation},
		{name: "mcp server", process: Process{Name: "node.exe", CommandLine: "node example-mcp-server.js"}, want: RoleMCPToolServer},
		{name: "pytest", process: Process{Name: "python.exe", CommandLine: "python -m PyTest"}, want: RoleTestRunner},
		{name: "tsserver", process: Process{Name: "node.exe", CommandLine: "node tsserver.js"}, want: RoleCodeIntelligence},
		{name: "git", process: Process{Name: "GIT.EXE", CommandLine: "git status"}, want: RoleShellCommand},
		{name: "package script", process: Process{Name: "node.exe", CommandLine: `node C:\npm\npm-cli.js run build`}, want: RoleShellCommand},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			label, friendly := friendlyProcessLabel(test.process, test.tool, test.root)
			if !friendly {
				t.Fatalf("friendlyProcessLabel() friendly = false, want true for label %q", label)
			}
			gotRole := classifyProcessRole(test.process, test.tool, test.root)
			if gotRole != test.want {
				t.Fatalf("classifyProcessRole() = %q, want %q (compact label was %q)", gotRole, test.want, label)
			}
		})
	}
}
