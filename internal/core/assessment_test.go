package core

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAssessSession(t *testing.T) {
	created := time.Unix(100, 0)
	root := ProcessIdentity{PID: 10, CreatedAt: created}

	tests := []struct {
		name       string
		session    Session
		wantStatus AssessmentStatus
		wantLabel  string
		summaryHas []string
	}{
		{
			name: "running with root present is active",
			session: Session{
				State:      StateRunning,
				Confidence: ConfidenceSupervised,
				Root:       root,
				Processes:  []Process{{PID: 10, CreatedAt: created}},
			},
			wantStatus: AssessmentOK,
			wantLabel:  "ACTIVE",
			summaryHas: []string{"No cleanup suggested"},
		},
		{
			name: "exited with no processes is finished",
			session: Session{
				State:      StateExited,
				Confidence: ConfidenceObserved,
				Root:       root,
			},
			wantStatus: AssessmentOK,
			wantLabel:  "FINISHED",
			summaryHas: []string{"No cleanup suggested"},
		},
		{
			name: "supervised leftovers with processes needs review",
			session: Session{
				State:      StateLeftovers,
				Confidence: ConfidenceSupervised,
				Root:       root,
				Processes: []Process{
					{PID: 20, CreatedAt: created},
					{PID: 21, CreatedAt: created},
					{PID: 22, CreatedAt: created},
					{PID: 23, CreatedAt: created},
					{PID: 24, CreatedAt: created},
					{PID: 25, CreatedAt: created},
					{PID: 26, CreatedAt: created},
					{PID: 27, CreatedAt: created},
					{PID: 28, CreatedAt: created},
					{PID: 29, CreatedAt: created},
					{PID: 30, CreatedAt: created},
					{PID: 31, CreatedAt: created},
				},
			},
			wantStatus: AssessmentNeedsReview,
			wantLabel:  "NEEDS REVIEW",
			summaryHas: []string{"12"},
		},
		{
			name: "observed leftovers is never needs review",
			session: Session{
				State:      StateLeftovers,
				Confidence: ConfidenceObserved,
				Root:       root,
				Processes:  []Process{{PID: 20, CreatedAt: created}},
			},
			wantStatus: AssessmentUnknown,
			wantLabel:  "UNKNOWN",
		},
		{
			name: "empty leftover state is unknown",
			session: Session{
				State:      StateLeftovers,
				Confidence: ConfidenceSupervised,
				Root:       root,
			},
			wantStatus: AssessmentUnknown,
			wantLabel:  "UNKNOWN",
		},
		{
			name: "running without root identity present is unknown",
			session: Session{
				State:      StateRunning,
				Confidence: ConfidenceSupervised,
				Root:       root,
				Processes:  []Process{{PID: 99, CreatedAt: created}},
			},
			wantStatus: AssessmentUnknown,
			wantLabel:  "UNKNOWN",
		},
		{
			name: "exited but processes remain is inconsistent and unknown",
			session: Session{
				State:      StateExited,
				Confidence: ConfidenceSupervised,
				Root:       root,
				Processes:  []Process{{PID: 20, CreatedAt: created}},
			},
			wantStatus: AssessmentUnknown,
			wantLabel:  "UNKNOWN",
		},
		{
			name: "inaccessible is unknown",
			session: Session{
				State:      StateInaccessible,
				Confidence: ConfidenceSupervised,
				Root:       root,
			},
			wantStatus: AssessmentUnknown,
			wantLabel:  "UNKNOWN",
		},
		{
			name: "stale is unknown",
			session: Session{
				State:      StateStale,
				Confidence: ConfidenceSupervised,
				Root:       root,
			},
			wantStatus: AssessmentUnknown,
			wantLabel:  "UNKNOWN",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AssessSession(tt.session)
			if got.Status != tt.wantStatus {
				t.Fatalf("Status = %q, want %q", got.Status, tt.wantStatus)
			}
			if got.Label != tt.wantLabel {
				t.Fatalf("Label = %q, want %q", got.Label, tt.wantLabel)
			}
			if len(got.Reasons) == 0 {
				t.Fatalf("Reasons must be non-empty, got %#v", got)
			}
			for _, want := range tt.summaryHas {
				if !strings.Contains(got.Summary, want) {
					t.Fatalf("Summary = %q, want it to contain %q", got.Summary, want)
				}
			}
		})
	}
}

func TestAssessSessionSupervisedLeftoversReasonsNameJobObjectAndExactCount(t *testing.T) {
	created := time.Unix(100, 0)
	root := ProcessIdentity{PID: 10, CreatedAt: created}
	session := Session{
		State:      StateLeftovers,
		Confidence: ConfidenceSupervised,
		Root:       root,
		Processes: []Process{
			{PID: 20, CreatedAt: created},
			{PID: 21, CreatedAt: created},
			{PID: 22, CreatedAt: created},
		},
	}

	got := AssessSession(session)

	if !strings.Contains(got.Summary, "3") {
		t.Fatalf("Summary = %q, want exact remaining count 3", got.Summary)
	}
	joined := strings.Join(got.Reasons, " | ")
	if !strings.Contains(joined, "Job Object") {
		t.Fatalf("Reasons = %q, want mention of Job Object", joined)
	}
	if !strings.Contains(joined, "3") {
		t.Fatalf("Reasons = %q, want mention of exact count 3", joined)
	}
}

func TestAssessSessionIgnoresActivityAndRoleData(t *testing.T) {
	created := time.Unix(100, 0)
	root := ProcessIdentity{PID: 10, CreatedAt: created}
	processes := []Process{{PID: 10, CreatedAt: created}}

	base := Session{
		State:      StateRunning,
		Confidence: ConfidenceSupervised,
		Root:       root,
		Processes:  processes,
	}

	withActivity := base
	withActivity.Activities = []AgentActivity{
		{ThreadID: "abc", State: ActivityThinking, Tool: ToolCodexCLI},
	}
	withActivity.ActivityMatchHint = ActivityMatchHint{ThreadID: "abc", Workspace: "C:\\work"}

	got1 := AssessSession(base)
	got2 := AssessSession(withActivity)

	if !reflect.DeepEqual(got1, got2) {
		t.Fatalf("assessment changed when activity data was added: %#v vs %#v", got1, got2)
	}

	// Verify the process collection passed in is not mutated.
	original := append([]Process(nil), processes...)
	AssessSession(base)
	if len(processes) != len(original) || processes[0] != original[0] {
		t.Fatalf("AssessSession mutated the input process collection: %#v, want %#v", processes, original)
	}
}
