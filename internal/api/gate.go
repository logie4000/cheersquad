package api

import (
	"context"
	"time"
)

// gate limits how many plans run at once, and how many of those have
// crossing points. Requests over the limit wait in line for a while
// rather than being turned away, since most plans finish in milliseconds.
type gate struct {
	line      chan struct{} // requests running or waiting
	plans     chan struct{} // plans running
	crossings chan struct{} // plans with crossings running
	wait      time.Duration
}

func newGate(plans, crossings, queued int, wait time.Duration) *gate {
	return &gate{
		line:      make(chan struct{}, plans+queued),
		plans:     make(chan struct{}, plans),
		crossings: make(chan struct{}, crossings),
		wait:      wait,
	}
}

// join takes a place in line, before the request body is read. It reports
// false at once if the line is full.
func (g *gate) join() (leave func(), ok bool) {
	select {
	case g.line <- struct{}{}:
		return func() { <-g.line }, true
	default:
		return nil, false
	}
}

// enter waits up to g.wait for a slot to run a plan, and a crossing slot
// too if the plan has crossings. It reports false if none frees up in time
// or ctx ends first.
func (g *gate) enter(ctx context.Context, crossings bool) (release func(), ok bool) {
	ctx, cancel := context.WithTimeout(ctx, g.wait)
	defer cancel()
	if crossings {
		if !take(ctx, g.crossings) {
			return nil, false
		}
		if !take(ctx, g.plans) {
			<-g.crossings
			return nil, false
		}
		return func() { <-g.plans; <-g.crossings }, true
	}
	if !take(ctx, g.plans) {
		return nil, false
	}
	return func() { <-g.plans }, true
}

func take(ctx context.Context, sem chan struct{}) bool {
	select {
	case sem <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}
