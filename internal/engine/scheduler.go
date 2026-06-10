package engine

import (
	"context"
	"sort"
)

// node is one schedulable job in the global queue. Needs only lists
// dependencies that are themselves scheduled; jobs gated on unscheduled
// work were already marked blocked during planning.
type node struct {
	workflowIndex int
	jobID         string
	rank          float64
	weight        int
	needs         []int
}

// nodeResult is the outcome of one dispatched node.
type nodeResult struct {
	dispatched bool
	failed     bool
	err        error
}

type completion struct {
	index  int
	failed bool
	err    error
}

// runQueue executes nodes with at most `slots` weighted slots in flight,
// always preferring the highest-rank ready node that fits the free slots.
// A node becomes ready when all its needs have completed, whatever their
// outcome: act evaluates job-level if-conditions against dependency
// results, so dispatching after a failed dependency keeps GitHub
// semantics. When failFast is set, nothing new is dispatched after the
// first failure or after ctx is cancelled; running nodes drain.
func runQueue(ctx context.Context, nodes []*node, slots int, failFast bool, exec func(int) (bool, error)) []nodeResult {
	results := make([]nodeResult, len(nodes))
	if len(nodes) == 0 {
		return results
	}
	if slots < 1 {
		slots = 1
	}

	waiting := map[int]int{}
	dependents := map[int][]int{}
	var ready []int
	for index, current := range nodes {
		waiting[index] = len(current.needs)
		for _, need := range current.needs {
			dependents[need] = append(dependents[need], index)
		}
		if len(current.needs) == 0 {
			ready = append(ready, index)
		}
	}
	sortByRank(nodes, ready)

	completions := make(chan completion)
	free := slots
	running := 0
	failed := false

	dispatch := func() {
		if failed && failFast || ctx.Err() != nil {
			return
		}
		// Highest rank first; lower-ranked nodes backfill remaining slots so
		// detection time never waits on idle capacity.
		for position := 0; position < len(ready); {
			index := ready[position]
			weight := nodes[index].weight
			if weight > slots {
				weight = slots
			}
			if weight > free {
				position++
				continue
			}
			ready = append(ready[:position], ready[position+1:]...)
			free -= weight
			running++
			results[index].dispatched = true
			go func(index int) {
				nodeFailed, err := exec(index)
				completions <- completion{index: index, failed: nodeFailed, err: err}
			}(index)
		}
	}

	dispatch()
	for running > 0 {
		done := <-completions
		running--
		weight := nodes[done.index].weight
		if weight > slots {
			weight = slots
		}
		free += weight
		results[done.index].failed = done.failed
		results[done.index].err = done.err
		if done.failed {
			failed = true
		}
		newlyReady := false
		for _, dependent := range dependents[done.index] {
			waiting[dependent]--
			if waiting[dependent] == 0 {
				ready = append(ready, dependent)
				newlyReady = true
			}
		}
		if newlyReady {
			sortByRank(nodes, ready)
		}
		dispatch()
	}
	return results
}

func sortByRank(nodes []*node, ready []int) {
	sort.SliceStable(ready, func(i, j int) bool {
		return nodes[ready[i]].rank > nodes[ready[j]].rank
	})
}
