package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/PyromancerBoom/taskwatch/internal/core"
)

var (
	errCoordinatorClosed = errors.New("coordinator is closed")
	ErrSessionNotFound   = errors.New("session not found")
)

type SessionStore interface {
	Load() ([]core.Session, error)
	Save([]core.Session) error
	LoadRecentWorkspaces() ([]string, error)
	SaveRecentWorkspaces([]string) error
}

type Supervisor interface {
	Launch(core.Tool, string) (core.Session, error)
	RecoverMany([]core.Session) ([]core.Session, []error)
	RefreshMany([]core.Session) ([]core.Session, []error)
	Close()
}

type Observer interface {
	Observe(map[uint32]core.ProcessIdentity) ([]core.Session, error)
}

type ActivityCollector interface {
	Collect(time.Time) ([]core.AgentActivity, error)
}

type warningKind uint8

const (
	warningGeneral warningKind = iota
	warningPersistence
	warningRecentWorkspaces
)

type Warning struct {
	SessionID string
	Message   string
	kind      warningKind
}

type Snapshot struct {
	CurrentDir       string
	RecentWorkspaces []string
	Sessions         []core.Session
	Warnings         []Warning
	RefreshedAt      time.Time
}

type LaunchResult struct {
	Snapshot  Snapshot
	SessionID string
}

type ProcessTree struct {
	Session     core.Session
	Rows        []core.ProcessTreeRow
	CompactRows []core.CompactProcessRow
}

type Coordinator struct {
	mu                sync.Mutex
	closed            bool
	baseDir           string
	store             SessionStore
	supervisor        Supervisor
	observer          Observer
	activityCollector ActivityCollector
	selfIdentity      core.ProcessIdentity
	now               func() time.Time
	resolveWorkspace  func(string, string) (string, error)
	supervised        []core.Session
	observed          []core.Session
	recentWorkspaces  []string
	warnings          []Warning
	refreshed         time.Time
	persistenceDirty  bool
	activityMatches   map[string]activityMatch
}

type activityMatch struct {
	Root      core.ProcessIdentity
	ThreadID  string
	Workspace string
}

// maxRecentWorkspaces caps the persisted recent-workspaces list.
const maxRecentWorkspaces = 8

const activityVisibilityWindow = 15 * time.Minute

func NewCoordinator(baseDir string, store SessionStore, supervisor Supervisor, observer Observer, selfIdentity core.ProcessIdentity) *Coordinator {
	coordinator := newCoordinator(baseDir, store, supervisor, observer, time.Now)
	coordinator.selfIdentity = selfIdentity
	return coordinator
}

func NewCoordinatorWithActivity(baseDir string, store SessionStore, supervisor Supervisor, observer Observer, collector ActivityCollector, selfIdentity core.ProcessIdentity) *Coordinator {
	coordinator := newCoordinatorWithActivity(baseDir, store, supervisor, observer, collector, time.Now)
	coordinator.selfIdentity = selfIdentity
	return coordinator
}

func newCoordinator(baseDir string, store SessionStore, supervisor Supervisor, observer Observer, now func() time.Time) *Coordinator {
	return newCoordinatorWithActivity(baseDir, store, supervisor, observer, nil, now)
}

func newCoordinatorWithActivity(baseDir string, store SessionStore, supervisor Supervisor, observer Observer, collector ActivityCollector, now func() time.Time) *Coordinator {
	return &Coordinator{
		baseDir:           baseDir,
		store:             store,
		supervisor:        supervisor,
		observer:          observer,
		activityCollector: collector,
		now:               now,
		resolveWorkspace:  resolveWorkspace,
		activityMatches:   make(map[string]activityMatch),
	}
}

// recordRecentWorkspace moves path to the front of the recent list, deduping
// case-insensitively and capping the list length. The most recently launched
// workspace is always first, so the picker can pre-select it.
func (coordinator *Coordinator) recordRecentWorkspace(path string) {
	updated := make([]string, 0, len(coordinator.recentWorkspaces)+1)
	updated = append(updated, path)
	for _, existing := range coordinator.recentWorkspaces {
		if strings.EqualFold(existing, path) {
			continue
		}
		updated = append(updated, existing)
	}
	if len(updated) > maxRecentWorkspaces {
		updated = updated[:maxRecentWorkspaces]
	}
	coordinator.recentWorkspaces = updated
}

func resolveWorkspace(base, path string) (string, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	resolved, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved = filepath.Clean(resolved)
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%q is not a directory", resolved)
	}
	return resolved, nil
}

func (coordinator *Coordinator) Launch(tool core.Tool, workspace string) (LaunchResult, error) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.closed {
		return LaunchResult{Snapshot: coordinator.snapshot()}, errCoordinatorClosed
	}

	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return LaunchResult{Snapshot: coordinator.snapshot()}, errors.New("workspace path is empty")
	}
	resolved, err := coordinator.resolveWorkspace(coordinator.baseDir, workspace)
	if err != nil {
		return LaunchResult{Snapshot: coordinator.snapshot()}, fmt.Errorf("resolve workspace: %w", err)
	}

	launched, err := coordinator.supervisor.Launch(tool, resolved)
	if err != nil {
		return LaunchResult{Snapshot: coordinator.snapshot()}, fmt.Errorf("launch %s: %w", tool, err)
	}
	launched = cloneSession(launched)
	coordinator.supervised = append(coordinator.supervised, launched)
	coordinator.warnings = clearPersistenceWarnings(coordinator.warnings)
	coordinator.recordRecentWorkspace(resolved)
	coordinator.warnings = clearWarningKind(coordinator.warnings, warningRecentWorkspaces)
	if err := coordinator.store.SaveRecentWorkspaces(append([]string(nil), coordinator.recentWorkspaces...)); err != nil {
		coordinator.warnings = append(coordinator.warnings, Warning{Message: fmt.Sprintf("save recent workspaces: %v", err), kind: warningRecentWorkspaces})
	}
	result := LaunchResult{Snapshot: coordinator.snapshot(), SessionID: launched.ID}
	if err := coordinator.store.Save(cloneSessions(coordinator.persistedSessions())); err != nil {
		coordinator.persistenceDirty = true
		coordinator.warnings = append(coordinator.warnings, Warning{
			SessionID: launched.ID,
			Message:   launchSaveWarningPrefix + err.Error(),
			kind:      warningPersistence,
		})
		result.Snapshot = coordinator.snapshot()
		return result, fmt.Errorf("save launched session %q: %w", launched.ID, err)
	}
	coordinator.persistenceDirty = false
	return result, nil
}

const launchSaveWarningPrefix = "save launched session: "

// clearPersistenceWarnings drops prior "could not save the full session
// set" warnings. Both Refresh and Launch retry a save of the complete
// persisted session set, so a successful save in either method makes any
// earlier persistence-category warning stale, regardless of which method
// produced it.
func clearPersistenceWarnings(warnings []Warning) []Warning {
	return clearWarningKind(warnings, warningPersistence)
}

func clearWarningKind(warnings []Warning, kind warningKind) []Warning {
	filtered := make([]Warning, 0, len(warnings))
	for _, w := range warnings {
		if w.kind == kind {
			continue
		}
		filtered = append(filtered, w)
	}
	return filtered
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
	clear(coordinator.activityMatches)

	recent, recentErr := coordinator.store.LoadRecentWorkspaces()
	if recentErr != nil {
		coordinator.recentWorkspaces = nil
		coordinator.warnings = append(coordinator.warnings, Warning{Message: fmt.Sprintf("load recent workspaces: %v", recentErr), kind: warningRecentWorkspaces})
	} else {
		coordinator.recentWorkspaces = recent
	}

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
		coordinator.warnings = append(coordinator.warnings, Warning{Message: fmt.Sprintf("observe Codex sessions: %v", observeErr)})
	} else {
		coordinator.observed = cloneSessions(observed)
	}

	refreshedAt := coordinator.now()
	coordinator.collectActivities(refreshedAt)

	if exited {
		if err := coordinator.store.Save(cloneSessions(coordinator.persistedSessions())); err != nil {
			return Snapshot{}, fmt.Errorf("save reconciled sessions: %w", err)
		}
	}
	coordinator.refreshed = refreshedAt
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
		coordinator.warnings = append(coordinator.warnings, Warning{Message: fmt.Sprintf("observe Codex sessions: %v", observeErr)})
	} else {
		coordinator.observed = cloneSessions(observed)
	}
	refreshedAt := coordinator.now()
	coordinator.collectActivities(refreshedAt)

	if newlyExited || coordinator.persistenceDirty {
		if err := coordinator.store.Save(cloneSessions(coordinator.persistedSessions())); err != nil {
			coordinator.persistenceDirty = true
			coordinator.warnings = append(coordinator.warnings, Warning{Message: fmt.Sprintf("save reconciled sessions: %v", err), kind: warningPersistence})
		} else {
			coordinator.persistenceDirty = false
		}
	}
	coordinator.refreshed = refreshedAt
	return coordinator.snapshot()
}

// Inspect returns a process forest prepared from a cached session snapshot.
func (coordinator *Coordinator) Inspect(sessionID string) (ProcessTree, error) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.closed {
		return ProcessTree{}, errCoordinatorClosed
	}

	var found *core.Session
	for i := range coordinator.supervised {
		if coordinator.supervised[i].ID == sessionID {
			found = &coordinator.supervised[i]
			break
		}
	}
	if found == nil {
		for i := range coordinator.observed {
			if coordinator.observed[i].ID == sessionID {
				found = &coordinator.observed[i]
				break
			}
		}
	}
	if found == nil {
		return ProcessTree{}, ErrSessionNotFound
	}

	session := cloneSession(*found)
	rows := core.CollectProcessForest(session.Processes)
	return ProcessTree{
		Session:     session,
		Rows:        rows,
		CompactRows: core.BuildCompactProcessForest(session, rows),
	}, nil
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
	if coordinator.selfIdentity.PID != 0 && !coordinator.selfIdentity.CreatedAt.IsZero() {
		excluded[coordinator.selfIdentity.PID] = coordinator.selfIdentity
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
		CurrentDir:       coordinator.baseDir,
		RecentWorkspaces: append([]string(nil), coordinator.recentWorkspaces...),
		Sessions:         sessions,
		Warnings:         append([]Warning(nil), coordinator.warnings...),
		RefreshedAt:      coordinator.refreshed,
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
	cloned.Activities = append([]core.AgentActivity(nil), session.Activities...)
	return cloned
}

func (coordinator *Coordinator) collectActivities(now time.Time) {
	if coordinator.activityCollector == nil {
		return
	}
	activities, err := coordinator.activityCollector.Collect(now)
	if err != nil {
		coordinator.warnings = append(coordinator.warnings, Warning{Message: "collect Codex activity: " + err.Error()})
	}
	visible := make([]core.AgentActivity, 0, len(activities))
	for _, activity := range activities {
		if activity.UpdatedAt.IsZero() || now.Sub(activity.UpdatedAt) > activityVisibilityWindow {
			continue
		}
		visible = append(visible, activity)
	}
	coordinator.attachActivities(visible)
}

func (coordinator *Coordinator) attachActivities(activities []core.AgentActivity) {
	for i := range coordinator.supervised {
		coordinator.supervised[i].Activities = nil
	}
	for i := range coordinator.observed {
		coordinator.observed[i].Activities = nil
	}

	var desktop []*core.Session
	for i := range coordinator.observed {
		session := &coordinator.observed[i]
		if session.Tool == core.ToolCodexDesktop {
			desktop = append(desktop, session)
		}
	}
	if len(desktop) == 1 {
		for _, activity := range activities {
			if activity.Tool != core.ToolCodexDesktop {
				continue
			}
			activity.Correlation = core.CorrelationApplication
			desktop[0].Activities = append(desktop[0].Activities, activity)
		}
	}

	cli := coordinator.runningCLISessions()
	activeIDs := make(map[string]struct{}, len(cli))
	for _, session := range cli {
		activeIDs[session.ID] = struct{}{}
	}
	for id := range coordinator.activityMatches {
		if _, active := activeIDs[id]; !active {
			delete(coordinator.activityMatches, id)
		}
	}
	available := make(map[string]core.AgentActivity)
	for _, activity := range activities {
		if activity.Tool == core.ToolCodexCLI && activity.ThreadID != "" {
			available[strings.ToLower(activity.ThreadID)] = activity
		}
	}
	assigned := make(map[string]struct{})
	matched := make(map[string]struct{})
	assign := func(session *core.Session, activity core.AgentActivity, correlation core.ActivityCorrelation) {
		activity.Correlation = correlation
		session.Activities = []core.AgentActivity{activity}
		key := strings.ToLower(activity.ThreadID)
		assigned[key] = struct{}{}
		matched[session.ID] = struct{}{}
		coordinator.activityMatches[session.ID] = activityMatch{Root: session.Root, ThreadID: activity.ThreadID, Workspace: activity.Workspace}
	}

	for _, session := range cli {
		hint := strings.ToLower(session.ActivityMatchHint.ThreadID)
		if hint == "" {
			continue
		}
		if activity, exists := available[hint]; exists {
			if _, used := assigned[hint]; !used {
				assign(session, activity, core.CorrelationDirect)
			}
		}
	}

	for _, session := range cli {
		if _, done := matched[session.ID]; done || session.ActivityMatchHint.ThreadID != "" {
			continue
		}
		previous, sticky := coordinator.activityMatches[session.ID]
		if sticky && !core.SameProcess(previous.Root, session.Root) {
			sticky = false
			delete(coordinator.activityMatches, session.ID)
		}
		workspace := firstWorkspace(session.Workspace, session.ActivityMatchHint.Workspace)
		if workspace == "" && sticky {
			workspace = previous.Workspace
		}
		if workspace != "" {
			if activity, ok := bestWorkspaceActivity(available, assigned, workspace, session.StartedAt, sticky); ok {
				assign(session, activity, core.CorrelationInferred)
				continue
			}
		}
		if sticky {
			key := strings.ToLower(previous.ThreadID)
			if activity, exists := available[key]; exists {
				if _, used := assigned[key]; !used {
					assign(session, activity, core.CorrelationInferred)
				}
			}
		}
	}

	for _, session := range cli {
		if _, done := matched[session.ID]; done || session.ActivityMatchHint.ThreadID != "" {
			continue
		}
		if activity, ok := nearestActivity(available, assigned, session.StartedAt, time.Minute); ok {
			assign(session, activity, core.CorrelationInferred)
		}
	}
}

func (coordinator *Coordinator) runningCLISessions() []*core.Session {
	sessions := make([]*core.Session, 0)
	for i := range coordinator.supervised {
		if coordinator.supervised[i].Tool == core.ToolCodexCLI && coordinator.supervised[i].State == core.StateRunning {
			sessions = append(sessions, &coordinator.supervised[i])
		}
	}
	for i := range coordinator.observed {
		if coordinator.observed[i].Tool == core.ToolCodexCLI && coordinator.observed[i].State == core.StateRunning {
			sessions = append(sessions, &coordinator.observed[i])
		}
	}
	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].StartedAt.Equal(sessions[j].StartedAt) {
			return sessions[i].ID < sessions[j].ID
		}
		return sessions[i].StartedAt.Before(sessions[j].StartedAt)
	})
	return sessions
}

func firstWorkspace(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return filepath.Clean(value)
		}
	}
	return ""
}

func sameWorkspace(left, right string) bool {
	return left != "" && right != "" && strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
}

func bestWorkspaceActivity(activities map[string]core.AgentActivity, assigned map[string]struct{}, workspace string, started time.Time, preferNewest bool) (core.AgentActivity, bool) {
	var best core.AgentActivity
	found := false
	for key, activity := range activities {
		if _, used := assigned[key]; used || !sameWorkspace(activity.Workspace, workspace) {
			continue
		}
		if !found || (preferNewest && activity.UpdatedAt.After(best.UpdatedAt)) || (!preferNewest && activityDistance(activity, started) < activityDistance(best, started)) {
			best, found = activity, true
		}
	}
	return best, found
}

func nearestActivity(activities map[string]core.AgentActivity, assigned map[string]struct{}, started time.Time, limit time.Duration) (core.AgentActivity, bool) {
	var best core.AgentActivity
	found := false
	for key, activity := range activities {
		if _, used := assigned[key]; used || activity.StartedAt.IsZero() {
			continue
		}
		distance := activityDistance(activity, started)
		if distance > limit {
			continue
		}
		if !found || distance < activityDistance(best, started) || (distance == activityDistance(best, started) && activity.ThreadID < best.ThreadID) {
			best, found = activity, true
		}
	}
	return best, found
}

func activityDistance(activity core.AgentActivity, started time.Time) time.Duration {
	distance := activity.StartedAt.Sub(started)
	if distance < 0 {
		return -distance
	}
	return distance
}
