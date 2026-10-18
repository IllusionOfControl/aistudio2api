package app

import (
	"context"
	"sync"
)

// startupLimiter bounds concurrently starting workers; limits can be adjusted dynamically at runtime.
type startupLimiter struct {
	mu      sync.Mutex
	limit   int
	active  int
	changed chan struct{}
}

// setLimit sets concurrent startup limit and wakes waiting workers.
func (limiter *startupLimiter) setLimit(limit int) {
	limiter.mu.Lock()
	limiter.limit = limit
	limiter.notifyLocked()
	limiter.mu.Unlock()
}

// size returns concurrent startup limit.
func (limiter *startupLimiter) size() int {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	return limiter.limit
}

// acquire waits for a startup slot, returning context error if ctx is cancelled.
func (limiter *startupLimiter) acquire(ctx context.Context) error {
	for {
		limiter.mu.Lock()
		if limiter.active < limiter.limit {
			limiter.active++
			limiter.mu.Unlock()
			return nil
		}
		if limiter.changed == nil {
			limiter.changed = make(chan struct{})
		}
		changed := limiter.changed
		limiter.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// release returns a startup slot and wakes waiting workers.
func (limiter *startupLimiter) release() {
	limiter.mu.Lock()
	limiter.active--
	limiter.notifyLocked()
	limiter.mu.Unlock()
}

// notifyLocked wakes all waiting workers.
func (limiter *startupLimiter) notifyLocked() {
	if limiter.changed != nil {
		close(limiter.changed)
		limiter.changed = nil
	}
}
