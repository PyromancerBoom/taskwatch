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
