package app

import (
	"fmt"

	"taskwatch/internal/core"
	"taskwatch/internal/store"
)

type FileStore struct {
	path string
}

func NewFileStore(path string) *FileStore {
	return &FileStore{path: path}
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
