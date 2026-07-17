package core

import "sort"

// ProcessTreeRow is a process and its nesting depth in a process forest.
type ProcessTreeRow struct {
	Process Process
	Depth   int
}

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

// CollectProcessForest returns every supplied process once in deterministic
// preorder. Invalid parent relationships become roots, and cyclic components
// are rooted at their first process in the established process ordering.
func CollectProcessForest(processes []Process) []ProcessTreeRow {
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

	candidatesByPID := make(map[uint32][]int, len(ordered))
	for i := range ordered {
		candidatesByPID[ordered[i].PID] = append(candidatesByPID[ordered[i].PID], i)
	}

	// A PID can be reused, so a ParentPID may match several processes. The
	// correct parent is the most recently created one that still predates
	// the child, not simply the first process ever seen with that PID.
	parents := make([]int, len(ordered))
	for i := range parents {
		parents[i] = -1
		if ordered[i].ParentPID == ordered[i].PID || ordered[i].CreatedAt.IsZero() {
			continue
		}
		best := -1
		for _, candidate := range candidatesByPID[ordered[i].ParentPID] {
			if ordered[candidate].CreatedAt.IsZero() || ordered[candidate].CreatedAt.After(ordered[i].CreatedAt) {
				continue
			}
			if best == -1 || ordered[candidate].CreatedAt.After(ordered[best].CreatedAt) {
				best = candidate
			}
		}
		parents[i] = best
	}

	for i := range ordered {
		positions := make(map[int]int)
		var path []int
		for current := i; current != -1; current = parents[current] {
			start, exists := positions[current]
			if exists {
				root := path[start]
				for _, candidate := range path[start+1:] {
					if candidate < root {
						root = candidate
					}
				}
				parents[root] = -1
				break
			}
			positions[current] = len(path)
			path = append(path, current)
		}
	}

	children := make([][]int, len(ordered))
	var roots []int
	for child, parent := range parents {
		if parent == -1 {
			roots = append(roots, child)
			continue
		}
		children[parent] = append(children[parent], child)
	}

	rows := make([]ProcessTreeRow, 0, len(ordered))
	var appendTree func(int, int)
	appendTree = func(index, depth int) {
		rows = append(rows, ProcessTreeRow{Process: ordered[index], Depth: depth})
		for _, child := range children[index] {
			appendTree(child, depth+1)
		}
	}
	for _, root := range roots {
		appendTree(root, 0)
	}
	return rows
}
