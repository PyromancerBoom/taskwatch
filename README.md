# TaskWatch: Process manager for AI coding agents

TaskWatch v0.1.0-preview is a Windows 10/11 x64 technical preview for inspecting AI coding-agent processes and launching supervised CLI sessions. It has no user-facing process-termination feature.

## What it supports

- Observation of the packaged Codex Desktop application's current validated process tree.
- Supervised launches of Claude Code and Codex CLI from a workspace you choose in TaskWatch.
- Read-only process-tree inspection, refresh, and restart recovery for eligible supervised sessions.

For CLI supervision, launch Claude Code or Codex CLI through TaskWatch. Existing CLI processes are not discovered or adopted.

## Prerequisites

- Windows 10 or Windows 11 on x64.
- Go 1.25 or later to build from source.
- Claude Code and/or Codex CLI on `PATH` if you plan to launch them from TaskWatch.

## Download (future release)

There is no downloadable binary, installer, or other release artifact yet. A future GitHub Release will provide the supported download path.

Early unsigned Windows preview builds may trigger a Microsoft Defender SmartScreen warning. When artifacts are published, download them only from the project's GitHub Releases page and verify any published checksum before proceeding.

## Build from source

All Go commands must use the repository wrapper, which keeps Go caches and temporary files inside `.cache/`:

```powershell
./scripts/go-local.ps1 -GoArgs @("build", "-o", ".\taskwatch.exe", "./cmd/taskwatch")
```

## Quick start

```powershell
.\taskwatch.exe
```

In the terminal UI:

- Press `c` to launch Claude Code, or `x` to launch Codex CLI.
- Choose a workspace (or enter one) for that new session.
- Press `Enter` to inspect the selected process tree.
- Press `r` to refresh immediately.
- Press `Ctrl+C` to close TaskWatch. It leaves all processes running.

Command-line information is also available without opening the UI:

```powershell
.\taskwatch.exe --help
.\taskwatch.exe --version
```

## State files

TaskWatch stores state under `%LOCALAPPDATA%\TaskWatch`:

- `sessions.json` contains schema-versioned metadata for supervised-session recovery.
- `workspaces.json` contains the independently versioned list of recent launch workspaces.

Codex Desktop observations are deliberately not persisted: they are valid only while TaskWatch can currently validate the desktop process family.

## Limitations of this preview

- There is no user-facing termination or safe-exit flow. The `k` and `q` keys do not terminate or close sessions outside the workspace editor.
- Only CLI sessions launched through TaskWatch are supervised; already-running Claude Code and Codex CLI sessions are not found.
- Restart recovery remains available while the supervised session's Job Object still has running processes.
- Codex Desktop observation covers only its current validated application tree, not historical sessions or detached/rootless processes.
- Protected or elevated processes can be visible but inaccessible to TaskWatch running as a standard user.
- Windows 10/11 x64 is the only supported preview target.

## Safety

TaskWatch exposes no process-cleanup or termination action in this preview. Its inspection views are read-only. Closing TaskWatch does not close supervised applications, and the Job Objects it uses are not configured to kill processes when TaskWatch exits.

The project’s safety model is designed around exact process identity (PID plus creation time), conservative handling of unreadable metadata, and explicit user confirmation before any future termination capability could be made available.

## Development

Use the wrapper for all Go commands:

```powershell
./scripts/go-local.ps1 build ./...
./scripts/go-local.ps1 test ./...
./scripts/go-local.ps1 vet ./...
./scripts/go-local.ps1 fmt ./...
```

Run one package's tests with, for example:

```powershell
./scripts/go-local.ps1 test ./internal/core
```

## Uninstall

Close TaskWatch with `Ctrl+C`, then remove any future downloaded executable or installation using its release-specific instructions. To remove TaskWatch's saved session and recent-workspace metadata, delete `%LOCALAPPDATA%\TaskWatch`:

```powershell
Remove-Item -LiteralPath "$env:LOCALAPPDATA\TaskWatch" -Recurse
```

Removing this state does not terminate any CLI or desktop processes.

## Contributing, security, and license

See [Contributing](CONTRIBUTING.md), [Security](SECURITY.md), [third-party notices](THIRD_PARTY_NOTICES.md), and the [MIT License](LICENSE).
