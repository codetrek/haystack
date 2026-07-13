package invertedstore

import "sync"

// postingBudget is a variable-amount counting semaphore bounding in-flight postings (spec §7, E). The
// producer acquire()s before enqueuing an apply; the apply release()s after running. A request larger
// than the whole budget is capped (acquire/release the same capped amount) so it never self-deadlocks.
type postingBudget struct {
	mu   sync.Mutex
	cond *sync.Cond
	cap  int64
	used int64
}

func newPostingBudget(capacity int64) *postingBudget {
	if capacity <= 0 {
		capacity = 1
	}
	b := &postingBudget{cap: capacity}
	b.cond = sync.NewCond(&b.mu)
	return b
}

// acquire blocks until n (capped at the budget) tokens are free, reserves them, and returns the
// amount actually reserved (which the caller MUST later release exactly). n<=0 reserves nothing.
func (b *postingBudget) acquire(n int64) int64 {
	if n <= 0 {
		return 0
	}
	if n > b.cap {
		n = b.cap
	}
	b.mu.Lock()
	for b.used+n > b.cap {
		b.cond.Wait()
	}
	b.used += n
	b.mu.Unlock()
	return n
}

func (b *postingBudget) release(n int64) {
	if n <= 0 {
		return
	}
	b.mu.Lock()
	b.used -= n
	b.cond.Broadcast()
	b.mu.Unlock()
}
