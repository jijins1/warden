package api

import (
	"sync"
	"time"
)

// pendingStream tracks a client that detached and can reattach.
type pendingStream struct {
	runID      int64
	lastSeq    int64
	detachedAt time.Time
}

// pendingPool manages detached clients waiting to reattach.
// Clients have 5 minutes to reconnect, then their slot is freed.
type pendingPool struct {
	mu      sync.Mutex
	streams map[int64]*pendingStream
	ticker  *time.Ticker
}

// newPendingPool creates a new detach pool.
func newPendingPool() *pendingPool {
	p := &pendingPool{
		streams: make(map[int64]*pendingStream),
		ticker:  time.NewTicker(30 * time.Second),
	}
	// Periodically clean up expired detached clients
	go p.reaper()
	return p
}

// mark records that a client has detached and can potentially reattach.
func (p *pendingPool) mark(runID int64, lastSeq int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.streams[runID] = &pendingStream{
		runID:      runID,
		lastSeq:    lastSeq,
		detachedAt: time.Now(),
	}
}

// claim tries to reattach a client, returning the last sequence it received.
// Returns (lastSeq, true) if reattach is allowed, or (0, false) if the
// reattach window has expired.
func (p *pendingPool) claim(runID int64) (int64, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	s, ok := p.streams[runID]
	if !ok {
		return 0, false
	}

	// Reattach window: 5 minutes
	if time.Since(s.detachedAt) > 5*time.Minute {
		delete(p.streams, runID)
		return 0, false
	}

	lastSeq := s.lastSeq
	delete(p.streams, runID)
	return lastSeq, true
}

// reaper periodically removes expired pending streams.
func (p *pendingPool) reaper() {
	for range p.ticker.C {
		p.mu.Lock()
		now := time.Now()
		for runID, s := range p.streams {
			if now.Sub(s.detachedAt) > 5*time.Minute {
				delete(p.streams, runID)
			}
		}
		p.mu.Unlock()
	}
}

// close stops the reaper goroutine.
func (p *pendingPool) close() {
	p.ticker.Stop()
}
