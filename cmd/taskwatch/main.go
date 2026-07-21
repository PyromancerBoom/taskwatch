package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	tea "charm.land/bubbletea/v2"

	"taskwatch/internal/app"
	"taskwatch/internal/platform/windows"
	"taskwatch/internal/tui"
)

const version = "0.1.0-preview"

const helpText = `TaskWatch v0.1.0-preview — Windows technical preview

Inspect packaged Codex Desktop as one application process tree.

Start a tracked CLI session:
  1. Run taskwatch.
  2. Press c for Claude or x for Codex CLI.
  3. Choose a recent workspace or enter a folder.

Only sessions launched this way are supervised.
Already-running CLI sessions are not discovered.
The selected CLI must be available on PATH.

Restart recovery requires the launched CLI root process to still be running.
Session termination controls are unavailable in v0.1.

Usage:
  taskwatch
  taskwatch --help
  taskwatch --version

State: %LocalAppData%\TaskWatch\sessions.json
`

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
	selfIdentity, err := windows.CurrentProcessIdentity()
	if err != nil {
		return fmt.Errorf("resolve current process identity: %w", err)
	}

	store := app.NewFileStore(paths.statePath)
	supervisor := windows.NewSupervisor()
	observer := windows.NewObserver(nil)
	coordinator := app.NewCoordinator(paths.workspace, store, supervisor, observer, selfIdentity)
	defer coordinator.Close()

	model := tui.NewModel(coordinator, tui.DefaultRefreshInterval)
	if _, err := tea.NewProgram(model).Run(); err != nil {
		return fmt.Errorf("run TaskWatch: %w", err)
	}
	return nil
}

func execute(args []string, stdout, stderr io.Writer, start func() error) int {
	if len(args) > 0 {
		if len(args) > 1 {
			fmt.Fprintf(stderr, "TaskWatch: expected at most one argument, got %d\nRun 'taskwatch --help' for usage.\n", len(args))
			return 2
		}
		switch args[0] {
		case "--help", "-h":
			fmt.Fprint(stdout, helpText)
			return 0
		case "--version":
			fmt.Fprintf(stdout, "TaskWatch v%s\n", version)
			return 0
		}
		fmt.Fprintf(stderr, "TaskWatch: unknown argument %q\nRun 'taskwatch --help' for usage.\n", args[0])
		return 2
	}
	if err := start(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func main() {
	if requested, err := windows.RunJobKeeperIfRequested(); requested {
		if err != nil {
			os.Exit(1)
		}
		return
	}
	os.Exit(execute(os.Args[1:], os.Stdout, os.Stderr, run))
}
