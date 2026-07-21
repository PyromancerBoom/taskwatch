package windows

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	winapi "golang.org/x/sys/windows"

	"github.com/PyromancerBoom/taskwatch/internal/core"
)

const (
	jobObjectQuery              = 0x0004
	jobObjectBasicProcessIDList = 3
	processTerminateAccess      = 0x0001
	observedTerminationExitCode = 1
	launchCleanupExitCode       = 1
	jobKeeperHandleEnv          = "TASKWATCH_JOB_KEEPER_HANDLE"
)

var (
	kernel32          = winapi.NewLazySystemDLL("kernel32.dll")
	procOpenJobObject = kernel32.NewProc("OpenJobObjectW")
)

type nativeWindows struct{}

func CurrentProcessIdentity() (core.ProcessIdentity, error) {
	pid := winapi.GetCurrentProcessId()
	if pid == 0 {
		return core.ProcessIdentity{}, errors.New("current process PID is zero")
	}
	createdAt, err := (nativeWindows{}).processCreatedAt(nativeHandle(winapi.CurrentProcess()))
	if err != nil {
		return core.ProcessIdentity{}, fmt.Errorf("read current process creation time: %w", err)
	}
	if createdAt.IsZero() {
		return core.ProcessIdentity{}, errors.New("current process creation time is zero")
	}
	return core.ProcessIdentity{PID: pid, CreatedAt: createdAt}, nil
}

func (nativeWindows) createJob(name string) (nativeHandle, error) {
	namePointer, err := winapi.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	handle, err := winapi.CreateJobObject(nil, namePointer)
	return nativeHandle(handle), err
}

func (nativeWindows) openJob(name string) (nativeHandle, error) {
	namePointer, err := winapi.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	handle, _, callErr := procOpenJobObject.Call(
		uintptr(jobObjectQuery),
		0,
		uintptr(unsafe.Pointer(namePointer)),
	)
	if handle == 0 {
		return 0, callErr
	}
	return nativeHandle(handle), nil
}

func (nativeWindows) createSuspended(executable, workspace string) (nativeHandle, nativeHandle, uint32, error) {
	application := executable
	commandLine := quoteWindowsArgument(executable)
	extension := strings.ToLower(filepath.Ext(executable))
	if extension == ".cmd" || extension == ".bat" {
		application = os.Getenv("ComSpec")
		if application == "" {
			application = filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
		}
		commandLine = quoteWindowsArgument(application) + ` /d /s /c "` + quoteWindowsArgument(executable) + `"`
	}
	applicationPointer, err := winapi.UTF16PtrFromString(application)
	if err != nil {
		return 0, 0, 0, err
	}
	commandLinePointer, err := winapi.UTF16PtrFromString(commandLine)
	if err != nil {
		return 0, 0, 0, err
	}
	var workspacePointer *uint16
	if workspace != "" {
		workspacePointer, err = winapi.UTF16PtrFromString(workspace)
		if err != nil {
			return 0, 0, 0, err
		}
	}
	startup := winapi.StartupInfo{Cb: uint32(unsafe.Sizeof(winapi.StartupInfo{}))}
	var processInformation winapi.ProcessInformation
	flags := uint32(winapi.CREATE_SUSPENDED | winapi.CREATE_NEW_CONSOLE | winapi.CREATE_NEW_PROCESS_GROUP)
	if err := winapi.CreateProcess(
		applicationPointer,
		commandLinePointer,
		nil,
		nil,
		false,
		flags,
		nil,
		workspacePointer,
		&startup,
		&processInformation,
	); err != nil {
		return 0, 0, 0, err
	}
	return nativeHandle(processInformation.Process), nativeHandle(processInformation.Thread), processInformation.ProcessId, nil
}

func quoteWindowsArgument(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
}

func (nativeWindows) assignProcess(job, process nativeHandle) error {
	return winapi.AssignProcessToJobObject(winapi.Handle(job), winapi.Handle(process))
}

func (nativeWindows) startJobKeeper(job nativeHandle) error {
	keeperHandle, err := duplicateJobKeeperHandle(job)
	if err != nil {
		return err
	}
	defer winapi.CloseHandle(winapi.Handle(keeperHandle))

	executable, err := os.Executable()
	if err != nil {
		return err
	}
	command := exec.Command(executable)
	command.Env = append(os.Environ(), jobKeeperHandleEnv+"="+strconv.FormatUint(uint64(keeperHandle), 10))
	command.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:                 true,
		CreationFlags:              winapi.CREATE_NO_WINDOW,
		AdditionalInheritedHandles: []syscall.Handle{syscall.Handle(keeperHandle)},
	}
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}

func duplicateJobKeeperHandle(job nativeHandle) (nativeHandle, error) {
	var keeperHandle winapi.Handle
	if err := winapi.DuplicateHandle(
		winapi.CurrentProcess(),
		winapi.Handle(job),
		winapi.CurrentProcess(),
		&keeperHandle,
		jobObjectQuery,
		true,
		0,
	); err != nil {
		return 0, err
	}
	return nativeHandle(keeperHandle), nil
}

// RunJobKeeperIfRequested keeps the internal keeper process alive until every
// process assigned to its Job Object has exited.
func RunJobKeeperIfRequested() (bool, error) {
	value, requested := os.LookupEnv(jobKeeperHandleEnv)
	if !requested {
		return false, nil
	}
	rawHandle, err := strconv.ParseUint(value, 10, 64)
	if err != nil || rawHandle == 0 {
		return true, fmt.Errorf("invalid job keeper handle")
	}
	handle := winapi.Handle(rawHandle)
	defer winapi.CloseHandle(handle)
	for {
		pids, err := (nativeWindows{}).jobProcessIDs(nativeHandle(handle))
		if err != nil {
			return true, err
		}
		if len(pids) == 0 {
			return true, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (nativeWindows) resumeThread(thread nativeHandle) error {
	_, err := winapi.ResumeThread(winapi.Handle(thread))
	return err
}

func (nativeWindows) processCreatedAt(process nativeHandle) (time.Time, error) {
	var created, exited, kernel, user winapi.Filetime
	if err := winapi.GetProcessTimes(winapi.Handle(process), &created, &exited, &kernel, &user); err != nil {
		return time.Time{}, err
	}
	return time.Unix(0, created.Nanoseconds()).UTC(), nil
}

func (nativeWindows) terminateProcess(process nativeHandle) error {
	return winapi.TerminateProcess(winapi.Handle(process), launchCleanupExitCode)
}

func (nativeWindows) closeHandle(handle nativeHandle) {
	if handle != 0 {
		_ = winapi.CloseHandle(winapi.Handle(handle))
	}
}

func (nativeWindows) jobProcessIDs(job nativeHandle) ([]uint32, error) {
	entrySize := int(unsafe.Sizeof(uintptr(0)))
	capacity := 64
	for {
		buffer := make([]byte, 8+capacity*entrySize)
		err := winapi.QueryInformationJobObject(
			winapi.Handle(job),
			jobObjectBasicProcessIDList,
			uintptr(unsafe.Pointer(&buffer[0])),
			uint32(len(buffer)),
			nil,
		)
		assigned := *(*uint32)(unsafe.Pointer(&buffer[0]))
		inList := *(*uint32)(unsafe.Pointer(&buffer[4]))
		if err != nil && !errors.Is(err, syscall.ERROR_MORE_DATA) {
			return nil, err
		}
		if int(assigned) > capacity || errors.Is(err, syscall.ERROR_MORE_DATA) {
			capacity = int(assigned)
			if capacity < 2*int(inList) {
				capacity = 2 * int(inList)
			}
			if capacity == 0 {
				capacity = 64
			}
			continue
		}
		pids := make([]uint32, 0, inList)
		for i := 0; i < int(inList); i++ {
			offset := 8 + i*entrySize
			pid := *(*uintptr)(unsafe.Pointer(&buffer[offset]))
			pids = append(pids, uint32(pid))
		}
		return pids, nil
	}
}

func (nativeWindows) openProcessForTerminate(pid uint32) (nativeHandle, error) {
	handle, err := winapi.OpenProcess(processTerminateAccess|winapi.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	return nativeHandle(handle), err
}

func (nativeWindows) terminateOpenedProcess(process nativeHandle) error {
	if process == 0 {
		return fmt.Errorf("invalid process handle")
	}
	return winapi.TerminateProcess(winapi.Handle(process), observedTerminationExitCode)
}
