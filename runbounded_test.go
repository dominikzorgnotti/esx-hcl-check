package main

import (
	"sync/atomic"
	"testing"
)

// Run with -race to verify the worker pool has no data races: each fn writes
// only its own slot, so concurrent writes to distinct indices are safe.
func TestRunBoundedOrderAndRace(t *testing.T) {
	const n = 1000
	got := make([]int, n)
	runBounded(n, 8, func(i int) {
		got[i] = i * 2 // each goroutine owns exactly slot i
	})
	for i := 0; i < n; i++ {
		if got[i] != i*2 {
			t.Fatalf("slot %d = %d, want %d", i, got[i], i*2)
		}
	}
}

func TestRunBoundedRespectsLimit(t *testing.T) {
	var inFlight, maxInFlight int32
	const workers = 4
	runBounded(200, workers, func(i int) {
		cur := atomic.AddInt32(&inFlight, 1)
		for {
			old := atomic.LoadInt32(&maxInFlight)
			if cur <= old || atomic.CompareAndSwapInt32(&maxInFlight, old, cur) {
				break
			}
		}
		// brief busy spin to encourage overlap
		for j := 0; j < 1000; j++ {
			_ = j
		}
		atomic.AddInt32(&inFlight, -1)
	})
	if maxInFlight > workers {
		t.Fatalf("observed %d concurrent workers, limit was %d", maxInFlight, workers)
	}
}

// TestRunBoundedOneIsSequential locks the user-facing guarantee that -workers=1
// processes one host at a time (never two concurrently).
func TestRunBoundedOneIsSequential(t *testing.T) {
	var inFlight, maxInFlight int32
	runBounded(100, 1, func(i int) {
		cur := atomic.AddInt32(&inFlight, 1)
		for {
			old := atomic.LoadInt32(&maxInFlight)
			if cur <= old || atomic.CompareAndSwapInt32(&maxInFlight, old, cur) {
				break
			}
		}
		for j := 0; j < 1000; j++ {
			_ = j
		}
		atomic.AddInt32(&inFlight, -1)
	})
	if maxInFlight != 1 {
		t.Fatalf("workers=1 ran %d concurrently, want strictly 1 (sequential)", maxInFlight)
	}
}

func TestRunBoundedZeroWorkersIsSequential(t *testing.T) {
	got := make([]int, 10)
	runBounded(10, 0, func(i int) { got[i] = i + 1 }) // 0 -> treated as 1
	for i := 0; i < 10; i++ {
		if got[i] != i+1 {
			t.Fatalf("slot %d = %d, want %d", i, got[i], i+1)
		}
	}
}
