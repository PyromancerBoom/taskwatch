# AGENTS.md

TaskWatch is a Windows tool for finding and safely cleaning up processes left
behind by Claude Code, Codex CLI, and the Codex desktop app. Written in Go with
a Bubble Tea terminal UI. See [docs/system-design.md](docs/system-design.md)
for the full architecture.

## Commands

All Go commands go through `scripts/go-local.ps1`, which pins every cache,
module download, and temp file inside the git-ignored `.cache/` directory. Do
not call `go` directly — it would write to the machine-global Go caches.

```powershell
./scripts/go-local.ps1 build ./...   # build
./scripts/go-local.ps1 test ./...    # run all tests
./scripts/go-local.ps1 vet ./...     # static checks
./scripts/go-local.ps1 fmt ./...     # format
```

Run a single package's tests: `./scripts/go-local.ps1 test ./internal/core`.

## Project layout

- `cmd/taskwatch` — entry point: resolves the launch folder and state path,
  wires real dependencies, starts the UI.
- `internal/core` — platform-independent session, identity, and safety model
  (process trees, confirmation requests). No Windows APIs here.
- `internal/platform/windows` — supervisor, observer, and process snapshotter
  (Job Objects, Windows process APIs).
- `internal/store` — schema-versioned JSON persistence (atomic writes).
- `internal/app` — coordinator owning startup recovery and refresh cycles.
- `internal/tui` — Bubble Tea view/model; presents coordinator data only.

## Non-negotiable safety rules

TaskWatch treats "kill this process" as a decision only the user makes. When
touching supervision, observation, or termination code, preserve these:

1. TaskWatch never terminates a process on its own; every termination is
   explicitly user-confirmed and locked to one session and one exact process
   set.
2. Process identity is always PID **plus** creation time — never a bare PID.
3. Missing or unreadable metadata lowers confidence and blocks unsafe actions;
   it is never treated as a green light.
4. Re-validate job membership and every target's identity through an open
   handle immediately before terminating. Only the exact confirmed processes
   are ever terminated. Never call `TerminateJobObject`.
5. Closing TaskWatch does not terminate a Job Object (no kill-on-close).

The full rule set lives in `docs/system-design.md` → "Safety rules".

## Conventions

- **Test-first.** Behavior changes come with tests and must pass `test` and
  `vet` before a milestone advances.
- **No commits unless asked.** Do not create Git commits unless the user
  explicitly requests one.
- Windows 10/11 x64 is the only MVP target.
- Keep platform APIs out of `internal/core` — it stays OS-independent.
- The TUI never rebuilds process trees or duplicates platform safety logic; it
  renders what the coordinator provides.

## Ponytail plugin

This project uses the [ponytail](https://github.com/DietrichGebert/ponytail)
plugin — a "lazy senior developer" ruleset that prefers skipping, reusing, and
leveraging built-ins before writing new code. Keep it enabled while working in
this repo, for both Claude Code and Codex. Align its bias toward less code with
this project's own goal of small, reviewable, well-tested changes.
