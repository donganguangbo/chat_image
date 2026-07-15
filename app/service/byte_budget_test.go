package service

import (
	"sync"
	"testing"
)

func TestByteBudget(t *testing.T) {
	b := newByteBudget(100)

	// fits exactly to the cap
	if !b.acquire(60) {
		t.Fatal("acquire 60 should succeed")
	}
	if !b.acquire(40) {
		t.Fatal("acquire 40 (total 100) should succeed at the cap")
	}
	// one more byte over the cap is refused
	if b.acquire(1) {
		t.Fatal("acquire 1 over the cap should fail")
	}
	// releasing frees room again
	b.release(40)
	if !b.acquire(40) {
		t.Fatal("acquire 40 after release should succeed")
	}

	// zero-size (status poll) always fits, even at the cap
	if !b.acquire(0) {
		t.Fatal("acquire 0 should always succeed")
	}

	// nil budget is unbounded (disabled)
	var nilB *byteBudget
	if !nilB.acquire(1 << 40) {
		t.Fatal("nil budget must be unbounded")
	}
	nilB.release(1 << 40) // must not panic
}

// Concurrent acquire/release must never let `used` exceed max or go negative;
// go test -race also checks the locking.
func TestByteBudgetConcurrent(t *testing.T) {
	b := newByteBudget(1000)
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				if b.acquire(100) {
					b.release(100)
				}
			}
		}()
	}
	wg.Wait()
	// all released → back to zero, so the full cap is acquirable again
	if !b.acquire(1000) {
		t.Fatalf("after all releases the budget should be fully free, used=%d", b.used)
	}
}
