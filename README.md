# TaskWatch: Process manager for AI coding agents

TaskWatch v0.1.0-preview is a Windows 10/11 x64 technical preview for inspecting AI coding-agent processes and launching supervised CLI sessions. It has no user-facing process-termination feature.

## What it supports

- Read-only auto-detection of the packaged Codex Desktop application and external Codex CLI agent processes.
- Supervised launches of Claude Code and Codex CLI from a workspace you choose in TaskWatch.
- Recent, redacted Codex activity (state, tool, model, and context usage) from active rollout files.
- Human-readable session inspection: an advisory lifecycle assessment, semantic process-role breakdown, current-run Codex activity, and the exact process tree.
- Read-only process-tree inspection, refresh, and restart recovery for eligible supervised sessions.

For Job Object supervision, launch Claude Code or Codex CLI through TaskWatch. Existing Codex CLI processes are detected automatically but remain observed and ephemeral; they are never adopted into supervision. Existing Claude Code processes are not discovered.

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
- Press `Enter` to inspect the selected session. Inspection is one scrollable document: session context, an advisory assessment, current-run Codex activity, a semantic resource breakdown, and the process tree.
- Press `t` during inspection to toggle the exact raw process tree.
- Press `r` to refresh immediately.
- Press `Ctrl+C` to close TaskWatch. It leaves all processes running.

### Reading an inspection

- **Assessment** is advisory only and never offers a cleanup action. `ACTIVE` and `FINISHED` mean no cleanup is suggested; `NEEDS REVIEW` appears *only* for a proven supervised leftover (a session TaskWatch launched whose original process exited while validated members of its Job Object remain); `UNKNOWN` covers stale, inaccessible, or internally inconsistent sessions. Every verdict lists the exact reasons TaskWatch reached it, and the assessment is derived purely from lifecycle state and exact process identity — activity and role labels never influence it.
- **Semantic roles** group each validated process into exactly one role (agent runtime, shell command, test runner, development server, browser automation, MCP/tool server, code intelligence, or background helper). Role counts and resource totals always add up to the session's exact process set; the labels are display-only.
- **Agent activity** history is Codex-only and kept in memory for the current TaskWatch run: it survives a Codex signal briefly leaving the 15-minute rollout window (shown as retained rather than current) but is never persisted and resets when the session or its root identity changes, or when TaskWatch restarts. Claude inspection states that the activity signal is unavailable in this preview. Missing activity is never rendered as idle, finished, or safe to stop.

Command-line information is also available without opening the UI:

```powershell
.\taskwatch.exe --help
.\taskwatch.exe --version
```

## State files

TaskWatch stores state under `%LOCALAPPDATA%\TaskWatch`:

- `sessions.json` contains schema-versioned metadata for supervised-session recovery.
- `workspaces.json` contains the independently versioned list of recent launch workspaces.

Observed Codex Desktop/CLI sessions and rollout activity are deliberately not persisted: they are valid only while TaskWatch can currently validate the process family and recent activity files.

## Limitations of this preview

- There is no user-facing termination or safe-exit flow. The `k` and `q` keys do not terminate or close sessions outside the workspace editor.
- Only CLI sessions launched through TaskWatch are supervised. Already-running Codex CLI sessions are observed automatically; already-running Claude Code sessions are not found.
- Restart recovery remains available while the supervised session's Job Object still has running processes.
- Codex Desktop observation covers only its current validated application tree, not historical sessions or detached/rootless processes.
- Codex activity reads only active `.jsonl` rollout files from the last 15 minutes. Cold `.zst` history and subagent rollouts are deliberately skipped.
- Prompts, assistant text, tool-argument values, command lines, cumulative token/cost analytics, and subagent hierarchy are never displayed or retained as activity.
- Protected or elevated processes can be visible but inaccessible to TaskWatch running as a standard user.
- Windows 10/11 x64 is the only supported preview target.

## Safety

TaskWatch exposes no process-cleanup or termination action in this preview. Its inspection views and rollout enrichment are read-only. The advisory assessment (including `NEEDS REVIEW`), semantic role labels, and activity history are display-only: they add no cleanup key, confirmation flow, or termination target. Activity correlation, including visibly labeled inferred matches, never changes process identity, lifecycle, resources, persistence, or any future termination decision. Closing TaskWatch does not close supervised applications, and the Job Objects it uses are not configured to kill processes when TaskWatch exits.

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
