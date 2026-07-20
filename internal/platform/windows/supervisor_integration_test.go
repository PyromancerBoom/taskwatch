package windows

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"taskwatch/internal/core"
)

const (
	jobHelperRoleEnv = "TASKWATCH_TEST_JOB_HELPER_ROLE"
	jobHelperDirEnv  = "TASKWATCH_TEST_JOB_HELPER_DIR"
	jobHelperRoot    = "root"
	jobHelperChild   = "child"
	jobHelperLeaf    = "grandchild"
)

func TestMain(m *testing.M) {
	if role := os.Getenv(jobHelperRoleEnv); role != "" {
		os.Exit(runJobHelper(role, os.Getenv(jobHelperDirEnv)))
	}
	os.Exit(m.Run())
}

// TestSupervisorRealJobLifecycle proves the Windows behavior that the fake
// native tests cannot: descendants inherit the real Job Object, closing
// TaskWatch's handle leaves them alive, and a fresh Supervisor can recover the
// named job while the root-held handle still exists. After recovery, the fresh
// Supervisor keeps the job available when the root exits and descendants are
// left over. Helpers stop cooperatively; this test never terminates a process.
func TestSupervisorRealJobLifecycle(t *testing.T) {
	helperDir := t.TempDir()
	launched := false
	t.Cleanup(func() { stopAndWaitForJobHelpers(t, helperDir, launched) })

	currentExecutable, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test executable: %v", err)
	}
	helperExecutable := filepath.Join(helperDir, "codex.exe")
	if err := copyTestExecutable(currentExecutable, helperExecutable); err != nil {
		t.Fatalf("copy helper executable: %v", err)
	}

	t.Setenv(jobHelperRoleEnv, jobHelperRoot)
	t.Setenv(jobHelperDirEnv, helperDir)

	first := newSupervisor(
		&fakeResolver{paths: map[string]string{"codex": helperExecutable}},
		nativeWindows{},
		NewProcessSnapshotter(),
	)
	t.Cleanup(first.Close)

	session, err := first.Launch(core.ToolCodexCLI, helperDir)
	if err != nil {
		t.Fatalf("launch real supervised helper: %v", err)
	}
	launched = true
	pids := waitForHelperPIDs(t, helperDir)
	if pids[jobHelperRoot] != session.Root.PID {
		t.Fatalf("root PID file = %d, session root = %d", pids[jobHelperRoot], session.Root.PID)
	}
	wantAll := sortedPIDs(pids[jobHelperRoot], pids[jobHelperChild], pids[jobHelperLeaf])

	running := waitForSession(t, 10*time.Second, func() (core.Session, error) {
		return first.Refresh(session)
	}, func(got core.Session) bool {
		return got.State == core.StateRunning && containsAllPIDs(sessionPIDs(got), wantAll)
	})
	if running.Confidence != core.ConfidenceSupervised {
		t.Fatalf("running confidence = %q, want supervised", running.Confidence)
	}

	first.Close()

	second := newSupervisor(execResolver{}, nativeWindows{}, NewProcessSnapshotter())
	t.Cleanup(second.Close)
	recovered, err := second.Recover(session)
	if err != nil {
		t.Fatalf("recover named job after closing original supervisor: %v", err)
	}
	if recovered.State != core.StateRunning || !containsAllPIDs(sessionPIDs(recovered), wantAll) {
		t.Fatalf("recovered session = state %q PIDs %v, want running with helpers %v", recovered.State, sessionPIDs(recovered), wantAll)
	}

	writeHelperSignal(t, helperDir, jobHelperRoot+".stop")
	wantLeftovers := sortedPIDs(pids[jobHelperChild], pids[jobHelperLeaf])
	leftovers := waitForSession(t, 10*time.Second, func() (core.Session, error) {
		return second.Refresh(recovered)
	}, func(got core.Session) bool {
		gotPIDs := sessionPIDs(got)
		return got.State == core.StateLeftovers && !containsPID(gotPIDs, pids[jobHelperRoot]) && containsAllPIDs(gotPIDs, wantLeftovers)
	})
	if leftovers.Root != session.Root {
		t.Fatalf("leftover root identity changed: got %#v, want %#v", leftovers.Root, session.Root)
	}

	writeHelperSignal(t, helperDir, jobHelperChild+".stop")
	writeHelperSignal(t, helperDir, jobHelperLeaf+".stop")
	waitForSession(t, 10*time.Second, func() (core.Session, error) {
		return second.Refresh(leftovers)
	}, func(got core.Session) bool {
		return got.State == core.StateExited && len(got.Processes) == 0
	})
}

func runJobHelper(role, directory string) int {
	if directory == "" {
		return 2
	}
	donePath := filepath.Join(directory, role+".done")
	defer func() { _ = os.WriteFile(donePath, []byte("done"), 0o600) }()

	nextRole := ""
	switch role {
	case jobHelperRoot:
		nextRole = jobHelperChild
	case jobHelperChild:
		nextRole = jobHelperLeaf
	case jobHelperLeaf:
	default:
		return 2
	}
	pidPath := filepath.Join(directory, role+".pid")
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		return 5
	}
	if nextRole != "" {
		executable, err := os.Executable()
		if err != nil {
			return 3
		}
		command := exec.Command(executable)
		command.Env = helperEnvironment(nextRole, directory)
		if err := command.Start(); err != nil {
			return 4
		}
		if err := os.WriteFile(filepath.Join(directory, nextRole+".pid"), []byte(strconv.Itoa(command.Process.Pid)), 0o600); err != nil {
			_ = command.Process.Release()
			return 5
		}
		_ = command.Process.Release()
	}
	deadline := time.Now().Add(30 * time.Second)
	stopPath := filepath.Join(directory, role+".stop")
	for time.Now().Before(deadline) {
		if _, err := os.Stat(stopPath); err == nil {
			return 0
		} else if !os.IsNotExist(err) {
			return 6
		}
		time.Sleep(20 * time.Millisecond)
	}
	return 7
}

func helperEnvironment(role, directory string) []string {
	environment := make([]string, 0, len(os.Environ())+2)
	for _, value := range os.Environ() {
		name, _, _ := strings.Cut(value, "=")
		if strings.EqualFold(name, jobHelperRoleEnv) || strings.EqualFold(name, jobHelperDirEnv) {
			continue
		}
		environment = append(environment, value)
	}
	return append(environment, jobHelperRoleEnv+"="+role, jobHelperDirEnv+"="+directory)
}

func copyTestExecutable(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.Create(destination)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return err
	}
	return output.Close()
}

func waitForHelperPIDs(t *testing.T, directory string) map[string]uint32 {
	t.Helper()
	roles := []string{jobHelperRoot, jobHelperChild, jobHelperLeaf}
	result := make(map[string]uint32, len(roles))
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, role := range roles {
			if result[role] != 0 {
				continue
			}
			value, err := os.ReadFile(filepath.Join(directory, role+".pid"))
			if err != nil {
				continue
			}
			pid, err := strconv.ParseUint(string(value), 10, 32)
			if err == nil && pid != 0 {
				result[role] = uint32(pid)
			}
		}
		if len(result) == len(roles) {
			return result
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("helpers did not become ready: got %v", result)
	return nil
}

func waitForSession(t *testing.T, timeout time.Duration, refresh func() (core.Session, error), ready func(core.Session) bool) core.Session {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last core.Session
	var lastErr error
	for time.Now().Before(deadline) {
		last, lastErr = refresh()
		if lastErr == nil && ready(last) {
			return last
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("session did not reach expected state: last state %q PIDs %v error %v", last.State, sessionPIDs(last), lastErr)
	return core.Session{}
}

func sessionPIDs(session core.Session) []uint32 {
	pids := make([]uint32, 0, len(session.Processes))
	for _, process := range session.Processes {
		pids = append(pids, process.PID)
	}
	sort.Slice(pids, func(i, j int) bool { return pids[i] < pids[j] })
	return pids
}

func sortedPIDs(pids ...uint32) []uint32 {
	sort.Slice(pids, func(i, j int) bool { return pids[i] < pids[j] })
	return pids
}

func containsAllPIDs(have, want []uint32) bool {
	for _, pid := range want {
		if !containsPID(have, pid) {
			return false
		}
	}
	return true
}

func containsPID(pids []uint32, want uint32) bool {
	for _, pid := range pids {
		if pid == want {
			return true
		}
	}
	return false
}

func writeHelperSignal(t *testing.T, directory, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, name), []byte("stop"), 0o600); err != nil {
		t.Fatalf("write helper signal %s: %v", name, err)
	}
}

func stopAndWaitForJobHelpers(t *testing.T, directory string, launched bool) {
	t.Helper()
	if !launched {
		return
	}
	roles := []string{jobHelperRoot, jobHelperChild, jobHelperLeaf}
	for _, role := range roles {
		_ = os.WriteFile(filepath.Join(directory, role+".stop"), []byte("stop"), 0o600)
	}
	deadline := time.Now().Add(5 * time.Second)
	for _, role := range roles {
		if role != jobHelperRoot {
			if _, err := os.Stat(filepath.Join(directory, role+".pid")); err != nil {
				return
			}
		}
		for time.Now().Before(deadline) {
			if _, err := os.Stat(filepath.Join(directory, role+".done")); err == nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if _, err := os.Stat(filepath.Join(directory, role+".done")); err != nil {
			t.Logf("helper cleanup timed out in %s; helpers retain their 30-second self-timeout", directory)
			return
		}
	}
}
