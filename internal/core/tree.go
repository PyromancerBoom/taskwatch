package core

import "sort"

// CollectTree returns the validated root and its descendants in deterministic
// breadth-first order. Siblings are ordered by PID, then creation time and name.
func CollectTree(processes []Process, root ProcessIdentity) []Process {
	ordered := append([]Process(nil), processes...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].PID != ordered[j].PID {
			return ordered[i].PID < ordered[j].PID
		}
		if !ordered[i].CreatedAt.Equal(ordered[j].CreatedAt) {
			return ordered[i].CreatedAt.Before(ordered[j].CreatedAt)
		}
		return ordered[i].Name < ordered[j].Name
	})

	rootIndex := -1
	for i := range ordered {
		if SameProcess(root, ordered[i].Identity()) {
			rootIndex = i
			break
		}
	}
	if rootIndex == -1 {
		return nil
	}

	result := make([]Process, 0, len(ordered))
	queue := []Process{ordered[rootIndex]}
	seen := make(map[uint32]struct{}, len(ordered))
	seen[root.PID] = struct{}{}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		result = append(result, current)

		for _, candidate := range ordered {
			if candidate.ParentPID != current.PID {
				continue
			}
			if candidate.CreatedAt.IsZero() || candidate.CreatedAt.Before(current.CreatedAt) {
				continue
			}
			if _, exists := seen[candidate.PID]; exists {
				continue
			}
			seen[candidate.PID] = struct{}{}
			queue = append(queue, candidate)
		}
	}

	return result
}
