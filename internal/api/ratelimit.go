package api

import (
	"sync"
	"time"
)

// limiter is a keyed token bucket.
type limiter struct {
	mu    sync.Mutex
	burst float64
	rate  float64 // tokens per second
	now   func() time.Time
	m     map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

const limiterPruneAt = 4096

func newLimiter(burst, ratePerSec float64, now func() time.Time) *limiter {
	return &limiter{burst: burst, rate: ratePerSec, now: now, m: map[string]*bucket{}}
}

// allow takes one token for key. When it refuses, wait says how long until a
// token is available.
func (l *limiter) allow(key string) (ok bool, wait time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	t := l.now()
	b := l.m[key]
	if b == nil {
		if len(l.m) >= limiterPruneAt {
			l.prune(t)
		}
		b = &bucket{tokens: l.burst, last: t}
		l.m[key] = b
	}
	if el := t.Sub(b.last).Seconds(); el > 0 {
		b.tokens += el * l.rate
		if b.tokens > l.burst {
			b.tokens = l.burst
		}
	}
	b.last = t
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	need := (1 - b.tokens) / l.rate
	return false, time.Duration(need * float64(time.Second))
}

// prune drops buckets that have refilled completely. Caller holds l.mu.
func (l *limiter) prune(t time.Time) {
	for k, b := range l.m {
		if b.tokens+t.Sub(b.last).Seconds()*l.rate >= l.burst {
			delete(l.m, k)
		}
	}
}
