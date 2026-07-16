package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

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
