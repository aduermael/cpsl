package main

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestDebouncerSingleTrigger(t *testing.T) {
	var count atomic.Int32
	d := newDebouncer(newDebouncerOptions{delay: 50 * time.Millisecond, fire: func() {
		count.Add(1)
	}})

	d.Trigger()
	time.Sleep(100 * time.Millisecond)

	if got := count.Load(); got != 1 {
		t.Fatalf("expected 1 fire, got %d", got)
	}
}

func TestDebouncerRapidTriggersFireOnce(t *testing.T) {
	var count atomic.Int32
	d := newDebouncer(newDebouncerOptions{delay: 50 * time.Millisecond, fire: func() {
		count.Add(1)
	}})

	// Trigger 10 times rapidly — only the last should fire
	for i := 0; i < 10; i++ {
		d.Trigger()
		time.Sleep(10 * time.Millisecond)
	}

	// Wait for debounce to settle
	time.Sleep(100 * time.Millisecond)

	if got := count.Load(); got != 1 {
		t.Fatalf("expected 1 fire after rapid triggers, got %d", got)
	}
}

func TestDebouncerResetsTimer(t *testing.T) {
	var count atomic.Int32
	d := newDebouncer(newDebouncerOptions{delay: 50 * time.Millisecond, fire: func() {
		count.Add(1)
	}})

	d.Trigger()
	time.Sleep(30 * time.Millisecond) // not yet fired
	d.Trigger()                        // reset
	time.Sleep(30 * time.Millisecond) // still not fired (only 30ms since reset)

	if got := count.Load(); got != 0 {
		t.Fatalf("expected 0 fires before debounce period, got %d", got)
	}

	time.Sleep(50 * time.Millisecond) // now it should have fired
	if got := count.Load(); got != 1 {
		t.Fatalf("expected 1 fire after debounce period, got %d", got)
	}
}
