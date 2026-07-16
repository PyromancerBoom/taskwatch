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
		{CPUPercent: 3.5, MemoryBytes: 1024, CPUAvailable: true, MemoryAvailable: true},
		{CPUPercent: 1.25, MemoryBytes: 2048, CPUAvailable: true, MemoryAvailable: true},
	}

	got := AggregateResources(processes)
	if got.CPUPercent != 4.75 {
		t.Fatalf("CPUPercent = %v, want 4.75", got.CPUPercent)
	}
	if got.MemoryBytes != 3072 {
		t.Fatalf("MemoryBytes = %d, want 3072", got.MemoryBytes)
	}
	if !got.CPUAvailable || !got.MemoryAvailable {
		t.Fatalf("availability = CPU %t, memory %t; want both available", got.CPUAvailable, got.MemoryAvailable)
	}
}

func TestAggregateResourcesEmptySetIsUnavailable(t *testing.T) {
	got := AggregateResources(nil)
	if got.CPUAvailable || got.MemoryAvailable {
		t.Fatalf("availability = CPU %t, memory %t; want both unavailable", got.CPUAvailable, got.MemoryAvailable)
	}
}

func TestAggregateResourcesRequiresEveryProcessMetric(t *testing.T) {
	t.Run("partially unavailable memory", func(t *testing.T) {
		got := AggregateResources([]Process{
			{CPUPercent: 1, MemoryBytes: 100, CPUAvailable: true, MemoryAvailable: true},
			{CPUPercent: 2, MemoryBytes: 200, CPUAvailable: true, MemoryAvailable: false},
		})
		if !got.CPUAvailable || got.MemoryAvailable {
			t.Fatalf("availability = CPU %t, memory %t; want CPU available and memory unavailable", got.CPUAvailable, got.MemoryAvailable)
		}
		if got.CPUPercent != 3 || got.MemoryBytes != 300 {
			t.Fatalf("numeric totals = CPU %v, memory %d; want 3 and 300", got.CPUPercent, got.MemoryBytes)
		}
	})

	t.Run("first CPU sample unavailable", func(t *testing.T) {
		got := AggregateResources([]Process{{
			MemoryBytes:     100,
			CPUAvailable:    false,
			MemoryAvailable: true,
		}})
		if got.CPUAvailable || !got.MemoryAvailable {
			t.Fatalf("availability = CPU %t, memory %t; want CPU unavailable and memory available", got.CPUAvailable, got.MemoryAvailable)
		}
	})
}

func TestSameProcessRejectsReusedPID(t *testing.T) {
	old := ProcessIdentity{PID: 42, CreatedAt: time.Unix(100, 0)}
	reused := ProcessIdentity{PID: 42, CreatedAt: time.Unix(200, 0)}

	if SameProcess(old, reused) {
		t.Fatal("SameProcess() accepted a reused PID")
	}
}
