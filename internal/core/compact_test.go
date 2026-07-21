package core

import (
	"testing"
	"time"
)

func TestFriendlyProcessLabelUsesFocusedRulesInPriorityOrder(t *testing.T) {
	tests := []struct {
		name         string
		process      Process
		tool         Tool
		root         bool
		wantLabel    string
		wantFriendly bool
	}{
		{name: "codex root", process: Process{Name: "cmd.exe"}, tool: ToolCodexCLI, root: true, wantLabel: "Codex agent host", wantFriendly: true},
		{name: "claude root", process: Process{Name: "claude.exe"}, tool: ToolClaudeCLI, root: true, wantLabel: "Claude agent host", wantFriendly: true},
		{name: "code mode", process: Process{Name: "node.exe", CommandLine: `node code_mode/server.js`}, wantLabel: "Code-mode host", wantFriendly: true},
		{name: "mcp server", process: Process{Name: "node.exe", CommandLine: `node example-mcp-server.js`}, wantLabel: "MCP server", wantFriendly: true},
		{name: "playwright wins over package script", process: Process{Name: "node.exe", CommandLine: `node npm-cli.js exec playwright test`}, wantLabel: "Browser tests", wantFriendly: true},
		{name: "pytest", process: Process{Name: "python.exe", CommandLine: `python -m PyTest`}, wantLabel: "Test run", wantFriendly: true},
		{name: "typescript server", process: Process{Name: "node.exe", CommandLine: `node tsserver.js`}, wantLabel: "Code intel", wantFriendly: true},
		{name: "git executable", process: Process{Name: "GIT.EXE", CommandLine: `git status`}, wantLabel: "Git", wantFriendly: true},
		{name: "package script", process: Process{Name: "node.exe", CommandLine: `node C:\npm\npm-cli.js run build`}, wantLabel: "Package script", wantFriendly: true},
		{name: "node repl", process: Process{Name: "node.exe", CommandLine: `"C:\Program Files\nodejs\node.exe"`}, wantLabel: "Node REPL", wantFriendly: true},
		{name: "fallback when command line missing", process: Process{Name: "worker.exe"}, wantLabel: "worker.exe", wantFriendly: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			label, friendly := friendlyProcessLabel(test.process, test.tool, test.root)
			if label != test.wantLabel || friendly != test.wantFriendly {
				t.Fatalf("friendlyProcessLabel() = %q, %v; want %q, %v", label, friendly, test.wantLabel, test.wantFriendly)
			}
		})
	}
}

func TestBuildCompactProcessForestCollapsesWrapperChainAndAggregatesResources(t *testing.T) {
	created := time.Unix(100, 0).UTC()
	processes := []Process{
		compactTestProcess(10, 0, "codex.exe", created, 100, 10),
		compactTestProcess(20, 10, "cmd.exe", created.Add(time.Second), 2, 1),
		compactTestProcess(21, 20, "conhost.exe", created.Add(2*time.Second), 3, 2),
		compactTestProcess(22, 21, "git.exe", created.Add(3*time.Second), 5, 3),
	}
	session := Session{Tool: ToolCodexCLI, Root: processes[0].Identity(), Processes: processes}

	got := BuildCompactProcessForest(session, CollectProcessForest(processes))

	if len(got) != 2 {
		t.Fatalf("BuildCompactProcessForest() returned %d rows, want 2: %#v", len(got), got)
	}
	if got[0].Label != "Codex agent host" || got[0].ProcessCount != 1 || got[0].PID != 10 || got[0].Depth != 0 {
		t.Fatalf("root row = %#v, want exact labeled root", got[0])
	}
	wantResources := AggregateResources(processes[1:])
	if got[1].Label != "Git" || got[1].ProcessCount != 3 || got[1].PID != 0 || got[1].ParentPID != 0 || got[1].Depth != 1 || got[1].StartedAt != processes[1].CreatedAt || got[1].Resources != wantResources {
		t.Fatalf("collapsed row = %#v, want three-process Git row with resources %#v", got[1], wantResources)
	}
	if got[0].Resources != AggregateResources(processes[:1]) {
		t.Fatalf("root resources = %#v, want own resources only", got[0].Resources)
	}
}

func TestBuildCompactProcessForestBucketsOnlyEligibleTinyHelperLeaves(t *testing.T) {
	const mebibyte = 1024 * 1024
	created := time.Unix(200, 0).UTC()
	processes := []Process{
		compactTestProcess(10, 0, "codex.exe", created, 100*mebibyte, 10),
		compactTestProcess(20, 10, "cmd.exe", created.Add(time.Second), 2*mebibyte, 1),
		compactTestProcess(21, 20, "node.exe", created.Add(2*time.Second), 3*mebibyte, 2),
		compactTestProcess(30, 10, "conhost.exe", created.Add(3*time.Second), mebibyte, 1),
		compactTestProcess(40, 10, "node.exe", created.Add(4*time.Second), 5*mebibyte+1, 1),
		compactTestProcess(50, 10, "cmd.exe", created.Add(5*time.Second), mebibyte, 1),
		compactTestProcess(60, 10, "worker.exe", created.Add(6*time.Second), mebibyte, 1),
		compactTestProcess(70, 10, "git.exe", created.Add(7*time.Second), mebibyte, 1),
	}
	processes[2].CommandLine = "node worker.js"
	processes[4].CommandLine = "node large-worker.js"
	processes[5].MemoryAvailable = false
	session := Session{Tool: ToolCodexCLI, Root: processes[0].Identity(), Processes: processes}

	got := BuildCompactProcessForest(session, CollectProcessForest(processes))

	bucket := compactRowWithLabel(t, got, "+3 shell/helper processes")
	wantResources := AggregateResources([]Process{processes[1], processes[2], processes[3]})
	if bucket.ProcessCount != 3 || bucket.Depth != 1 || bucket.PID != 0 || bucket.Resources != wantResources {
		t.Fatalf("helper bucket = %#v, want three helpers with resources %#v", bucket, wantResources)
	}
	for _, want := range []string{"node.exe", "cmd.exe", "worker.exe", "Git"} {
		compactRowWithLabel(t, got, want)
	}
	total := 0
	for _, row := range got {
		total += row.ProcessCount
	}
	if total != len(processes) {
		t.Fatalf("compact rows account for %d processes, want %d: %#v", total, len(processes), got)
	}
}

func TestBuildCompactProcessForestSortsSiblingSubtreesByMemory(t *testing.T) {
	created := time.Unix(300, 0).UTC()
	processes := []Process{
		compactTestProcess(1, 0, "codex.exe", created, 1, 1),
		compactTestProcess(20, 1, "middle.exe", created.Add(time.Second), 5, 1),
		compactTestProcess(30, 1, "tie-a.exe", created.Add(2*time.Second), 5, 1),
		compactTestProcess(40, 1, "unknown.exe", created.Add(3*time.Second), 100, 1),
		compactTestProcess(50, 1, "light-parent.exe", created.Add(4*time.Second), 1, 1),
		compactTestProcess(51, 50, "heavy-child.exe", created.Add(5*time.Second), 9, 1),
		compactTestProcess(60, 1, "tie-b.exe", created.Add(6*time.Second), 5, 1),
	}
	processes[3].MemoryAvailable = false
	session := Session{Tool: ToolCodexCLI, Root: processes[0].Identity(), Processes: processes}

	got := BuildCompactProcessForest(session, CollectProcessForest(processes))

	want := []string{"Codex agent host", "light-parent.exe", "heavy-child.exe", "middle.exe", "tie-a.exe", "tie-b.exe", "unknown.exe"}
	if len(got) != len(want) {
		t.Fatalf("BuildCompactProcessForest() returned %d rows, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Label != want[i] {
			t.Fatalf("row %d label = %q, want %q; rows: %#v", i, got[i].Label, want[i], got)
		}
	}
}

func TestBuildCompactProcessForestKeepsBranchedWrappersAndBucketsPerRoot(t *testing.T) {
	const mebibyte = 1024 * 1024
	created := time.Unix(400, 0).UTC()
	processes := []Process{
		compactTestProcess(10, 0, "cmd.exe", created, mebibyte, 1),
		compactTestProcess(11, 10, "git.exe", created.Add(time.Second), 3*mebibyte, 1),
		compactTestProcess(12, 10, "python.exe", created.Add(2*time.Second), 2*mebibyte, 1),
		compactTestProcess(13, 10, "node.exe", created.Add(3*time.Second), 5*mebibyte, 1),
		compactTestProcess(14, 10, "conhost.exe", created.Add(4*time.Second), mebibyte, 1),
		compactTestProcess(30, 0, "other-root.exe", created, 50*mebibyte, 1),
		compactTestProcess(31, 30, "node.exe", created.Add(time.Second), mebibyte, 1),
		compactTestProcess(32, 30, "cmd.exe", created.Add(2*time.Second), mebibyte, 1),
	}
	processes[2].CommandLine = "python -m pytest"
	processes[3].CommandLine = "node worker.js"
	processes[6].CommandLine = "node worker-a.js"
	session := Session{Tool: ToolCodexCLI, Root: processes[0].Identity(), Processes: processes}

	got := BuildCompactProcessForest(session, CollectProcessForest(processes))

	root := compactRowWithLabel(t, got, "Codex agent host")
	if root.ProcessCount != 1 {
		t.Fatalf("branched root wrapper collapsed unexpectedly: %#v", root)
	}
	compactRowWithLabel(t, got, "Git")
	compactRowWithLabel(t, got, "Test run")
	firstBucket := compactRowWithLabel(t, got, "+2 shell/helper processes")
	secondBucket := compactRowWithLabel(t, got, "+2 shell/helper processes")
	if firstBucket.Depth != 1 || secondBucket.Depth != 1 {
		t.Fatalf("per-root buckets should be direct children: %#v", got)
	}
	bucketCount := 0
	for _, row := range got {
		if row.Label == "+2 shell/helper processes" {
			bucketCount++
		}
	}
	if bucketCount != 2 {
		t.Fatalf("helper bucket count = %d, want one for each root: %#v", bucketCount, got)
	}
}

func TestBuildCompactProcessForestKeepsAgentLabelWhenRootWrapperCollapses(t *testing.T) {
	created := time.Unix(500, 0).UTC()
	processes := []Process{
		compactTestProcess(10, 0, "cmd.exe", created, 2, 1),
		compactTestProcess(11, 10, "node.exe", created.Add(time.Second), 3, 1),
	}
	processes[1].CommandLine = "node agent.js"
	session := Session{Tool: ToolCodexCLI, Root: processes[0].Identity(), Processes: processes}

	got := BuildCompactProcessForest(session, CollectProcessForest(processes))

	if len(got) != 1 || got[0].Label != "Codex agent host" || got[0].ProcessCount != 2 || got[0].Depth != 0 {
		t.Fatalf("collapsed root wrapper = %#v, want one two-process agent host", got)
	}
}

func compactRowWithLabel(t *testing.T, rows []CompactProcessRow, label string) CompactProcessRow {
	t.Helper()
	for _, row := range rows {
		if row.Label == label {
			return row
		}
	}
	t.Fatalf("compact rows missing label %q: %#v", label, rows)
	return CompactProcessRow{}
}

func compactTestProcess(pid, parent uint32, name string, created time.Time, memory uint64, cpu float64) Process {
	return Process{
		PID:             pid,
		ParentPID:       parent,
		Name:            name,
		CreatedAt:       created,
		MemoryBytes:     memory,
		CPUPercent:      cpu,
		MemoryAvailable: true,
		CPUAvailable:    true,
	}
}
