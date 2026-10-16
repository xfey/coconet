package main

import (
	"math/rand/v2"
	"time"
)

// The local head check is small. Network polling slows once the graph stops
// changing; a new local revision immediately makes the next pass eligible.
type projectPoll struct {
	revision int64
	seen     bool
	idle     int
	failures int
	next     time.Time
}

func (p *projectPoll) due(now time.Time, revision int64) bool {
	return !p.seen || revision != p.revision || !now.Before(p.next)
}
func (p *projectPoll) complete(now time.Time, before, after int64, pending bool, err error) {
	changed := !p.seen || before != after || p.revision != after
	p.revision = after
	p.seen = true
	delay := 5 * time.Second
	if err != nil {
		p.failures++
		delay = time.Duration(1<<min(p.failures, 6)) * time.Second
	} else {
		p.failures = 0
		if changed || pending {
			p.idle = 0
		} else {
			p.idle++
		}
		if p.idle >= 2 {
			delay = time.Minute
		}
	}
	// Up to ten percent jitter avoids synchronized fleets.
	delay += time.Duration(rand.Int64N(int64(delay/10) + 1))
	p.next = now.Add(delay)
}
