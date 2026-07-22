package core

import "fmt"

// AssessmentStatus is the trust-level classification of a session assessment.
type AssessmentStatus string

const (
	AssessmentOK          AssessmentStatus = "ok"
	AssessmentNeedsReview AssessmentStatus = "needs-review"
	AssessmentUnknown     AssessmentStatus = "unknown"
)

// SessionAssessment is a conservative, human-readable explanation of a
// session's lifecycle state. It is advisory only: it never authorizes
// termination and never depends on activity or semantic role data.
type SessionAssessment struct {
	Status  AssessmentStatus
	Label   string
	Summary string
	Reasons []string
}

// AssessSession derives a conservative assessment from lifecycle state,
// confidence, exact process identity, and process membership only. It must
// never receive or reference activity or role information (Safety Invariants
// #2, #3, #5, #7).
func AssessSession(session Session) SessionAssessment {
	rootPresent := false
	for _, p := range session.Processes {
		if SameProcess(session.Root, p.Identity()) {
			rootPresent = true
			break
		}
	}

	switch session.State {
	case StateRunning:
		if rootPresent {
			return SessionAssessment{
				Status:  AssessmentOK,
				Label:   "ACTIVE",
				Summary: "No cleanup suggested",
				Reasons: []string{
					"The original process identity is still present",
					"TaskWatch can still validate its process family",
				},
			}
		}
		return SessionAssessment{
			Status:  AssessmentUnknown,
			Label:   "UNKNOWN",
			Summary: "Session reports running, but its original process identity could not be validated",
			Reasons: []string{
				"The session state is running, but the original PID-plus-creation-time identity was not found among its processes",
			},
		}

	case StateExited:
		if len(session.Processes) == 0 {
			return SessionAssessment{
				Status:  AssessmentOK,
				Label:   "FINISHED",
				Summary: "No cleanup suggested",
				Reasons: []string{
					"The session exited and no processes remain",
				},
			}
		}
		return SessionAssessment{
			Status:  AssessmentUnknown,
			Label:   "UNKNOWN",
			Summary: "Session reports exited, but processes are still present",
			Reasons: []string{
				"The session state is exited, but its process collection is not empty",
			},
		}

	case StateLeftovers:
		count := len(session.Processes)
		if count == 0 {
			return SessionAssessment{
				Status:  AssessmentUnknown,
				Label:   "UNKNOWN",
				Summary: "Session reports leftovers, but no remaining processes were found",
				Reasons: []string{
					"The session state is leftovers, but its process collection is empty",
				},
			}
		}
		if session.Confidence != ConfidenceSupervised {
			return SessionAssessment{
				Status:  AssessmentUnknown,
				Label:   "UNKNOWN",
				Summary: "Required evidence is insufficient to review this observed session",
				Reasons: []string{
					"Observed sessions are never classified as leftovers; TaskWatch cannot supervise their Job Object membership",
				},
			}
		}
		return SessionAssessment{
			Status:  AssessmentNeedsReview,
			Label:   "NEEDS REVIEW",
			Summary: fmt.Sprintf("The original supervised agent exited, but %d validated processes remain", count),
			Reasons: []string{
				"The original PID-plus-creation-time identity is gone",
				"The remaining processes still belong to its supervised Job Object",
				fmt.Sprintf("TaskWatch can account for all %d remaining process identities", count),
			},
		}

	case StateInaccessible:
		return SessionAssessment{
			Status:  AssessmentUnknown,
			Label:   "UNKNOWN",
			Summary: "Required evidence is unreadable for this session",
			Reasons: []string{
				"TaskWatch could not read the process evidence needed to assess this session",
			},
		}

	case StateStale:
		return SessionAssessment{
			Status:  AssessmentUnknown,
			Label:   "UNKNOWN",
			Summary: "This session's evidence failed to refresh",
			Reasons: []string{
				"TaskWatch could not refresh this session's process evidence",
			},
		}

	default:
		return SessionAssessment{
			Status:  AssessmentUnknown,
			Label:   "UNKNOWN",
			Summary: "This session's state is not recognized",
			Reasons: []string{
				fmt.Sprintf("Unrecognized session state %q", session.State),
			},
		}
	}
}
