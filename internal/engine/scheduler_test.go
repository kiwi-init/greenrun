package engine

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// orderedExec records dispatch order and completes nodes immediately.
type orderedExec struct {
	mu    sync.Mutex
	order []int
	fail  map[int]bool
}

func (e *orderedExec) run(index int) (bool, error) {
	e.mu.Lock()
	e.order = append(e.order, index)
	e.mu.Unlock()
	return e.fail[index], nil
}

func (e *orderedExec) dispatched() []int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]int(nil), e.order...)
}

func TestRunQueueDispatchesByRankAcrossWorkflows(t *testing.T) {
	nodes := []*node{
		{workflowIndex: 0, jobID: "deploy", rank: 10, weight: 1},
		{workflowIndex: 1, jobID: "lint", rank: 100, weight: 1},
		{workflowIndex: 0, jobID: "test", rank: 80, weight: 1},
	}
	exec := &orderedExec{}
	results := runQueue(context.Background(), nodes, 1, true, exec.run)
	require.Equal(t, []int{1, 2, 0}, exec.dispatched())
	for _, result := range results {
		require.True(t, result.dispatched)
		require.False(t, result.failed)
	}
}

func TestRunQueueGatesDependentsOnCompletion(t *testing.T) {
	nodes := []*node{
		{workflowIndex: 0, jobID: "build", rank: 100, weight: 1, needs: []int{1}},
		{workflowIndex: 0, jobID: "lint", rank: 1, weight: 1},
	}
	exec := &orderedExec{}
	runQueue(context.Background(), nodes, 4, true, exec.run)
	require.Equal(t, []int{1, 0}, exec.dispatched())
}

func TestRunQueueStopsDispatchingAfterFailureWhenFailFast(t *testing.T) {
	nodes := []*node{
		{workflowIndex: 0, jobID: "lint", rank: 100, weight: 1},
		{workflowIndex: 0, jobID: "test", rank: 10, weight: 1},
	}
	exec := &orderedExec{fail: map[int]bool{0: true}}
	results := runQueue(context.Background(), nodes, 1, true, exec.run)
	require.Equal(t, []int{0}, exec.dispatched())
	require.True(t, results[0].failed)
	require.False(t, results[1].dispatched)
}

func TestRunQueueCompleteModeRunsDependentsOfFailures(t *testing.T) {
	nodes := []*node{
		{workflowIndex: 0, jobID: "lint", rank: 100, weight: 1},
		{workflowIndex: 0, jobID: "package", rank: 50, weight: 1, needs: []int{0}},
		{workflowIndex: 0, jobID: "test", rank: 10, weight: 1},
	}
	exec := &orderedExec{fail: map[int]bool{0: true}}
	results := runQueue(context.Background(), nodes, 1, false, exec.run)
	// The dependent still dispatches: act evaluates its if-condition
	// against the failed dependency, exactly as GitHub would.
	require.Equal(t, []int{0, 1, 2}, exec.dispatched())
	require.True(t, results[0].failed)
	require.True(t, results[1].dispatched)
	require.True(t, results[2].dispatched)
}

func TestRunQueueBackfillsAroundWideJobs(t *testing.T) {
	releaseWide := make(chan struct{})
	dispatched := make(chan int, 3)
	nodes := []*node{
		{workflowIndex: 0, jobID: "matrix", rank: 100, weight: 2},
		{workflowIndex: 0, jobID: "wide", rank: 90, weight: 2},
		{workflowIndex: 0, jobID: "narrow", rank: 10, weight: 1},
	}
	exec := func(index int) (bool, error) {
		dispatched <- index
		if index == 0 {
			<-releaseWide
		}
		return false, nil
	}
	done := make(chan []nodeResult, 1)
	go func() { done <- runQueue(context.Background(), nodes, 3, true, exec) }()

	// The top-ranked wide job starts, "wide" (weight 2) cannot fit in the
	// remaining slot, and the low-ranked narrow job backfills it. The two
	// dispatch goroutines race to report, so compare as a set.
	first := map[int]bool{<-dispatched: true, <-dispatched: true}
	require.Equal(t, map[int]bool{0: true, 2: true}, first)
	close(releaseWide)
	require.Equal(t, 1, <-dispatched)

	results := <-done
	for _, result := range results {
		require.True(t, result.dispatched)
	}
}

func TestRunQueueOversizedWeightStillRuns(t *testing.T) {
	nodes := []*node{{workflowIndex: 0, jobID: "matrix", rank: 1, weight: 9}}
	exec := &orderedExec{}
	results := runQueue(context.Background(), nodes, 2, true, exec.run)
	require.True(t, results[0].dispatched)
}

func TestRunQueueHonorsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	nodes := []*node{{workflowIndex: 0, jobID: "lint", rank: 1, weight: 1}}
	exec := &orderedExec{}
	results := runQueue(ctx, nodes, 1, true, exec.run)
	require.False(t, results[0].dispatched)
	require.Empty(t, exec.dispatched())
}

func TestRunQueueRunsIndependentJobsConcurrently(t *testing.T) {
	var mu sync.Mutex
	running, peak := 0, 0
	barrier := make(chan struct{})
	exec := func(int) (bool, error) {
		mu.Lock()
		running++
		if running > peak {
			peak = running
		}
		ready := running == 2
		mu.Unlock()
		if ready {
			close(barrier)
		}
		select {
		case <-barrier:
		case <-time.After(5 * time.Second):
			t.Error("jobs never overlapped")
		}
		mu.Lock()
		running--
		mu.Unlock()
		return false, nil
	}
	nodes := []*node{
		{workflowIndex: 0, jobID: "a", rank: 2, weight: 1},
		{workflowIndex: 1, jobID: "b", rank: 1, weight: 1},
	}
	runQueue(context.Background(), nodes, 2, true, exec)
	require.Equal(t, 2, peak)
}

func TestDefaultConcurrencyBounds(t *testing.T) {
	require.Equal(t, 2, DefaultConcurrency(1))
	require.Equal(t, 2, DefaultConcurrency(4))
	require.Equal(t, 4, DefaultConcurrency(8))
	require.Equal(t, 8, DefaultConcurrency(32))
}
