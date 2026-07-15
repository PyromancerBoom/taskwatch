package core

import "fmt"

// TerminationRequest is an immutable snapshot of an explicitly selected target
// set. Confirm returns a confirmed copy so confirmation cannot happen implicitly.
type TerminationRequest struct {
	sessionID string
	targets   []ProcessIdentity
	confirmed bool
}

func NewTerminationRequest(sessionID string, processes []Process) TerminationRequest {
	targets := make([]ProcessIdentity, len(processes))
	for i, process := range processes {
		targets[i] = process.Identity()
	}
	return TerminationRequest{sessionID: sessionID, targets: targets}
}

func (request TerminationRequest) SessionID() string {
	return request.sessionID
}

func (request TerminationRequest) Targets() []ProcessIdentity {
	return append([]ProcessIdentity(nil), request.targets...)
}

func (request TerminationRequest) Confirmed() bool {
	return request.confirmed
}

func (request TerminationRequest) Confirm() TerminationRequest {
	request.confirmed = true
	return request
}

// RevalidateObserved ensures a confirmed observed target set is byte-for-byte
// equivalent in process identity to the current snapshot before termination.
func RevalidateObserved(request TerminationRequest, current []Process) error {
	if !request.Confirmed() {
		return fmt.Errorf("termination request is not confirmed")
	}
	if len(request.targets) != len(current) {
		return fmt.Errorf("target count changed: confirmed %d, current %d", len(request.targets), len(current))
	}

	confirmed := make(map[uint32]ProcessIdentity, len(request.targets))
	for _, target := range request.targets {
		if target.PID == 0 || target.CreatedAt.IsZero() {
			return fmt.Errorf("confirmed target has invalid identity")
		}
		if _, exists := confirmed[target.PID]; exists {
			return fmt.Errorf("confirmed target PID %d is duplicated", target.PID)
		}
		confirmed[target.PID] = target
	}

	seen := make(map[uint32]struct{}, len(current))
	for _, process := range current {
		if _, exists := seen[process.PID]; exists {
			return fmt.Errorf("current target PID %d is duplicated", process.PID)
		}
		seen[process.PID] = struct{}{}

		target, exists := confirmed[process.PID]
		if !exists {
			return fmt.Errorf("target membership changed: PID %d was not confirmed", process.PID)
		}
		if !SameProcess(target, process.Identity()) {
			return fmt.Errorf("target PID %d was reused", process.PID)
		}
	}

	return nil
}
