// Package windows implements the Windows-specific process supervision and
// observation boundary for TaskWatch.
package windows

import "taskwatch/internal/core"

// ProcessDetails augments the portable process model with metadata whose
// availability depends on the target process's access controls.
type ProcessDetails struct {
	Process            core.Process
	ExecutablePath     string
	CommandLine        string
	MetadataAccessible bool
	MetadataError      string
	CPUAvailable       bool
	MemoryAvailable    bool
}

func detailsProcesses(details []ProcessDetails) []core.Process {
	processes := make([]core.Process, len(details))
	for i := range details {
		processes[i] = details[i].Process
	}
	return processes
}

// Snapshotter returns a best-effort point-in-time process snapshot. Failure to
// read optional metadata is recorded per process rather than failing the scan.
type Snapshotter interface {
	Snapshot() ([]ProcessDetails, error)
}
