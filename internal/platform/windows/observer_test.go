package windows

import (
	"testing"
	"time"

	"github.com/PyromancerBoom/taskwatch/internal/core"
)

const packagedCodexDesktopPath = `C:\Program Files\WindowsApps\OpenAI.Codex_26.707.9981.0_x64__2p2nqsd0c76g0\app\resources\codex.exe`

func TestGroupCodexDesktopFindsRootsAndDescendants(t *testing.T) {
	created := time.Unix(100, 0).UTC()
	processes := []ProcessDetails{
		{Process: core.Process{PID: 10, ParentPID: 1, Name: "CODEX.EXE", CreatedAt: created}, ExecutablePath: packagedCodexDesktopPath},
		{Process: core.Process{PID: 11, ParentPID: 10, Name: "renderer.exe", CreatedAt: created.Add(time.Second)}},
		{Process: core.Process{PID: 12, ParentPID: 11, Name: "worker.exe", CreatedAt: created.Add(2 * time.Second)}},
		{Process: core.Process{PID: 20, ParentPID: 1, Name: "other.exe", CreatedAt: created}},
	}

	got := GroupCodexDesktop(processes, nil)
	if len(got) != 1 {
		t.Fatalf("GroupCodexDesktop() returned %d sessions, want 1", len(got))
	}
	if got[0].Tool != core.ToolCodexDesktop || got[0].Confidence != core.ConfidenceObserved {
		t.Fatalf("session = %#v, want observed Codex desktop", got[0])
	}
	if got[0].Root.PID != 10 || len(got[0].Processes) != 3 {
		t.Fatalf("root/processes = %#v/%#v, want PID 10 and three-process tree", got[0].Root, got[0].Processes)
	}
}

func TestGroupCodexDesktopExcludesSupervisedTreesAndNestedCodexRoots(t *testing.T) {
	created := time.Unix(100, 0).UTC()
	processes := []ProcessDetails{
		{Process: core.Process{PID: 10, ParentPID: 1, Name: "Codex.exe", CreatedAt: created}, ExecutablePath: packagedCodexDesktopPath},
		{Process: core.Process{PID: 11, ParentPID: 10, Name: "Codex.exe", CreatedAt: created.Add(time.Second)}, ExecutablePath: packagedCodexDesktopPath},
		{Process: core.Process{PID: 20, ParentPID: 1, Name: "Codex.exe", CreatedAt: created}, ExecutablePath: packagedCodexDesktopPath},
	}

	got := GroupCodexDesktop(processes, map[uint32]core.ProcessIdentity{20: processes[2].Process.Identity()})
	if len(got) != 1 || got[0].Root.PID != 10 || len(got[0].Processes) != 2 {
		t.Fatalf("GroupCodexDesktop() = %#v, want only root PID 10 and its nested child", got)
	}
}

func TestGroupCodexDesktopDoesNotExcludeReusedSupervisedPID(t *testing.T) {
	oldCreation := time.Unix(100, 0).UTC()
	reusedCreation := oldCreation.Add(time.Hour)
	processes := []ProcessDetails{{Process: core.Process{PID: 20, ParentPID: 1, Name: "Codex.exe", CreatedAt: reusedCreation}, ExecutablePath: packagedCodexDesktopPath, MetadataAccessible: true}}

	got := GroupCodexDesktop(processes, map[uint32]core.ProcessIdentity{20: {PID: 20, CreatedAt: oldCreation}})
	if len(got) != 1 || got[0].Root.PID != 20 {
		t.Fatalf("GroupCodexDesktop() = %#v, want reused PID observed as a new root", got)
	}
}

func TestGroupCodexDesktopMarksMetadataFailuresInaccessibleAndOmitsIdentitylessDescendants(t *testing.T) {
	created := time.Unix(100, 0).UTC()
	processes := []ProcessDetails{
		{Process: core.Process{PID: 10, ParentPID: 1, Name: "Codex.exe", CreatedAt: created}, ExecutablePath: packagedCodexDesktopPath, MetadataAccessible: true, MetadataError: "command line: access denied"},
		{Process: core.Process{PID: 11, ParentPID: 10, Name: "unknown.exe"}, MetadataAccessible: false, MetadataError: "access denied"},
	}

	got := GroupCodexDesktop(processes, nil)
	if len(got) != 1 {
		t.Fatalf("GroupCodexDesktop() returned %d sessions, want 1", len(got))
	}
	if got[0].State != core.StateInaccessible {
		t.Fatalf("session state = %q, want inaccessible", got[0].State)
	}
	if len(got[0].Processes) != 1 || got[0].Processes[0].PID != 10 {
		t.Fatalf("safe processes = %#v, want only identity-bearing root", got[0].Processes)
	}
}

func TestGroupCodexDesktopKeepsIdentitySafeTreeRunningWhenOptionalMetadataFails(t *testing.T) {
	created := time.Unix(100, 0).UTC()
	processes := []ProcessDetails{
		{
			Process:            core.Process{PID: 10, ParentPID: 1, Name: "Codex.exe", CreatedAt: created},
			ExecutablePath:     packagedCodexDesktopPath,
			MetadataAccessible: true,
			MetadataError:      "command line: access denied; memory: access denied",
		},
		{
			Process:            core.Process{PID: 11, ParentPID: 10, Name: "renderer.exe", CreatedAt: created.Add(time.Second)},
			MetadataAccessible: true,
			MetadataError:      "path: access denied",
		},
	}

	got := GroupCodexDesktop(processes, nil)
	if len(got) != 1 {
		t.Fatalf("GroupCodexDesktop() returned %d sessions, want 1", len(got))
	}
	if got[0].State != core.StateRunning {
		t.Fatalf("session state = %q, want running", got[0].State)
	}
	if len(got[0].Processes) != 2 {
		t.Fatalf("processes = %#v, want complete identity-safe tree", got[0].Processes)
	}
}

func TestGroupCodexDesktopDoesNotSuppressOlderRootBehindReusedNewerParentPID(t *testing.T) {
	older := time.Unix(100, 0).UTC()
	newer := older.Add(time.Hour)
	processes := []ProcessDetails{
		{Process: core.Process{PID: 10, ParentPID: 20, Name: "Codex.exe", CreatedAt: older}, ExecutablePath: packagedCodexDesktopPath, MetadataAccessible: true},
		{Process: core.Process{PID: 20, ParentPID: 1, Name: "Codex.exe", CreatedAt: newer}, ExecutablePath: packagedCodexDesktopPath, MetadataAccessible: true},
	}

	got := GroupCodexDesktop(processes, nil)
	if len(got) != 2 {
		t.Fatalf("GroupCodexDesktop() returned %d sessions, want both Codex roots", len(got))
	}
	if got[0].Root.PID != 10 || got[1].Root.PID != 20 {
		t.Fatalf("root PIDs = %d, %d, want 10 and 20", got[0].Root.PID, got[1].Root.PID)
	}
}

func TestGroupCodexDesktopIgnoresKnownOlderChildWhenDerivingInaccessibleState(t *testing.T) {
	created := time.Unix(200, 0).UTC()
	processes := []ProcessDetails{
		{Process: core.Process{PID: 20, ParentPID: 1, Name: "Codex.exe", CreatedAt: created}, ExecutablePath: packagedCodexDesktopPath, MetadataAccessible: true},
		{
			Process:            core.Process{PID: 10, ParentPID: 20, Name: "older-unrelated.exe", CreatedAt: created.Add(-time.Hour)},
			MetadataAccessible: false,
			MetadataError:      "access denied",
		},
	}

	got := GroupCodexDesktop(processes, nil)
	if len(got) != 1 {
		t.Fatalf("GroupCodexDesktop() returned %d sessions, want 1", len(got))
	}
	if got[0].State != core.StateRunning {
		t.Fatalf("session state = %q, want running", got[0].State)
	}
	if len(got[0].Processes) != 1 || got[0].Processes[0].PID != 20 {
		t.Fatalf("safe processes = %#v, want only root PID 20", got[0].Processes)
	}
}

func TestGroupCodexDesktopMarksIdentitylessChildOfSafeDescendantInaccessible(t *testing.T) {
	created := time.Unix(200, 0).UTC()
	processes := []ProcessDetails{
		{Process: core.Process{PID: 20, ParentPID: 1, Name: "Codex.exe", CreatedAt: created}, ExecutablePath: packagedCodexDesktopPath, MetadataAccessible: true},
		{Process: core.Process{PID: 21, ParentPID: 20, Name: "renderer.exe", CreatedAt: created.Add(time.Second)}, MetadataAccessible: true},
		{
			Process:            core.Process{PID: 22, ParentPID: 21, Name: "unknown.exe"},
			MetadataAccessible: false,
			MetadataError:      "creation time unavailable",
		},
	}

	got := GroupCodexDesktop(processes, nil)
	if len(got) != 1 {
		t.Fatalf("GroupCodexDesktop() returned %d sessions, want 1", len(got))
	}
	if got[0].State != core.StateInaccessible {
		t.Fatalf("session state = %q, want inaccessible", got[0].State)
	}
	if len(got[0].Processes) != 2 || got[0].Processes[0].PID != 20 || got[0].Processes[1].PID != 21 {
		t.Fatalf("safe processes = %#v, want only root PID 20 and renderer PID 21", got[0].Processes)
	}
}

func TestGroupCodexDesktopRejectsIdentitylessParentEdge(t *testing.T) {
	created := time.Unix(200, 0).UTC()
	processes := []ProcessDetails{
		{
			Process:            core.Process{PID: 10, ParentPID: 20, Name: "Codex.exe", CreatedAt: created},
			ExecutablePath:     packagedCodexDesktopPath,
			MetadataAccessible: true,
		},
		{
			Process:            core.Process{PID: 20, ParentPID: 1, Name: "Codex.exe"},
			ExecutablePath:     packagedCodexDesktopPath,
			MetadataAccessible: false,
			MetadataError:      "creation time unavailable",
		},
	}

	got := GroupCodexDesktop(processes, nil)
	if len(got) != 2 {
		t.Fatalf("GroupCodexDesktop() returned %d sessions, want identifiable child and identity-less parent as separate roots", len(got))
	}
	if got[0].Root.PID != 10 || got[0].State != core.StateRunning || len(got[0].Processes) != 1 {
		t.Fatalf("identifiable child session = %#v, want independent running root PID 10", got[0])
	}
	if got[1].Root.PID != 20 || got[1].State != core.StateInaccessible || len(got[1].Processes) != 0 {
		t.Fatalf("identity-less parent session = %#v, want non-terminable inaccessible root PID 20", got[1])
	}
}

func TestGroupCodexDesktopIgnoresCodexCLIExecutablePath(t *testing.T) {
	processes := []ProcessDetails{{
		Process:        core.Process{PID: 10, ParentPID: 1, Name: "codex.exe", CreatedAt: time.Unix(100, 0).UTC()},
		ExecutablePath: `C:\Users\developer\AppData\Roaming\npm\node_modules\@openai\codex\vendor\x86_64-pc-windows-msvc\codex.exe`,
	}}

	if got := GroupCodexDesktop(processes, nil); len(got) != 0 {
		t.Fatalf("GroupCodexDesktop() = %#v, want CLI executable ignored", got)
	}
}

func TestGroupCodexDesktopIgnoresCodexExecutableWithoutPath(t *testing.T) {
	processes := []ProcessDetails{{
		Process: core.Process{PID: 10, ParentPID: 1, Name: "codex.exe", CreatedAt: time.Unix(100, 0).UTC()},
	}}

	if got := GroupCodexDesktop(processes, nil); len(got) != 0 {
		t.Fatalf("GroupCodexDesktop() = %#v, want uncertain executable ignored", got)
	}
}
