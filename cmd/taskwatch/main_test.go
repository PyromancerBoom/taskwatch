package main

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecuteShowsHelpWithoutStartingTaskWatch(t *testing.T) {
	var stdout, stderr bytes.Buffer
	started := false

	exitCode := execute([]string{"--help"}, &stdout, &stderr, func() error {
		started = true
		return nil
	})

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0", exitCode)
	}
	if started {
		t.Fatal("TaskWatch started while handling --help")
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	wantQuickStart := `Start a tracked CLI session:
  1. Run taskwatch.
  2. Press c for Claude or x for Codex CLI.
  3. Choose a recent workspace or enter a folder.

Only sessions launched this way are supervised.
Already-running CLI sessions are not discovered.
The selected CLI must be available on PATH.`
	if !strings.Contains(stdout.String(), wantQuickStart) {
		t.Errorf("help missing launch instructions:\n%s", stdout.String())
	}
	for _, want := range []string{
		"TaskWatch v0.1.0-preview",
		"Windows technical preview",
		"Codex Desktop as one application process tree",
		"Restart recovery requires the launched CLI root process to still be running",
		"Session termination controls are unavailable in v0.1",
		`%LocalAppData%\TaskWatch\sessions.json`,
		"taskwatch --version",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("help missing %q:\n%s", want, stdout.String())
		}
	}
	if strings.Contains(stdout.String(), "never terminates processes") {
		t.Fatalf("help overstates launch-failure cleanup behavior:\n%s", stdout.String())
	}
}

func TestExecuteShowsVersionWithoutStartingTaskWatch(t *testing.T) {
	var stdout, stderr bytes.Buffer
	started := false

	exitCode := execute([]string{"--version"}, &stdout, &stderr, func() error {
		started = true
		return nil
	})

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0", exitCode)
	}
	if started {
		t.Fatal("TaskWatch started while handling --version")
	}
	if got, want := stdout.String(), "TaskWatch v0.1.0-preview\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestExecuteRejectsArgumentsWithoutStartingTaskWatch(t *testing.T) {
	var stdout, stderr bytes.Buffer
	started := false

	exitCode := execute([]string{"--unknown"}, &stdout, &stderr, func() error {
		started = true
		return nil
	})

	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2", exitCode)
	}
	if started {
		t.Fatal("TaskWatch started for an unknown argument")
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	for _, want := range []string{`unknown argument "--unknown"`, "taskwatch --help"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr missing %q: %s", want, stderr.String())
		}
	}
}

func TestExecuteRejectsExtraArgumentsWithoutMislabelingHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	started := false

	exitCode := execute([]string{"--help", "stray"}, &stdout, &stderr, func() error {
		started = true
		return nil
	})

	if exitCode != 2 || started {
		t.Fatalf("exit code = %d, started = %v; want 2, false", exitCode, started)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "expected at most one argument, got 2") {
		t.Fatalf("stderr does not diagnose extra arguments: %s", stderr.String())
	}
	if strings.Contains(stderr.String(), `unknown argument "--help"`) {
		t.Fatalf("stderr mislabels --help as unknown: %s", stderr.String())
	}
}

func TestExecuteStartsTaskWatchWithoutArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	started := false

	exitCode := execute(nil, &stdout, &stderr, func() error {
		started = true
		return nil
	})

	if exitCode != 0 || !started {
		t.Fatalf("exit code = %d, started = %v; want 0, true", exitCode, started)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("stdout = %q, stderr = %q; want both empty", stdout.String(), stderr.String())
	}
}

func TestExecuteReportsStartupFailure(t *testing.T) {
	var stdout, stderr bytes.Buffer
	wantErr := errors.New("startup failed")

	exitCode := execute(nil, &stdout, &stderr, func() error { return wantErr })

	if exitCode != 1 {
		t.Fatalf("exit code = %d, want 1", exitCode)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if got, want := stderr.String(), "startup failed\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}

func TestResolveStartupPathsUsesWorkspaceAndTaskWatchStatePath(t *testing.T) {
	workspace := filepath.Join("C:", "work", "project")
	cacheRoot := filepath.Join("C:", "Users", "developer", "AppData", "Local")

	paths, err := resolveStartupPaths(
		func() (string, error) { return workspace, nil },
		func() (string, error) { return cacheRoot, nil },
	)
	if err != nil {
		t.Fatalf("resolve startup paths: %v", err)
	}
	if paths.workspace != workspace {
		t.Fatalf("workspace = %q, want %q", paths.workspace, workspace)
	}
	wantStatePath := filepath.Join(cacheRoot, "TaskWatch", "sessions.json")
	if paths.statePath != wantStatePath {
		t.Fatalf("state path = %q, want %q", paths.statePath, wantStatePath)
	}
}

func TestResolveStartupPathsReportsWorkspaceError(t *testing.T) {
	wantErr := errors.New("working directory unavailable")

	_, err := resolveStartupPaths(
		func() (string, error) { return "", wantErr },
		func() (string, error) { return "unused", nil },
	)
	if !errors.Is(err, wantErr) || !strings.Contains(err.Error(), "resolve workspace") {
		t.Fatalf("error = %v, want wrapped workspace error", err)
	}
}

func TestResolveStartupPathsReportsCacheRootError(t *testing.T) {
	wantErr := errors.New("cache directory unavailable")

	_, err := resolveStartupPaths(
		func() (string, error) { return "workspace", nil },
		func() (string, error) { return "", wantErr },
	)
	if !errors.Is(err, wantErr) || !strings.Contains(err.Error(), "resolve local app data") {
		t.Fatalf("error = %v, want wrapped cache-root error", err)
	}
}
