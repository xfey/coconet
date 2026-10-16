package main

import (
	"errors"
	"testing"
	"time"
)

func TestProjectPollIdlesAndWakesForLocalRevision(t *testing.T) {
	now := time.Now()
	p := &projectPoll{}
	if !p.due(now, 0) {
		t.Fatal("initial sync delayed")
	}
	p.complete(now, 0, 1, false, nil)
	for range 2 {
		now = p.next
		p.complete(now, 1, 1, false, nil)
	}
	if delay := p.next.Sub(now); delay < time.Minute || delay > 66*time.Second {
		t.Fatal("idle delay", delay)
	}
	if p.due(now.Add(time.Second), 1) {
		t.Fatal("idle network polling too frequent")
	}
	if !p.due(now.Add(time.Second), 2) {
		t.Fatal("new capture did not wake network sync")
	}
	p.complete(now, 2, 2, true, nil)
	if p.next.Sub(now) > 6*time.Second {
		t.Fatal("backlog treated as idle")
	}
	for range 10 {
		now = p.next
		p.complete(now, 2, 2, true, errors.New("offline"))
	}
	if p.next.Sub(now) > 71*time.Second {
		t.Fatal("backoff grew without bound")
	}
}
