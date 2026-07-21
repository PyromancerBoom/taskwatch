package codex

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PyromancerBoom/taskwatch/internal/core"
)

var testNow = time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)

func TestCollectParsesCurrentTopLevelActivityAndAppendsState(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "rollout.jsonl")
	writeRollout(t, path,
		line(`session_meta`, `{"id":"thread-1","timestamp":"2026-07-21T11:59:00Z","cwd":"C:\\repo","originator":"codex-tui"}`),
		line(`turn_context`, `{"model":"gpt-5.6"}`),
		line(`event_msg`, `{"type":"task_started"}`),
		line(`response_item`, `{"type":"function_call","name":"exec","call_id":"call-1","arguments":"not decoded"}`),
		line(`event_msg`, `{"type":"token_count","info":{"last_token_usage":{"total_tokens":82069},"model_context_window":258400}}`),
	)
	setModified(t, path, testNow)

	collector := New(root)
	got, err := collector.Collect(testNow)
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Collect() activities = %#v, want one", got)
	}
	activity := got[0]
	if activity.ThreadID != "thread-1" || activity.Tool != core.ToolCodexCLI || activity.Workspace != `C:\repo` || activity.Model != "gpt-5.6" || activity.LastTool != "exec" || activity.State != core.ActivityExecuting {
		t.Fatalf("activity = %#v", activity)
	}
	if activity.ContextTokens != 82069 || activity.ContextWindow != 258400 || !activity.StartedAt.Equal(time.Date(2026, 7, 21, 11, 59, 0, 0, time.UTC)) || !activity.UpdatedAt.Equal(testNow) {
		t.Fatalf("activity timing/context = %#v", activity)
	}

	appendRollout(t, path,
		line(`response_item`, `{"type":"function_call_output","call_id":"call-1","output":"not decoded"}`),
		line(`event_msg`, `{"type":"task_complete"}`),
	)
	setModified(t, path, testNow.Add(time.Second))
	got, err = collector.Collect(testNow.Add(time.Second))
	if err != nil || len(got) != 1 || got[0].State != core.ActivityWaiting {
		t.Fatalf("Collect() after append = %#v, %v, want waiting", got, err)
	}
}

func TestCollectAppendsFromCachedCompleteOffset(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "rollout.jsonl")
	writeRollout(t, path,
		line(`session_meta`, `{"id":"thread-1","originator":"codex-tui"}`),
		line(`event_msg`, `{"type":"task_started"}`),
	)
	setModified(t, path, testNow)

	collector := New(root)
	if got, err := collector.Collect(testNow); err != nil || len(got) != 1 || got[0].State != core.ActivityThinking {
		t.Fatalf("initial Collect() = %#v, %v", got, err)
	}
	cached := collector.files[path]
	offset := cached.offset

	appendRollout(t, path, line(`event_msg`, `{"type":"task_complete"}`))
	setModified(t, path, testNow.Add(time.Second))
	got, err := collector.Collect(testNow.Add(time.Second))
	if err != nil || len(got) != 1 || got[0].State != core.ActivityWaiting {
		t.Fatalf("appended Collect() = %#v, %v", got, err)
	}
	if collector.files[path] != cached {
		t.Fatal("append replaced the cached parser state instead of resuming it")
	}
	if cached.offset <= offset {
		t.Fatalf("cached offset = %d, want greater than %d", cached.offset, offset)
	}
}

func TestCollectDerivesConservativeActivityStates(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  core.ActivityState
	}{
		{name: "bootstrap only", want: core.ActivityUnknown},
		{name: "task active", lines: []string{line("event_msg", `{"type":"task_started"}`)}, want: core.ActivityThinking},
		{name: "trailing user turn", lines: []string{line("response_item", `{"type":"message","role":"user","content":"not decoded"}`)}, want: core.ActivityThinking},
		{name: "unmatched call", lines: []string{line("response_item", `{"type":"function_call","name":"exec","call_id":"one","arguments":"not decoded"}`)}, want: core.ActivityExecuting},
		{name: "matched call remains active", lines: []string{
			line("response_item", `{"type":"custom_tool_call","name":"apply_patch","call_id":"one","input":"not decoded"}`),
			line("response_item", `{"type":"custom_tool_call_output","call_id":"one","output":"not decoded"}`),
		}, want: core.ActivityThinking},
		{name: "completed", lines: []string{line("event_msg", `{"type":"task_complete"}`)}, want: core.ActivityWaiting},
		{name: "compacted", lines: []string{line("event_msg", `{"type":"task_started"}`), line("compacted", `{}`)}, want: core.ActivityUnknown},
		{name: "explicit boundary after compaction", lines: []string{line("compacted", `{}`), line("event_msg", `{"type":"task_complete"}`)}, want: core.ActivityWaiting},
		{name: "unknown top-level record", lines: []string{line("event_msg", `{"type":"task_started"}`), line("new_state_record", `{}`)}, want: core.ActivityUnknown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "rollout.jsonl")
			lines := append([]string{line("session_meta", `{"id":"thread","originator":"codex-tui"}`)}, test.lines...)
			writeRollout(t, path, lines...)
			setModified(t, path, testNow)
			got, err := New(root).Collect(testNow)
			if err != nil || len(got) != 1 || got[0].State != test.want {
				t.Fatalf("Collect() = %#v, %v; want state %q", got, err, test.want)
			}
		})
	}
}

func TestCollectReturnsRedactedPartialActivityWithAggregatedRecordWarnings(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "rollout.jsonl")
	secret := "PROMPT_AND_TOOL_ARGUMENT_MUST_NOT_ESCAPE"
	oversized := `{"timestamp":"2026-07-21T12:00:00Z","type":"response_item","payload":{"type":"message","role":"user","content":"` + strings.Repeat(secret, maxRecordBytes/len(secret)+2) + `"}}` + "\n"
	writeRollout(t, path,
		line("session_meta", `{"id":"thread","originator":"codex-tui","cwd":"C:\\repo"}`),
		line("event_msg", `{"type":"task_started"}`),
		`{"type":`+"\n",
		oversized,
	)
	setModified(t, path, testNow)

	got, err := New(root).Collect(testNow)
	if len(got) != 1 || got[0].State != core.ActivityUnknown {
		t.Fatalf("Collect() = %#v, %v; want usable unknown activity", got, err)
	}
	if err == nil || !strings.Contains(err.Error(), "invalid record") || !strings.Contains(err.Error(), "oversized record") {
		t.Fatalf("Collect() warning = %v, want aggregated invalid/oversized warning", err)
	}
	if strings.Contains(fmt.Sprintf("%#v", got), secret) {
		t.Fatalf("activity leaked content: %#v", got)
	}
}

func TestCollectWarnsWhenReadBoundsSkipRolloutData(t *testing.T) {
	t.Run("bootstrap", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "rollout.jsonl")
		var rollout strings.Builder
		rollout.Grow(headerBytes + tailBytes + (512 << 10))
		rollout.WriteString(line("session_meta", `{"id":"thread","originator":"codex-tui"}`))
		rollout.WriteString(line("event_msg", `{"type":"task_started"}`))
		appendFiller(&rollout, headerBytes+(256<<10))
		rollout.WriteString(line("event_msg", `{"type":"task_complete"}`))
		appendFiller(&rollout, headerBytes+tailBytes+(512<<10))
		writeRollout(t, path, rollout.String())
		setModified(t, path, testNow)

		got, err := New(root).Collect(testNow)
		if len(got) != 1 || got[0].State != core.ActivityUnknown {
			t.Fatalf("Collect() = %#v, %v; want usable unknown row", got, err)
		}
		if err == nil || !strings.Contains(err.Error(), "read bound") {
			t.Fatalf("Collect() warning = %v, want skipped read-bound warning", err)
		}
	})

	t.Run("append", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "rollout.jsonl")
		writeRollout(t, path,
			line("session_meta", `{"id":"thread","originator":"codex-tui"}`),
			line("event_msg", `{"type":"task_started"}`),
		)
		setModified(t, path, testNow)
		collector := New(root)
		if got, err := collector.Collect(testNow); err != nil || len(got) != 1 || got[0].State != core.ActivityThinking {
			t.Fatalf("initial Collect() = %#v, %v", got, err)
		}

		var appended strings.Builder
		appended.Grow(tailBytes + (512 << 10))
		appendFiller(&appended, 256<<10)
		appended.WriteString(line("event_msg", `{"type":"task_complete"}`))
		appendFiller(&appended, tailBytes+(512<<10))
		appendRollout(t, path, appended.String())
		setModified(t, path, testNow.Add(time.Second))

		got, err := collector.Collect(testNow.Add(time.Second))
		if len(got) != 1 || got[0].State != core.ActivityUnknown {
			t.Fatalf("appended Collect() = %#v, %v; want usable unknown row", got, err)
		}
		if err == nil || !strings.Contains(err.Error(), "read bound") {
			t.Fatalf("appended warning = %v, want skipped read-bound warning", err)
		}
	})
}

func TestCollectResetsCachedStateWhenFileIsReplaced(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "rollout.jsonl")
	writeRollout(t, path, line("session_meta", `{"id":"one","originator":"codex-tui"}`), line("event_msg", `{"type":"task_complete"}`))
	setModified(t, path, testNow)
	collector := New(root)
	if got, err := collector.Collect(testNow); err != nil || len(got) != 1 || got[0].ThreadID != "one" {
		t.Fatalf("initial Collect() = %#v, %v", got, err)
	}
	cached := collector.files[path]
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	writeRollout(t, path, line("session_meta", `{"id":"two","originator":"codex-tui"}`), line("event_msg", `{"type":"task_started"}`))
	setModified(t, path, testNow.Add(time.Second))
	got, err := collector.Collect(testNow.Add(time.Second))
	if err != nil || len(got) != 1 || got[0].ThreadID != "two" || got[0].State != core.ActivityThinking {
		t.Fatalf("replacement Collect() = %#v, %v", got, err)
	}
	if collector.files[path] == cached {
		t.Fatal("replacement reused cached parser state")
	}
}

func TestCollectFiltersSourcesSubagentsAndExpiredFiles(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name       string
		meta       string
		modifiedAt time.Time
	}{
		{name: "desktop", meta: `{"id":"desktop","originator":"Codex Desktop","cwd":"C:\\desktop"}`, modifiedAt: testNow},
		{name: "legacy desktop", meta: `{"id":"legacy","originator":"codex_work_desktop"}`, modifiedAt: testNow},
		{name: "exec", meta: `{"id":"exec","originator":"codex_exec"}`, modifiedAt: testNow},
		{name: "subagent", meta: `{"id":"child","originator":"codex-tui","parent_thread_id":"parent"}`, modifiedAt: testNow},
		{name: "unknown", meta: `{"id":"unknown","originator":"other"}`, modifiedAt: testNow},
		{name: "expired", meta: `{"id":"expired","originator":"codex-tui"}`, modifiedAt: testNow.Add(-16 * time.Minute)},
	}
	for _, test := range tests {
		path := filepath.Join(root, test.name+".jsonl")
		writeRollout(t, path, line("session_meta", test.meta))
		setModified(t, path, test.modifiedAt)
	}

	got, err := New(root).Collect(testNow)
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if len(got) != 3 || got[0].ThreadID != "desktop" || got[0].Tool != core.ToolCodexDesktop || got[1].ThreadID != "exec" || got[2].ThreadID != "legacy" {
		t.Fatalf("Collect() = %#v, want desktop/exec/legacy only in stable order", got)
	}
}

func TestCollectRetriesPartialFinalLineAndResetsAfterShrink(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "rollout.jsonl")
	partial := line("session_meta", `{"id":"one","originator":"codex-tui"}`)
	if err := os.WriteFile(path, []byte(strings.TrimSuffix(partial, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	setModified(t, path, testNow)
	collector := New(root)
	if got, err := collector.Collect(testNow); err != nil || len(got) != 0 {
		t.Fatalf("partial Collect() = %#v, %v, want no activity", got, err)
	}
	appendRollout(t, path, "\n")
	setModified(t, path, testNow.Add(time.Second))
	if got, err := collector.Collect(testNow.Add(time.Second)); err != nil || len(got) != 1 || got[0].ThreadID != "one" {
		t.Fatalf("completed Collect() = %#v, %v", got, err)
	}

	writeRollout(t, path,
		line("session_meta", `{"id":"two","originator":"codex-tui"}`),
		line("event_msg", `{"type":"task_complete"}`),
	)
	setModified(t, path, testNow.Add(2*time.Second))
	if got, err := collector.Collect(testNow.Add(2 * time.Second)); err != nil || len(got) != 1 || got[0].ThreadID != "two" || got[0].State != core.ActivityWaiting {
		t.Fatalf("rewritten Collect() = %#v, %v", got, err)
	}
}

func TestCollectTreatsPartialStateRecordAsUnknownAndRetriesIt(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "rollout.jsonl")
	writeRollout(t, path,
		line("session_meta", `{"id":"one","originator":"codex-tui"}`),
		line("event_msg", `{"type":"task_started"}`),
		line("response_item", `{"type":"function_call","name":"exec","call_id":"call"}`),
	)
	setModified(t, path, testNow)
	collector := New(root)
	if got, err := collector.Collect(testNow); err != nil || len(got) != 1 || got[0].State != core.ActivityExecuting {
		t.Fatalf("initial Collect() = %#v, %v", got, err)
	}

	partial := strings.TrimSuffix(line("response_item", `{"type":"function_call_output","call_id":"call"}`), "\n")
	appendRollout(t, path, partial)
	setModified(t, path, testNow.Add(time.Second))
	if got, err := collector.Collect(testNow.Add(time.Second)); err != nil || len(got) != 1 || got[0].State != core.ActivityUnknown {
		t.Fatalf("partial Collect() = %#v, %v, want unknown", got, err)
	}

	appendRollout(t, path, "\n")
	setModified(t, path, testNow.Add(2*time.Second))
	if got, err := collector.Collect(testNow.Add(2 * time.Second)); err != nil || len(got) != 1 || got[0].State != core.ActivityThinking {
		t.Fatalf("completed Collect() = %#v, %v, want thinking", got, err)
	}
}

func TestCollectMissingRootIsEmptyAndRecentCompressedFileWarns(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if got, err := New(missing).Collect(testNow); err != nil || len(got) != 0 {
		t.Fatalf("missing Collect() = %#v, %v", got, err)
	}

	root := t.TempDir()
	path := filepath.Join(root, "rollout.jsonl.zst")
	if err := os.WriteFile(path, []byte("compressed"), 0o600); err != nil {
		t.Fatal(err)
	}
	setModified(t, path, testNow)
	if got, err := New(root).Collect(testNow); len(got) != 0 || err == nil || !strings.Contains(err.Error(), "compressed") {
		t.Fatalf("compressed Collect() = %#v, %v, want aggregate warning", got, err)
	}
}

func TestCollectScansOnlyRootAndCurrentDateDirectories(t *testing.T) {
	root := t.TempDir()
	localNow := testNow.In(time.Local)
	paths := map[string]string{
		"root":       filepath.Join(root, "root.jsonl"),
		"today":      filepath.Join(root, localNow.Format(filepath.Join("2006", "01", "02")), "today.jsonl"),
		"yesterday":  filepath.Join(root, localNow.AddDate(0, 0, -1).Format(filepath.Join("2006", "01", "02")), "yesterday.jsonl"),
		"historical": filepath.Join(root, localNow.AddDate(0, 0, -2).Format(filepath.Join("2006", "01", "02")), "historical.jsonl"),
	}
	for id, path := range paths {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		writeRollout(t, path, line("session_meta", fmt.Sprintf(`{"id":%q,"originator":"codex-tui"}`, id)))
		setModified(t, path, testNow)
	}

	got, err := New(root).Collect(testNow)
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("Collect() = %#v, want root/today/yesterday only", got)
	}
	for _, activity := range got {
		if activity.ThreadID == "historical" {
			t.Fatalf("Collect() traversed historical rollout directory: %#v", got)
		}
	}
}

func TestCollectCapsActiveRolloutFiles(t *testing.T) {
	root := t.TempDir()
	for index := 0; index < maxActiveFiles+1; index++ {
		path := filepath.Join(root, fmt.Sprintf("rollout-%03d.jsonl", index))
		writeRollout(t, path, line("session_meta", fmt.Sprintf(`{"id":"thread-%03d","originator":"codex-tui"}`, index)))
		setModified(t, path, testNow)
	}

	got, err := New(root).Collect(testNow)
	if len(got) != maxActiveFiles {
		t.Fatalf("Collect() returned %d activities, want capped %d", len(got), maxActiveFiles)
	}
	if err == nil || !strings.Contains(err.Error(), "active rollout limit") {
		t.Fatalf("Collect() warning = %v, want active rollout limit warning", err)
	}
}

func TestDefaultRootHonorsCodexHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	root, err := DefaultRoot()
	if err != nil || root != filepath.Join(home, "sessions") {
		t.Fatalf("DefaultRoot() = %q, %v", root, err)
	}
}

func line(recordType, payload string) string {
	return `{"timestamp":"2026-07-21T12:00:00Z","type":"` + recordType + `","payload":` + payload + "}\n"
}

func writeRollout(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "")), 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendRollout(t *testing.T, path string, lines ...string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString(strings.Join(lines, "")); err != nil {
		t.Fatal(err)
	}
}

func setModified(t *testing.T, path string, at time.Time) {
	t.Helper()
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

func appendFiller(builder *strings.Builder, target int) {
	filler := line("world_state", `{}`)
	for builder.Len() < target {
		builder.WriteString(filler)
	}
}
