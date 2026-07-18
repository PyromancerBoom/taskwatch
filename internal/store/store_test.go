package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"taskwatch/internal/core"
)

func TestSaveAndLoadRoundTripSupervisedSessionsOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "sessions.json")
	started := time.Date(2026, time.July, 15, 12, 30, 0, 0, time.UTC)
	rootCreated := started.Add(-5 * time.Second)
	sessions := []core.Session{
		{
			ID:         "supervised-1",
			Tool:       core.ToolCodexCLI,
			Confidence: core.ConfidenceSupervised,
			JobName:    "tests",
			Workspace:  `C:\work\taskwatch`,
			Root:       core.ProcessIdentity{PID: 42, CreatedAt: rootCreated},
			StartedAt:  started,
		},
		{
			ID:         "observed-1",
			Tool:       core.ToolClaudeCLI,
			Confidence: core.ConfidenceObserved,
			Root:       core.ProcessIdentity{PID: 99, CreatedAt: started},
			StartedAt:  started,
		},
	}

	if err := Save(path, sessions); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Load() returned %d sessions, want 1", len(got))
	}
	if got[0].ID != sessions[0].ID || got[0].Tool != sessions[0].Tool || got[0].JobName != sessions[0].JobName || got[0].Workspace != sessions[0].Workspace {
		t.Fatalf("Load() session = %#v, want persisted fields from %#v", got[0], sessions[0])
	}
	if got[0].Confidence != core.ConfidenceSupervised {
		t.Fatalf("Load() confidence = %q, want %q", got[0].Confidence, core.ConfidenceSupervised)
	}
	if got[0].Root.PID != 42 || !got[0].Root.CreatedAt.Equal(rootCreated) || !got[0].StartedAt.Equal(started) {
		t.Fatalf("Load() identity/times = %#v, want PID 42 created at %v and session started at %v", got[0], rootCreated, started)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !strings.Contains(string(raw), `"schema_version": 1`) {
		t.Fatalf("saved JSON does not declare schema version 1: %s", raw)
	}
	if strings.Contains(string(raw), "observed-1") {
		t.Fatalf("saved JSON contains observed session: %s", raw)
	}
	if matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".sessions.json.tmp-*")); err != nil || len(matches) != 0 {
		t.Fatalf("temporary save files = %v, error = %v", matches, err)
	}
}

func TestLoadMissingFileReturnsEmpty(t *testing.T) {
	got, err := Load(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Load() = %#v, want empty", got)
	}
}

func TestLoadRejectsUnsupportedSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":2,"sessions":[]}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if _, err := Load(path); err == nil {
		t.Fatal("Load() accepted unsupported schema version")
	}
}

func TestLoadKeepsMetadataForJobRecoveryWithoutALiveRootSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	created := time.Unix(100, 0).UTC()
	session := core.Session{
		ID:         "leftovers",
		Tool:       core.ToolCodexCLI,
		Confidence: core.ConfidenceSupervised,
		JobName:    `Local\TaskWatch-leftovers`,
		Root:       core.ProcessIdentity{PID: 42, CreatedAt: created},
		StartedAt:  created,
	}
	if err := Save(path, []core.Session{session}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(got) != 1 || got[0].ID != session.ID || got[0].JobName != session.JobName || !core.SameProcess(got[0].Root, session.Root) {
		t.Fatalf("Load() = %#v, want metadata retained for named-job recovery", got)
	}
}

func TestSaveAndLoadWorkspacesRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "workspaces.json")
	recent := []string{`C:\work\taskwatch`, `C:\work\website`, `D:\scratch`}

	if err := SaveWorkspaces(path, recent); err != nil {
		t.Fatalf("SaveWorkspaces() error = %v", err)
	}
	got, err := LoadWorkspaces(path)
	if err != nil {
		t.Fatalf("LoadWorkspaces() error = %v", err)
	}
	if strings.Join(got, "|") != strings.Join(recent, "|") {
		t.Fatalf("LoadWorkspaces() = %#v, want %#v", got, recent)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !strings.Contains(string(raw), `"schema_version": 1`) {
		t.Fatalf("saved JSON does not declare schema version 1: %s", raw)
	}
}

func TestSaveWorkspacesEmptyListRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspaces.json")
	if err := SaveWorkspaces(path, nil); err != nil {
		t.Fatalf("SaveWorkspaces(nil) error = %v", err)
	}
	got, err := LoadWorkspaces(path)
	if err != nil {
		t.Fatalf("LoadWorkspaces() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("LoadWorkspaces() = %#v, want empty", got)
	}
}

func TestLoadWorkspacesMissingFileReturnsEmpty(t *testing.T) {
	got, err := LoadWorkspaces(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatalf("LoadWorkspaces() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("LoadWorkspaces() = %#v, want empty", got)
	}
}

func TestLoadWorkspacesRejectsUnsupportedSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspaces.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":2,"recent_workspaces":[]}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if _, err := LoadWorkspaces(path); err == nil {
		t.Fatal("LoadWorkspaces() accepted unsupported schema version")
	}
}
