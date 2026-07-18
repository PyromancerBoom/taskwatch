package app

import (
	"fmt"
	"path/filepath"

	"taskwatch/internal/core"
	"taskwatch/internal/store"
)

type FileStore struct {
	path           string
	workspacesPath string
}

func NewFileStore(path string) *FileStore {
	return &FileStore{
		path:           path,
		workspacesPath: filepath.Join(filepath.Dir(path), "workspaces.json"),
	}
}

func (fileStore *FileStore) Load() ([]core.Session, error) {
	sessions, err := store.Load(fileStore.path)
	if err != nil {
		return nil, fmt.Errorf("load session store %s: %w", fileStore.path, err)
	}
	return sessions, nil
}

func (fileStore *FileStore) Save(sessions []core.Session) error {
	if err := store.Save(fileStore.path, sessions); err != nil {
		return fmt.Errorf("save session store %s: %w", fileStore.path, err)
	}
	return nil
}

func (fileStore *FileStore) LoadRecentWorkspaces() ([]string, error) {
	recent, err := store.LoadWorkspaces(fileStore.workspacesPath)
	if err != nil {
		return nil, fmt.Errorf("load workspaces store %s: %w", fileStore.workspacesPath, err)
	}
	return recent, nil
}

func (fileStore *FileStore) SaveRecentWorkspaces(recent []string) error {
	if err := store.SaveWorkspaces(fileStore.workspacesPath, recent); err != nil {
		return fmt.Errorf("save workspaces store %s: %w", fileStore.workspacesPath, err)
	}
	return nil
}
