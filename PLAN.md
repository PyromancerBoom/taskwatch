# TaskWatch development plan

TaskWatch will be built in small, reviewable milestones. Each milestone must
pass its tests and receive a user checkpoint before work starts on the next
one. The plan is intentionally high-level; detailed implementation decisions
belong in tests and focused package documentation.

The implemented and planned architecture is documented in
[`docs/system-design.md`](docs/system-design.md).

## Working agreements

- Windows 10/11 x64 is the only MVP target.
- TaskWatch never terminates a process without explicit confirmation.
- TaskWatch runs as a standard user and never installs a service.
- Go caches, downloaded modules, and temporary build files stay inside the
  ignored `.cache/` directory through `scripts/go-local.ps1`.
- No Git commits are created unless the user explicitly requests one.
- Behavior changes are developed test-first and reviewed before the next
  milestone begins.

## Milestone 1 — Core safety model

**Status:** Complete and reviewed

- Model supervised and observed sessions, process identity, lifecycle state,
  and resource totals.
- Build process trees using PID and creation-time identity.
- Require an explicit confirmation object for destructive actions.
- Persist supervised session metadata atomically with schema-version checks.

**Checkpoint:** Core and persistence tests and vet checks pass.

## Milestone 2 — Windows supervision and observation

**Status:** Complete and reviewed; awaiting user checkpoint

- Launch Claude Code and Codex CLI suspended in new consoles.
- Assign each CLI to a named Windows Job Object before it can spawn children.
- Recover surviving named jobs and detect leftover descendants.
- Observe Codex desktop process trees on a clearly labeled best-effort basis.
- Collect process metadata and resource usage without elevation.
- Revalidate every target before confirmed termination and abort safely when
  identity or access cannot be proven.

**User checkpoint:** Review the process-safety behavior and Windows-platform
tests before TUI work begins.

## Milestone 3 — Interactive terminal UI

**Status:** Not started

- Show workspace, sessions, confidence, state, age, process count, CPU, and
  memory in a keyboard-driven dashboard.
- Launch Claude Code with `c`, Codex CLI with `x`, and change workspace with
  `w`.
- Inspect process trees with `Enter` and refresh with `r`.
- Use explicit review and confirmation screens for `k` termination actions.
- Block `q` exit while active groups exist, offering return, leave running,
  or review groups.

**User checkpoint:** Review the running TUI and wording before integration
polish begins.

## Milestone 4 — End-to-end Windows validation

**Status:** Not started

- Add a helper executable that leaves child and grandchild processes alive.
- Verify leftover detection, cancellation safety, confirmed termination, and
  named-job recovery against real Windows processes.
- Smoke-test installed Claude Code, Codex CLI, and Codex desktop when present.

**User checkpoint:** Review test evidence and any platform limitations.

## Milestone 5 — Build and handoff

**Status:** Not started

- Add `--help`, `--version`, and a concise README.
- Produce a portable Windows x64 executable under `dist/`.
- Run formatting, vet, unit/integration tests, and a final safety review.

**User checkpoint:** Try the executable and decide the next iteration. Broader
open-source packaging remains a separate follow-up.
