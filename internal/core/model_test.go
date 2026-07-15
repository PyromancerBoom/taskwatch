package core

import (
	"testing"
	"time"
)

func TestDeriveState(t *testing.T) {
	created := time.Unix(100, 0)
	tests := []struct {
		name         string
		root         ProcessIdentity
		processes    []Process
		inaccessible bool
		want         State
	}{
		{name: "running while root exists", root: ProcessIdentity{PID: 10, CreatedAt: created}, processes: []Process{{PID: 10, CreatedAt: created}}, want: StateRunning},
		{name: "reused root PID is not running", root: ProcessIdentity{PID: 10, CreatedAt: created}, processes: []Process{{PID: 10, CreatedAt: created.Add(time.Hour)}}, want: StateLeftovers},
		{name: "leftovers after root exits", root: ProcessIdentity{PID: 10, CreatedAt: created}, processes: []Process{{PID: 11, CreatedAt: created}}, want: StateLeftovers},
		{name: "exited when job is empty", root: ProcessIdentity{PID: 10, CreatedAt: created}, want: StateExited},
		{name: "inaccessible takes precedence", root: ProcessIdentity{PID: 10, CreatedAt: created}, processes: []Process{{PID: 10, CreatedAt: created.Add(time.Hour)}}, inaccessible: true, want: StateInaccessible},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DeriveState(tt.root, tt.processes, tt.inaccessible); got != tt.want {
				t.Fatalf("DeriveState() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAggregateResources(t *testing.T) {
	processes := []Process{
		{CPUPercent: 3.5, MemoryBytes: 1024},
		{CPUPercent: 1.25, MemoryBytes: 2048},
	}

	got := AggregateResources(processes)
	if got.CPUPercent != 4.75 {
		t.Fatalf("CPUPercent = %v, want 4.75", got.CPUPercent)
	}
	if got.MemoryBytes != 3072 {
		t.Fatalf("MemoryBytes = %d, want 3072", got.MemoryBytes)
	}
}

func TestSameProcessRejectsReusedPID(t *testing.T) {
	old := ProcessIdentity{PID: 42, CreatedAt: time.Unix(100, 0)}
	reused := ProcessIdentity{PID: 42, CreatedAt: time.Unix(200, 0)}

	if SameProcess(old, reused) {
		t.Fatal("SameProcess() accepted a reused PID")
	}
}
