package app

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"taskwatch/internal/core"
)

var errCoordinatorClosed = errors.New("coordinator is closed")

type SessionStore interface {
	Load() ([]core.Session, error)
	Save([]core.Session) error
}

type Supervisor interface {
	RecoverMany([]core.Session) ([]core.Session, []error)
	RefreshMany([]core.Session) ([]core.Session, []error)
	Close()
}

type Observer interface {
	Observe(map[uint32]core.ProcessIdentity) ([]core.Session, error)
}

type Warning struct {
	SessionID string
	Message   string
}

type Snapshot struct {
	Workspace   string
	Sessions    []core.Session
	Warnings    []Warning
	RefreshedAt time.Time
}

type Coordinator struct {
	mu         sync.Mutex
	closed     bool
	workspace  string
	store      SessionStore
	supervisor Supervisor
	observer   Observer
	now        func() time.Time
	supervised []core.Session
	observed   []core.Session
	warnings   []Warning
	refreshed  time.Time
}

func NewCoordinator(workspace string, store SessionStore, supervisor Supervisor, observer Observer) *Coordinator {
	return newCoordinator(workspace, store, supervisor, observer, time.Now)
}

func newCoordinator(workspace string, store SessionStore, supervisor Supervisor, observer Observer, now func() time.Time) *Coordinator {
	return &Coordinator{
		workspace:  workspace,
		store:      store,
		supervisor: supervisor,
		observer:   observer,
		now:        now,
	}
}

func (coordinator *Coordinator) Start() (Snapshot, error) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.closed {
		return coordinator.snapshot(), errCoordinatorClosed
	}

	loaded, err := coordinator.store.Load()
	if err != nil {
		return Snapshot{}, fmt.Errorf("load sessions: %w", err)
	}

	loaded = cloneSessions(loaded)
	coordinator.supervised = nil
	coordinator.observed = nil
	coordinator.warnings = nil
	exited := false
	if len(loaded) > 0 {
		recovered, recoverErrs := coordinator.supervisor.RecoverMany(cloneSessions(loaded))
		coordinator.supervised = make([]core.Session, len(loaded))
		for i := range loaded {
			if recoverErrs[i] != nil {
				stale := cloneSession(loaded[i])
				stale.State = core.StateStale
				coordinator.supervised[i] = stale
				coordinator.warnings = append(coordinator.warnings, Warning{
					SessionID: loaded[i].ID,
					Message:   fmt.Sprintf("recover session: %v", recoverErrs[i]),
				})
				continue
			}
			coordinator.supervised[i] = cloneSession(recovered[i])
			if recovered[i].State == core.StateExited {
				exited = true
			}
		}
	}

	observed, observeErr := coordinator.observer.Observe(coordinator.exclusions())
	if observeErr != nil {
		coordinator.warnings = append(coordinator.warnings, Warning{Message: fmt.Sprintf("observe desktop sessions: %v", observeErr)})
	} else {
		coordinator.observed = cloneSessions(observed)
	}

	if exited {
		if err := coordinator.store.Save(cloneSessions(coordinator.persistedSessions())); err != nil {
			return Snapshot{}, fmt.Errorf("save reconciled sessions: %w", err)
		}
	}
	coordinator.refreshed = coordinator.now()
	return coordinator.snapshot(), nil
}

func (coordinator *Coordinator) Refresh() Snapshot {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.closed {
		return coordinator.snapshot()
	}

	coordinator.warnings = nil
	active := make([]core.Session, 0, len(coordinator.supervised))
	indices := make([]int, 0, len(coordinator.supervised))
	for i := range coordinator.supervised {
		if coordinator.supervised[i].State == core.StateExited {
			continue
		}
		active = append(active, cloneSession(coordinator.supervised[i]))
		indices = append(indices, i)
	}

	newlyExited := false
	if len(active) > 0 {
		refreshed, refreshErrs := coordinator.supervisor.RefreshMany(cloneSessions(active))
		for i, index := range indices {
			if refreshErrs[i] != nil {
				stale := cloneSession(coordinator.supervised[index])
				stale.State = core.StateStale
				coordinator.supervised[index] = stale
				coordinator.warnings = append(coordinator.warnings, Warning{
					SessionID: stale.ID,
					Message:   fmt.Sprintf("refresh session: %v", refreshErrs[i]),
				})
				continue
			}
			result := cloneSession(refreshed[i])
			coordinator.supervised[index] = result
			if result.State == core.StateExited {
				newlyExited = true
			}
			if result.State == core.StateInaccessible {
				coordinator.warnings = append(coordinator.warnings, Warning{
					SessionID: result.ID,
					Message:   "session inaccessible",
				})
			}
		}
	}

	observed, observeErr := coordinator.observer.Observe(coordinator.exclusions())
	if observeErr != nil {
		retained := cloneSessions(coordinator.observed)
		for i := range retained {
			retained[i].State = core.StateStale
		}
		coordinator.observed = retained
		coordinator.warnings = append(coordinator.warnings, Warning{Message: fmt.Sprintf("observe desktop sessions: %v", observeErr)})
	} else {
		coordinator.observed = cloneSessions(observed)
	}

	if newlyExited {
		if err := coordinator.store.Save(cloneSessions(coordinator.persistedSessions())); err != nil {
			coordinator.warnings = append(coordinator.warnings, Warning{Message: fmt.Sprintf("save reconciled sessions: %v", err)})
		}
	}
	coordinator.refreshed = coordinator.now()
	return coordinator.snapshot()
}

func (coordinator *Coordinator) Close() {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()

	if coordinator.closed {
		return
	}
	coordinator.closed = true
	coordinator.supervisor.Close()
}

func (coordinator *Coordinator) exclusions() map[uint32]core.ProcessIdentity {
	excluded := make(map[uint32]core.ProcessIdentity)
	for _, session := range coordinator.supervised {
		for _, process := range session.Processes {
			excluded[process.PID] = process.Identity()
		}
	}
	return excluded
}

func (coordinator *Coordinator) persistedSessions() []core.Session {
	persisted := make([]core.Session, 0, len(coordinator.supervised))
	for _, session := range coordinator.supervised {
		if session.State != core.StateExited {
			persisted = append(persisted, cloneSession(session))
		}
	}
	return persisted
}

func (coordinator *Coordinator) snapshot() Snapshot {
	sessions := make([]core.Session, 0, len(coordinator.supervised)+len(coordinator.observed))
	sessions = append(sessions, cloneSessions(coordinator.supervised)...)
	sessions = append(sessions, cloneSessions(coordinator.observed)...)
	sort.SliceStable(sessions, func(i, j int) bool {
		if sessions[i].StartedAt.Equal(sessions[j].StartedAt) {
			return sessions[i].ID < sessions[j].ID
		}
		return sessions[i].StartedAt.Before(sessions[j].StartedAt)
	})
	return Snapshot{
		Workspace:   coordinator.workspace,
		Sessions:    sessions,
		Warnings:    append([]Warning(nil), coordinator.warnings...),
		RefreshedAt: coordinator.refreshed,
	}
}

func cloneSessions(sessions []core.Session) []core.Session {
	if sessions == nil {
		return nil
	}
	cloned := make([]core.Session, len(sessions))
	for i := range sessions {
		cloned[i] = cloneSession(sessions[i])
	}
	return cloned
}

func cloneSession(session core.Session) core.Session {
	cloned := session
	cloned.Processes = append([]core.Process(nil), session.Processes...)
	return cloned
}
