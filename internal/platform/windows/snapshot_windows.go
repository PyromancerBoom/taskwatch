package windows

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	winapi "golang.org/x/sys/windows"

	"github.com/PyromancerBoom/taskwatch/internal/core"
)

const processCommandLineInformation = 60

var (
	psapi                         = winapi.NewLazySystemDLL("psapi.dll")
	procGetProcessMemoryInfo      = psapi.NewProc("GetProcessMemoryInfo")
	ntdll                         = winapi.NewLazySystemDLL("ntdll.dll")
	procNtQueryInformationProcess = ntdll.NewProc("NtQueryInformationProcess")
)

type cpuSample struct {
	createdAt time.Time
	at        time.Time
	ticks     uint64
}

// ProcessSnapshotter uses Tool Help for enumeration and limited-information
// process handles for optional metadata. It is safe for repeated use.
type ProcessSnapshotter struct {
	mu       sync.Mutex
	previous map[uint32]cpuSample
	now      func() time.Time
}

func NewProcessSnapshotter() *ProcessSnapshotter {
	return &ProcessSnapshotter{previous: make(map[uint32]cpuSample), now: time.Now}
}

func (snapshotter *ProcessSnapshotter) Snapshot() ([]ProcessDetails, error) {
	handle, err := winapi.CreateToolhelp32Snapshot(winapi.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, fmt.Errorf("create process snapshot: %w", err)
	}
	defer winapi.CloseHandle(handle)

	now := snapshotter.now()
	entry := winapi.ProcessEntry32{Size: uint32(unsafe.Sizeof(winapi.ProcessEntry32{}))}
	if err := winapi.Process32First(handle, &entry); err != nil {
		if errors.Is(err, syscall.ERROR_NO_MORE_FILES) {
			return []ProcessDetails{}, nil
		}
		return nil, fmt.Errorf("read first process: %w", err)
	}

	result := make([]ProcessDetails, 0, 256)
	livePIDs := make(map[uint32]struct{}, 256)
	for {
		livePIDs[entry.ProcessID] = struct{}{}
		result = append(result, snapshotter.detailsForEntry(entry, now))
		entry.Size = uint32(unsafe.Sizeof(winapi.ProcessEntry32{}))
		if err := winapi.Process32Next(handle, &entry); err != nil {
			if errors.Is(err, syscall.ERROR_NO_MORE_FILES) {
				break
			}
			return nil, fmt.Errorf("read next process: %w", err)
		}
	}
	snapshotter.pruneCPUSamples(livePIDs)
	return result, nil
}

func (snapshotter *ProcessSnapshotter) detailsForEntry(entry winapi.ProcessEntry32, now time.Time) ProcessDetails {
	detail := ProcessDetails{Process: core.Process{
		PID:       entry.ProcessID,
		ParentPID: entry.ParentProcessID,
		Name:      winapi.UTF16ToString(entry.ExeFile[:]),
	}}
	process, err := winapi.OpenProcess(winapi.PROCESS_QUERY_LIMITED_INFORMATION|winapi.PROCESS_VM_READ, false, entry.ProcessID)
	if err != nil {
		process, err = winapi.OpenProcess(winapi.PROCESS_QUERY_LIMITED_INFORMATION, false, entry.ProcessID)
	}
	if err != nil {
		detail.MetadataError = err.Error()
		return detail
	}
	defer winapi.CloseHandle(process)
	detail.MetadataAccessible = true

	var created, exited, kernel, user winapi.Filetime
	if err := winapi.GetProcessTimes(process, &created, &exited, &kernel, &user); err == nil {
		detail.Process.CreatedAt = time.Unix(0, created.Nanoseconds()).UTC()
		ticks := filetimeTicks(kernel) + filetimeTicks(user)
		if cpu, available := snapshotter.cpuPercent(entry.ProcessID, detail.Process.CreatedAt, now, ticks); available {
			detail.Process.CPUPercent = cpu
			detail.Process.CPUAvailable = true
		}
	} else {
		detail.MetadataError = appendMetadataError(detail.MetadataError, "times", err)
	}

	pathBuffer := make([]uint16, 32768)
	pathLength := uint32(len(pathBuffer))
	if err := winapi.QueryFullProcessImageName(process, 0, &pathBuffer[0], &pathLength); err == nil {
		detail.ExecutablePath = winapi.UTF16ToString(pathBuffer[:pathLength])
	} else {
		detail.MetadataError = appendMetadataError(detail.MetadataError, "path", err)
	}
	if commandLine, err := queryCommandLine(process); err == nil {
		detail.Process.CommandLine = commandLine
	} else {
		detail.MetadataError = appendMetadataError(detail.MetadataError, "command line", err)
	}
	if workingSet, err := queryWorkingSet(process); err == nil {
		detail.Process.MemoryBytes = uint64(workingSet)
		detail.Process.MemoryAvailable = true
	} else {
		detail.MetadataError = appendMetadataError(detail.MetadataError, "memory", err)
	}
	return detail
}

func (snapshotter *ProcessSnapshotter) cpuPercent(pid uint32, createdAt, now time.Time, ticks uint64) (float64, bool) {
	snapshotter.mu.Lock()
	defer snapshotter.mu.Unlock()
	current := cpuSample{createdAt: createdAt, at: now, ticks: ticks}
	previous, exists := snapshotter.previous[pid]
	snapshotter.previous[pid] = current
	if !exists || !previous.createdAt.Equal(createdAt) || ticks < previous.ticks {
		return 0, false
	}
	elapsed := now.Sub(previous.at)
	if elapsed <= 0 {
		return 0, false
	}
	cpuDuration := time.Duration(ticks-previous.ticks) * 100 * time.Nanosecond
	return float64(cpuDuration) / float64(elapsed) * 100 / float64(runtime.NumCPU()), true
}

func (snapshotter *ProcessSnapshotter) pruneCPUSamples(livePIDs map[uint32]struct{}) {
	snapshotter.mu.Lock()
	defer snapshotter.mu.Unlock()
	for pid := range snapshotter.previous {
		if _, live := livePIDs[pid]; !live {
			delete(snapshotter.previous, pid)
		}
	}
}

func filetimeTicks(value winapi.Filetime) uint64 {
	return uint64(value.HighDateTime)<<32 | uint64(value.LowDateTime)
}

type processMemoryCounters struct {
	Size                       uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

func queryWorkingSet(process winapi.Handle) (uintptr, error) {
	counters := processMemoryCounters{Size: uint32(unsafe.Sizeof(processMemoryCounters{}))}
	result, _, callErr := procGetProcessMemoryInfo.Call(
		uintptr(process),
		uintptr(unsafe.Pointer(&counters)),
		uintptr(counters.Size),
	)
	if result == 0 {
		return 0, callErr
	}
	return counters.WorkingSetSize, nil
}

type unicodeString struct {
	Length        uint16
	MaximumLength uint16
	Buffer        *uint16
}

func queryCommandLine(process winapi.Handle) (string, error) {
	buffer := make([]byte, 64*1024)
	var returned uint32
	status, _, _ := procNtQueryInformationProcess.Call(
		uintptr(process),
		processCommandLineInformation,
		uintptr(unsafe.Pointer(&buffer[0])),
		uintptr(len(buffer)),
		uintptr(unsafe.Pointer(&returned)),
	)
	if int32(status) < 0 {
		return "", fmt.Errorf("NtQueryInformationProcess status 0x%x", uint32(status))
	}
	value := (*unicodeString)(unsafe.Pointer(&buffer[0]))
	if value.Buffer == nil || value.Length == 0 {
		return "", nil
	}
	return winapi.UTF16ToString(unsafe.Slice(value.Buffer, int(value.Length/2))), nil
}

func appendMetadataError(existing, field string, err error) string {
	message := field + ": " + err.Error()
	if existing == "" {
		return message
	}
	return existing + "; " + message
}
