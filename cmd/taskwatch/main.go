package main

import (
	"fmt"
	"os"
	"path/filepath"

	tea "charm.land/bubbletea/v2"

	"taskwatch/internal/app"
	"taskwatch/internal/platform/windows"
	"taskwatch/internal/tui"
)

type startupPaths struct {
	workspace string
	statePath string
}

func resolveStartupPaths(getwd, userCacheDir func() (string, error)) (startupPaths, error) {
	workspace, err := getwd()
	if err != nil {
		return startupPaths{}, fmt.Errorf("resolve workspace: %w", err)
	}
	cacheRoot, err := userCacheDir()
	if err != nil {
		return startupPaths{}, fmt.Errorf("resolve local app data: %w", err)
	}
	return startupPaths{
		workspace: workspace,
		statePath: filepath.Join(cacheRoot, "TaskWatch", "sessions.json"),
	}, nil
}

func run() error {
	paths, err := resolveStartupPaths(os.Getwd, os.UserCacheDir)
	if err != nil {
		return err
	}

	store := app.NewFileStore(paths.statePath)
	supervisor := windows.NewSupervisor()
	observer := windows.NewObserver(nil)
	coordinator := app.NewCoordinator(paths.workspace, store, supervisor, observer)
	defer coordinator.Close()

	model := tui.NewModel(coordinator, tui.DefaultRefreshInterval)
	if _, err := tea.NewProgram(model).Run(); err != nil {
		return fmt.Errorf("run TaskWatch: %w", err)
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
