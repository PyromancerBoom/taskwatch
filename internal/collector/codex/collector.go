package codex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/PyromancerBoom/taskwatch/internal/core"
)

const (
	activeWindow   = 15 * time.Minute
	maxRecordBytes = 1 << 20
	headerBytes    = 1 << 20
	tailBytes      = 8 << 20
	prefixBytes    = 4 << 10
	maxActiveFiles = 256
)

type Collector struct {
	root  string
	files map[string]*fileState
}

type fileState struct {
	offset      int64
	prefix      []byte
	info        os.FileInfo
	activity    core.AgentActivity
	metaFound   bool
	eligible    bool
	certain     bool
	activeTask  bool
	outstanding map[string]string
}

type rolloutLine struct {
	Timestamp time.Time       `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

func New(root string) *Collector {
	return &Collector{root: root, files: make(map[string]*fileState)}
}

func DefaultRoot() (string, error) {
	if home := strings.TrimSpace(os.Getenv("CODEX_HOME")); home != "" {
		return filepath.Join(home, "sessions"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex", "sessions"), nil
}

func (collector *Collector) Collect(now time.Time) ([]core.AgentActivity, error) {
	if _, err := os.Stat(collector.root); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("read Codex activity: %w", err)
	}
	entries, warnings := discoverActiveRollouts(collector.root, now)

	activities := make([]core.AgentActivity, 0, len(entries))
	for path, info := range entries {
		state := collector.files[path]
		reset := state == nil || info.Size() < state.offset
		if !reset && state.info != nil && !os.SameFile(state.info, info) {
			reset = true
		}
		if !reset && len(state.prefix) > 0 {
			prefix, err := readPrefix(path, int64(len(state.prefix)))
			if err != nil {
				warnings = append(warnings, err.Error())
				continue
			}
			reset = !bytes.Equal(prefix, state.prefix)
		}
		if reset {
			prefix, err := readPrefix(path, info.Size())
			if err != nil {
				warnings = append(warnings, err.Error())
				continue
			}
			state = newFileState()
			state.prefix = prefix
			collector.files[path] = state
			if err := bootstrap(path, info.Size(), state); err != nil {
				warnings = append(warnings, err.Error())
				if !state.metaFound {
					continue
				}
			}
		} else if info.Size() > state.offset {
			limit := info.Size() - state.offset
			skipFirst := false
			var boundErr error
			if limit > tailBytes {
				state.certain = false
				state.outstanding = make(map[string]string)
				boundErr = skippedReadWarning(path, limit-tailBytes)
				state.offset = info.Size() - tailBytes
				limit = tailBytes
				skipFirst = true
			}
			if err := errors.Join(boundErr, consume(path, state.offset, limit, skipFirst, state)); err != nil {
				warnings = append(warnings, err.Error())
			}
		}
		state.info = info
		if state.metaFound && state.eligible {
			activity := state.activity
			activity.UpdatedAt = info.ModTime()
			activity.State = state.activityState()
			activities = append(activities, activity)
		}
	}
	for path := range collector.files {
		if _, exists := entries[path]; !exists {
			delete(collector.files, path)
		}
	}
	sort.Slice(activities, func(i, j int) bool {
		if activities[i].StartedAt.Equal(activities[j].StartedAt) {
			return activities[i].ThreadID < activities[j].ThreadID
		}
		return activities[i].StartedAt.Before(activities[j].StartedAt)
	})
	if len(warnings) > 0 {
		sort.Strings(warnings)
		return activities, fmt.Errorf("Codex activity incomplete: %s", strings.Join(warnings, "; "))
	}
	return activities, nil
}

type rolloutCandidate struct {
	path string
	info os.FileInfo
}

func discoverActiveRollouts(root string, now time.Time) (map[string]os.FileInfo, []string) {
	localNow := now.In(time.Local)
	dates := []time.Time{localNow, localNow.AddDate(0, 0, -1)}
	dirs := []string{root}
	for _, date := range dates {
		dirs = append(dirs, filepath.Join(root, date.Format("2006"), date.Format("01"), date.Format("02")))
	}

	var candidates []rolloutCandidate
	var warnings []string
	compressed := 0
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			warnings = append(warnings, err.Error())
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				warnings = append(warnings, err.Error())
				continue
			}
			if now.Sub(info.ModTime()) > activeWindow {
				continue
			}
			lower := strings.ToLower(entry.Name())
			if strings.HasSuffix(lower, ".jsonl.zst") || strings.HasSuffix(lower, ".zst") {
				compressed++
				continue
			}
			if strings.HasSuffix(lower, ".jsonl") {
				candidates = append(candidates, rolloutCandidate{path: filepath.Join(dir, entry.Name()), info: info})
			}
		}
	}
	if compressed > 0 {
		warnings = append(warnings, fmt.Sprintf("%d compressed active rollout(s) ignored", compressed))
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].info.ModTime().Equal(candidates[j].info.ModTime()) {
			return candidates[i].path < candidates[j].path
		}
		return candidates[i].info.ModTime().After(candidates[j].info.ModTime())
	})
	if len(candidates) > maxActiveFiles {
		warnings = append(warnings, fmt.Sprintf("active rollout limit reached: ignored %d after newest %d", len(candidates)-maxActiveFiles, maxActiveFiles))
		candidates = candidates[:maxActiveFiles]
	}
	result := make(map[string]os.FileInfo, len(candidates))
	for _, candidate := range candidates {
		result[candidate.path] = candidate.info
	}
	return result, warnings
}

func newFileState() *fileState {
	return &fileState{outstanding: make(map[string]string)}
}

func bootstrap(path string, size int64, state *fileState) error {
	if size <= headerBytes+tailBytes {
		return consume(path, 0, size, false, state)
	}
	headerErr := consume(path, 0, headerBytes, false, state)
	state.certain = false
	state.activeTask = false
	state.outstanding = make(map[string]string)
	state.offset = size - tailBytes
	tailErr := consume(path, state.offset, tailBytes, true, state)
	return errors.Join(skippedReadWarning(path, size-headerBytes-tailBytes), headerErr, tailErr)
}

func skippedReadWarning(path string, bytes int64) error {
	return fmt.Errorf("%s: read bound skipped %d bytes", filepath.Base(path), bytes)
}

func readPrefix(path string, size int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	length := min(size, prefixBytes)
	prefix := make([]byte, length)
	_, err = io.ReadFull(file, prefix)
	if errors.Is(err, io.ErrUnexpectedEOF) {
		err = nil
	}
	return prefix, err
}

func consume(path string, start, limit int64, skipFirst bool, state *fileState) error {
	file, err := os.Open(path)
	if err != nil {
		state.certain = false
		return err
	}
	defer file.Close()
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		state.certain = false
		return err
	}
	reader := bufio.NewReaderSize(io.LimitReader(file, limit), 64<<10)
	position := start
	recordStart := start
	var record []byte
	oversized := false
	var issues []string
	for {
		fragment, readErr := reader.ReadSlice('\n')
		position += int64(len(fragment))
		if !oversized {
			if len(record)+len(fragment) > maxRecordBytes {
				oversized = true
				record = nil
			} else {
				record = append(record, fragment...)
			}
		}
		if errors.Is(readErr, bufio.ErrBufferFull) {
			continue
		}
		complete := len(fragment) > 0 && fragment[len(fragment)-1] == '\n'
		if complete {
			if skipFirst {
				skipFirst = false
			} else if oversized {
				state.certain = false
				state.activeTask = false
				state.outstanding = make(map[string]string)
				issues = append(issues, "oversized record")
			} else if err := state.apply(bytes.TrimSpace(record)); err != nil {
				state.certain = false
				state.activeTask = false
				state.outstanding = make(map[string]string)
				issues = append(issues, "invalid record")
			}
			state.offset = position
			recordStart = position
			record = nil
			oversized = false
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				if !complete {
					state.offset = recordStart
					if len(record) > 0 || oversized {
						state.certain = false
					}
				}
				if len(issues) > 0 {
					return fmt.Errorf("%s: %s", filepath.Base(path), strings.Join(issues, ", "))
				}
				return nil
			}
			state.certain = false
			return readErr
		}
	}
}

func (state *fileState) apply(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	var line rolloutLine
	if err := json.Unmarshal(data, &line); err != nil {
		return err
	}
	switch line.Type {
	case "session_meta":
		if state.metaFound {
			return nil
		}
		var payload struct {
			ID             string     `json:"id"`
			SessionID      string     `json:"session_id"`
			Timestamp      *time.Time `json:"timestamp"`
			CWD            string     `json:"cwd"`
			Originator     string     `json:"originator"`
			ParentThreadID *string    `json:"parent_thread_id"`
		}
		if err := json.Unmarshal(line.Payload, &payload); err != nil {
			return err
		}
		state.metaFound = true
		state.eligible = payload.ParentThreadID == nil || strings.TrimSpace(*payload.ParentThreadID) == ""
		state.activity.ThreadID = payload.ID
		if state.activity.ThreadID == "" {
			state.activity.ThreadID = payload.SessionID
		}
		state.activity.Workspace = payload.CWD
		state.activity.StartedAt = line.Timestamp
		if payload.Timestamp != nil {
			state.activity.StartedAt = *payload.Timestamp
		}
		switch strings.ToLower(payload.Originator) {
		case "codex desktop", "codex_work_desktop":
			state.activity.Tool = core.ToolCodexDesktop
		case "codex-tui", "codex_exec":
			state.activity.Tool = core.ToolCodexCLI
		default:
			state.eligible = false
		}
	case "turn_context":
		var payload struct {
			Model string `json:"model"`
		}
		if json.Unmarshal(line.Payload, &payload) == nil && payload.Model != "" {
			state.activity.Model = payload.Model
		}
	case "compacted":
		state.certain = false
		state.activeTask = false
		state.outstanding = make(map[string]string)
	case "event_msg":
		return state.applyEvent(line.Payload)
	case "response_item":
		return state.applyResponse(line.Payload)
	case "world_state", "inter_agent_communication_metadata":
		// Known non-boundary records deliberately carry no displayed data.
	default:
		state.certain = false
		state.activeTask = false
		state.outstanding = make(map[string]string)
	}
	return nil
}

func (state *fileState) applyEvent(data json.RawMessage) error {
	var payload struct {
		Type string `json:"type"`
		Info *struct {
			Last *struct {
				Total int64 `json:"total_tokens"`
			} `json:"last_token_usage"`
			Window int64 `json:"model_context_window"`
		} `json:"info"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}
	switch payload.Type {
	case "task_started", "user_message":
		state.certain = true
		state.activeTask = true
	case "task_complete":
		state.certain = true
		state.activeTask = false
		state.outstanding = make(map[string]string)
	case "token_count":
		if payload.Info != nil {
			state.activity.ContextWindow = payload.Info.Window
			if payload.Info.Last != nil {
				state.activity.ContextTokens = payload.Info.Last.Total
			}
		}
	}
	return nil
}

func (state *fileState) applyResponse(data json.RawMessage) error {
	var payload struct {
		Type   string `json:"type"`
		Name   string `json:"name"`
		CallID string `json:"call_id"`
		Role   string `json:"role"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}
	switch payload.Type {
	case "function_call", "custom_tool_call":
		state.certain = true
		state.activeTask = true
		state.activity.LastTool = payload.Name
		if payload.CallID != "" {
			state.outstanding[payload.CallID] = payload.Name
		}
	case "function_call_output", "custom_tool_call_output":
		if _, matched := state.outstanding[payload.CallID]; matched {
			delete(state.outstanding, payload.CallID)
			state.certain = true
			state.activeTask = true
		}
	case "message":
		if strings.EqualFold(payload.Role, "user") {
			state.certain = true
			state.activeTask = true
		}
	}
	return nil
}

func (state *fileState) activityState() core.ActivityState {
	if !state.certain {
		return core.ActivityUnknown
	}
	if len(state.outstanding) > 0 {
		return core.ActivityExecuting
	}
	if state.activeTask {
		return core.ActivityThinking
	}
	return core.ActivityWaiting
}
