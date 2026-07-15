package service

import "testing"

func TestComfyClientHealthGate(t *testing.T) {
	c := &ComfyClient{BaseURL: "http://x"}
	if !c.Healthy() {
		t.Fatal("a fresh client must be healthy")
	}
	c.MarkUnhealthy()
	if c.Healthy() {
		t.Fatal("a client in cooldown must report unhealthy")
	}
	c.MarkHealthy()
	if !c.Healthy() {
		t.Fatal("MarkHealthy must clear the cooldown")
	}
}

func TestComfyPoolNextSkipsUnhealthy(t *testing.T) {
	a := &ComfyClient{BaseURL: "http://a"}
	b := &ComfyClient{BaseURL: "http://b"}
	p := &ComfyPool{clients: []*ComfyClient{a, b}}

	// Both healthy → round-robin, each returned equally.
	counts := map[string]int{}
	for i := 0; i < 4; i++ {
		counts[p.Next().BaseURL]++
	}
	if counts["http://a"] != 2 || counts["http://b"] != 2 {
		t.Fatalf("expected balanced round-robin 2/2, got %v", counts)
	}

	// a unhealthy → Next must always return b.
	a.MarkUnhealthy()
	for i := 0; i < 4; i++ {
		if got := p.Next().BaseURL; got != "http://b" {
			t.Fatalf("unhealthy a must be skipped, got %s", got)
		}
	}

	// Both unhealthy → must still return a backend (never nil), so the caller can
	// try/fail-over rather than stall.
	b.MarkUnhealthy()
	if p.Next() == nil {
		t.Fatal("all-unhealthy pool must still return a backend, not nil")
	}

	// Empty pool → nil, Size 0.
	empty := &ComfyPool{}
	if empty.Next() != nil {
		t.Fatal("empty pool Next must be nil")
	}
	if empty.Size() != 0 || p.Size() != 2 {
		t.Fatalf("Size mismatch: empty=%d p=%d", empty.Size(), p.Size())
	}
}
