# Contributing to TaskWatch

Thanks for contributing. TaskWatch is a Windows 10/11 x64 tool, and its process-safety boundaries matter more than feature breadth.

## Before opening a change

- Keep changes small and focused.
- Add or update tests for behavior changes.
- Keep Windows APIs in `internal/platform/windows`; `internal/core` remains platform-independent.
- Keep the terminal UI as a renderer and coordinator client: it must not rebuild process trees or duplicate platform safety decisions.

## Required checks

Use the repository wrapper for every Go command. Do not call `go` directly; the wrapper keeps caches, module downloads, and temporary files in the repository's ignored `.cache/` directory.

```powershell
./scripts/go-local.ps1 fmt ./...
./scripts/go-local.ps1 test ./...
./scripts/go-local.ps1 vet ./...
./scripts/go-local.ps1 build ./...
```

For a focused test run:

```powershell
./scripts/go-local.ps1 test ./internal/core
```

## Process-safety requirements

Changes involving supervision, observation, or future termination work must preserve these rules:

1. TaskWatch must never automatically terminate an existing or successfully launched process. It may terminate a TaskWatch-created suspended process only when setup fails before that process is resumed. Any future cleanup action must be explicitly confirmed for one session and one exact process set.
2. Process identity is PID plus creation time, never a PID alone.
3. Missing or unreadable metadata reduces confidence and blocks unsafe actions; it is not evidence that an action is safe.
4. Immediately before any future termination, revalidate job membership and every target identity through an open handle. Only exact confirmed targets may be terminated; never call `TerminateJobObject`.
5. Closing TaskWatch must not terminate a Job Object or its processes.

The current preview intentionally exposes no user-facing termination control. Preserve that boundary unless a change explicitly introduces and fully validates the required safety flow.

## Pull requests

Describe the user-visible behavior, safety impact, and verification performed. Include the Windows version and architecture when reporting platform-specific behavior. Keep unrelated formatting or refactoring out of a focused change.

## Security issues

Do not report vulnerabilities in public issues. Follow the private reporting instructions in [SECURITY.md](SECURITY.md).

By contributing, you agree that your contributions may be distributed under the [MIT License](LICENSE).
