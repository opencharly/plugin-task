package task

import (
	"fmt"
	"sort"
	"strings"
)

// graph.go — the depends_on resolution: a topological closure (dependency-first) with
// cycle detection. A task's dependencies run before it, in declared order, each at
// most once.

// closure returns the tasks to run for `names`, dependency-first, deduplicated, with
// every task's depends_on recursively included. A cycle is a hard error naming the
// chain. The returned order is stable: a depth-first post-order over the declared
// depends_on lists.
func (ts *taskSet) closure(names []string) ([]string, error) {
	var order []string
	state := map[string]int{} // 0 unvisited, 1 in-progress, 2 done
	var stack []string

	var visit func(name string) error
	visit = func(name string) error {
		switch state[name] {
		case 1:
			return fmt.Errorf("task dependency cycle: %s", strings.Join(append(stack, name), " -> "))
		case 2:
			return nil
		}
		t, ok := ts.tasks[name]
		if !ok {
			return fmt.Errorf("task %q depends on unknown task %q", stack[len(stack)-1], name)
		}
		state[name] = 1
		stack = append(stack, name)
		for _, dep := range t.DependsOn {
			if err := visit(dep); err != nil {
				return err
			}
		}
		stack = stack[:len(stack)-1]
		state[name] = 2
		order = append(order, name)
		return nil
	}

	// visit roots in the caller's order; a missing root is reported against itself.
	for _, n := range names {
		if _, ok := ts.tasks[n]; !ok {
			return nil, fmt.Errorf("unknown task %q (declared tasks: %s)", n, strings.Join(sortedNames(ts.names()), ", "))
		}
		if err := visit(n); err != nil {
			return nil, err
		}
	}
	return order, nil
}

func sortedNames(names []string) []string {
	out := append([]string(nil), names...)
	sort.Strings(out)
	return out
}
