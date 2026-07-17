package core

import (
	"testing"
	"time"
)

func TestCollectTreeReturnsRootAndDescendantsOnly(t *testing.T) {
	created := time.Unix(100, 0)
	processes := []Process{
		{PID: 10, ParentPID: 1, Name: "codex.exe", CreatedAt: created},
		{PID: 11, ParentPID: 10, Name: "node.exe", CreatedAt: created.Add(time.Second)},
		{PID: 12, ParentPID: 11, Name: "powershell.exe", CreatedAt: created.Add(2 * time.Second)},
		{PID: 99, ParentPID: 1, Name: "unrelated.exe", CreatedAt: created},
	}

	got := CollectTree(processes, ProcessIdentity{PID: 10, CreatedAt: created})
	if len(got) != 3 {
		t.Fatalf("CollectTree() returned %d processes, want 3", len(got))
	}
	for i, pid := range []uint32{10, 11, 12} {
		if got[i].PID != pid {
			t.Fatalf("CollectTree()[%d].PID = %d, want %d", i, got[i].PID, pid)
		}
	}
}

func TestCollectTreeRejectsReusedRootPID(t *testing.T) {
	original := time.Unix(100, 0)
	processes := []Process{{PID: 10, ParentPID: 1, Name: "codex.exe", CreatedAt: original.Add(time.Hour)}}

	if got := CollectTree(processes, ProcessIdentity{PID: 10, CreatedAt: original}); len(got) != 0 {
		t.Fatalf("CollectTree() returned reused root: %#v", got)
	}
}

func TestCollectTreeRejectsIdentitylessAndOlderDescendants(t *testing.T) {
	created := time.Unix(100, 0).UTC()
	processes := []Process{
		{PID: 10, ParentPID: 1, Name: "Codex.exe", CreatedAt: created},
		{PID: 11, ParentPID: 10, Name: "reused.exe", CreatedAt: created.Add(-time.Second)},
		{PID: 12, ParentPID: 10, Name: "unknown.exe"},
		{PID: 13, ParentPID: 10, Name: "valid.exe", CreatedAt: created.Add(time.Second)},
	}

	got := CollectTree(processes, processes[0].Identity())
	if len(got) != 2 || got[0].PID != 10 || got[1].PID != 13 {
		t.Fatalf("CollectTree() = %#v, want root and only identity-valid descendant", got)
	}
}

func TestCollectProcessForestReturnsDeterministicPreorder(t *testing.T) {
	created := time.Unix(100, 0).UTC()
	processes := []Process{
		{PID: 22, ParentPID: 20, Name: "second-child.exe", CreatedAt: created.Add(2 * time.Second)},
		{PID: 5, Name: "later.exe", CreatedAt: created.Add(5 * time.Second)},
		{PID: 23, ParentPID: 21, Name: "grandchild.exe", CreatedAt: created.Add(4 * time.Second)},
		{PID: 20, ParentPID: 999, Name: "tree-root.exe", CreatedAt: created},
		{PID: 5, Name: "zulu.exe", CreatedAt: created.Add(4 * time.Second)},
		{PID: 21, ParentPID: 20, Name: "first-child.exe", CreatedAt: created.Add(3 * time.Second)},
		{PID: 10, Name: "middle-root.exe", CreatedAt: created.Add(time.Second)},
		{PID: 5, Name: "alpha.exe", CreatedAt: created.Add(4 * time.Second)},
	}

	got := CollectProcessForest(processes)
	want := []struct {
		pid   uint32
		name  string
		depth int
	}{
		{5, "alpha.exe", 0},
		{5, "zulu.exe", 0},
		{5, "later.exe", 0},
		{10, "middle-root.exe", 0},
		{20, "tree-root.exe", 0},
		{21, "first-child.exe", 1},
		{23, "grandchild.exe", 2},
		{22, "second-child.exe", 1},
	}
	assertProcessTreeRows(t, got, want)
}

func TestCollectProcessForestPromotesInvalidParentRelationships(t *testing.T) {
	created := time.Unix(100, 0).UTC()
	processes := []Process{
		{PID: 31, ParentPID: 30, Name: "valid-child.exe", CreatedAt: created.Add(time.Second)},
		{PID: 30, ParentPID: 999, Name: "missing-parent.exe", CreatedAt: created},
		{PID: 41, ParentPID: 40, Name: "unknown-parent-creation.exe", CreatedAt: created},
		{PID: 40, Name: "unknown-creation.exe"},
		{PID: 51, ParentPID: 50, Name: "unknown-child-creation.exe"},
		{PID: 50, Name: "known-parent.exe", CreatedAt: created},
		{PID: 61, ParentPID: 60, Name: "older-child.exe", CreatedAt: created},
		{PID: 60, Name: "newer-parent.exe", CreatedAt: created.Add(time.Second)},
		{PID: 70, ParentPID: 70, Name: "self-parent.exe", CreatedAt: created},
	}

	got := CollectProcessForest(processes)
	want := []struct {
		pid   uint32
		name  string
		depth int
	}{
		{30, "missing-parent.exe", 0},
		{31, "valid-child.exe", 1},
		{40, "unknown-creation.exe", 0},
		{41, "unknown-parent-creation.exe", 0},
		{50, "known-parent.exe", 0},
		{51, "unknown-child-creation.exe", 0},
		{60, "newer-parent.exe", 0},
		{61, "older-child.exe", 0},
		{70, "self-parent.exe", 0},
	}
	assertProcessTreeRows(t, got, want)
}

func TestCollectProcessForestBindsChildToMostRecentReusedPID(t *testing.T) {
	created := time.Unix(100, 0).UTC()
	processes := []Process{
		{PID: 5, ParentPID: 999, Name: "old-5.exe", CreatedAt: created},
		{PID: 5, ParentPID: 999, Name: "new-5.exe", CreatedAt: created.Add(10 * time.Second)},
		{PID: 6, ParentPID: 5, Name: "child.exe", CreatedAt: created.Add(11 * time.Second)},
	}

	got := CollectProcessForest(processes)
	want := []struct {
		pid   uint32
		name  string
		depth int
	}{
		{5, "old-5.exe", 0},
		{5, "new-5.exe", 0},
		{6, "child.exe", 1},
	}
	assertProcessTreeRows(t, got, want)
}

func TestCollectProcessForestKeepsCyclicComponentsVisible(t *testing.T) {
	created := time.Unix(100, 0).UTC()
	processes := []Process{
		{PID: 83, ParentPID: 81, Name: "branch.exe", CreatedAt: created.Add(time.Second)},
		{PID: 82, ParentPID: 81, Name: "cycle-c.exe", CreatedAt: created},
		{PID: 80, ParentPID: 82, Name: "cycle-a.exe", CreatedAt: created},
		{PID: 81, ParentPID: 80, Name: "cycle-b.exe", CreatedAt: created},
	}

	got := CollectProcessForest(processes)
	want := []struct {
		pid   uint32
		name  string
		depth int
	}{
		{80, "cycle-a.exe", 0},
		{81, "cycle-b.exe", 1},
		{82, "cycle-c.exe", 2},
		{83, "branch.exe", 2},
	}
	assertProcessTreeRows(t, got, want)
}

func assertProcessTreeRows(t *testing.T, got []ProcessTreeRow, want []struct {
	pid   uint32
	name  string
	depth int
}) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("CollectProcessForest() returned %d rows, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Process.PID != want[i].pid || got[i].Process.Name != want[i].name || got[i].Depth != want[i].depth {
			t.Fatalf("CollectProcessForest()[%d] = %#v, want PID %d, name %q, depth %d", i, got[i], want[i].pid, want[i].name, want[i].depth)
		}
	}
}
