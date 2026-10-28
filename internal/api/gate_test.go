package api

import (
	"context"
	"testing"
	"time"
)

func TestGateWaitsForSlot(t *testing.T) {
	g := newGate(1, 1, 1, time.Second)
	release, ok := g.enter(context.Background(), false)
	if !ok {
		t.Fatal("first plan should get the free slot")
	}
	time.AfterFunc(50*time.Millisecond, release)
	start := time.Now()
	if _, ok := g.enter(context.Background(), false); !ok {
		t.Fatal("second plan should get the slot once the first finishes")
	}
	if d := time.Since(start); d < 40*time.Millisecond {
		t.Fatalf("second plan did not wait (%s)", d)
	}
}

func TestGateGivesUpAfterWait(t *testing.T) {
	g := newGate(1, 1, 1, 20*time.Millisecond)
	g.enter(context.Background(), false)
	if _, ok := g.enter(context.Background(), false); ok {
		t.Fatal("got a slot that was never released")
	}
}

func TestGateLimitsCrossingPlans(t *testing.T) {
	g := newGate(3, 1, 0, 20*time.Millisecond)
	if _, ok := g.enter(context.Background(), true); !ok {
		t.Fatal("first crossing plan should run")
	}
	if _, ok := g.enter(context.Background(), true); ok {
		t.Fatal("second crossing plan should wait for the crossing slot")
	}
	// The failed attempt must not have leaked a slot: two plain plans
	// still fit beside the crossing plan.
	for i := 0; i < 2; i++ {
		if _, ok := g.enter(context.Background(), false); !ok {
			t.Fatalf("plain plan %d should run beside the crossing plan", i)
		}
	}
}

func TestGateLineIsBounded(t *testing.T) {
	g := newGate(1, 1, 1, time.Second)
	g.join()
	g.join()
	if _, ok := g.join(); ok {
		t.Fatal("a third request should be turned away from a line of two")
	}
}

func TestPlanBusyWhenLineFull(t *testing.T) {
	g := newGate(1, 1, 0, time.Second)
	g.join()
	rec := httptestPlan(t, g)
	if rec.Code != 503 || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("status %d, want 503 with Retry-After", rec.Code)
	}
}
