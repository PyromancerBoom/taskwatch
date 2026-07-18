package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"taskwatch/internal/core"
)

const SchemaVersion = 1

type fileData struct {
	SchemaVersion int             `json:"schema_version"`
	Sessions      []sessionRecord `json:"sessions"`
}

// sessionRecord deliberately contains only restart-safe supervised metadata.
type sessionRecord struct {
	SessionID     string    `json:"session_id"`
	Tool          core.Tool `json:"tool"`
	JobName       string    `json:"job_name"`
	Workspace     string    `json:"workspace"`
	RootPID       uint32    `json:"root_pid"`
	RootCreatedAt time.Time `json:"root_created_at"`
	StartTime     time.Time `json:"start_time"`
}

// Save atomically replaces path with a version-one JSON snapshot. Observed
// sessions are never persisted.
func Save(path string, sessions []core.Session) error {
	records := make([]sessionRecord, 0, len(sessions))
	for _, session := range sessions {
		if session.Confidence != core.ConfidenceSupervised {
			continue
		}
		startedAt := session.StartedAt
		if startedAt.IsZero() {
			startedAt = session.Root.CreatedAt
		}
		records = append(records, sessionRecord{
			SessionID:     session.ID,
			Tool:          session.Tool,
			JobName:       session.JobName,
			Workspace:     session.Workspace,
			RootPID:       session.Root.PID,
			RootCreatedAt: session.Root.CreatedAt,
			StartTime:     startedAt,
		})
	}

	payload, err := json.MarshalIndent(fileData{SchemaVersion: SchemaVersion, Sessions: records}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session store: %w", err)
	}
	payload = append(payload, '\n')

	if err := atomicWrite(path, payload); err != nil {
		return fmt.Errorf("persist session store: %w", err)
	}
	return nil
}

// atomicWrite replaces path with payload through a temporary file, a flush,
// and an atomic rename, creating the parent directory when needed. The
// temporary file is secured to 0600 before any data is written.
func atomicWrite(path string, payload []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create store directory: %w", err)
	}
	temporary, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary store: %w", err)
	}
	temporaryPath := temporary.Name()
	keepTemporary := false
	defer func() {
		if !keepTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()

	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure temporary store: %w", err)
	}
	if _, err := temporary.Write(payload); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write temporary store: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync temporary store: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary store: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace store: %w", err)
	}
	keepTemporary = true
	return nil
}

// Load reads a version-one store. A missing store is an empty store.
func Load(path string) ([]core.Session, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return []core.Session{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open session store: %w", err)
	}
	defer file.Close()

	var data fileData
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&data); err != nil {
		return nil, fmt.Errorf("decode session store: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, err
	}
	if data.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("unsupported session store schema version %d", data.SchemaVersion)
	}

	sessions := make([]core.Session, len(data.Sessions))
	for i, record := range data.Sessions {
		rootCreatedAt := record.RootCreatedAt
		if rootCreatedAt.IsZero() {
			rootCreatedAt = record.StartTime
		}
		sessions[i] = core.Session{
			ID:         record.SessionID,
			Tool:       record.Tool,
			Confidence: core.ConfidenceSupervised,
			JobName:    record.JobName,
			Workspace:  record.Workspace,
			Root:       core.ProcessIdentity{PID: record.RootPID, CreatedAt: rootCreatedAt},
			StartedAt:  record.StartTime,
		}
	}
	return sessions, nil
}

// WorkspacesSchemaVersion is the on-disk schema for the recent-workspaces
// sidecar file. It is versioned independently of the session store.
const WorkspacesSchemaVersion = 1

type workspacesData struct {
	SchemaVersion    int      `json:"schema_version"`
	RecentWorkspaces []string `json:"recent_workspaces"`
}

// SaveWorkspaces atomically writes the recent-workspaces list. It is stored in
// a separate file from the session store so that session-recovery metadata is
// never coupled to this convenience list.
func SaveWorkspaces(path string, recent []string) error {
	if recent == nil {
		recent = []string{}
	}
	payload, err := json.MarshalIndent(workspacesData{SchemaVersion: WorkspacesSchemaVersion, RecentWorkspaces: recent}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode workspaces store: %w", err)
	}
	payload = append(payload, '\n')

	if err := atomicWrite(path, payload); err != nil {
		return fmt.Errorf("persist workspaces store: %w", err)
	}
	return nil
}

// LoadWorkspaces reads the recent-workspaces list. A missing file is an empty
// list; an unsupported schema version fails loudly.
func LoadWorkspaces(path string) ([]string, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open workspaces store: %w", err)
	}
	defer file.Close()

	var data workspacesData
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&data); err != nil {
		return nil, fmt.Errorf("decode workspaces store: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, err
	}
	if data.SchemaVersion != WorkspacesSchemaVersion {
		return nil, fmt.Errorf("unsupported workspaces store schema version %d", data.SchemaVersion)
	}
	if data.RecentWorkspaces == nil {
		return []string{}, nil
	}
	return data.RecentWorkspaces, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("decode trailing session store data: %w", err)
	}
	return fmt.Errorf("session store contains multiple JSON values")
}
