package stealth

import (
	"math/rand"
	"time"
)

// Randomizer provides timing jitter for outgoing requests.
type Randomizer struct {
	minDelay time.Duration
	maxDelay time.Duration
	rng      *rand.Rand
}

// RandomizerOption configures a Randomizer.
type RandomizerOption func(*Randomizer)

// WithMinDelay sets the minimum delay.
func WithMinDelay(d time.Duration) RandomizerOption {
	return func(r *Randomizer) {
		r.minDelay = d
	}
}

// WithMaxDelay sets the maximum delay.
func WithMaxDelay(d time.Duration) RandomizerOption {
	return func(r *Randomizer) {
		r.maxDelay = d
	}
}

// NewRandomizer creates a new Randomizer with the given options.
func NewRandomizer(opts ...RandomizerOption) *Randomizer {
	r := &Randomizer{
		minDelay: 50 * time.Millisecond,
		maxDelay: 300 * time.Millisecond,
		rng:      rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Wait blocks for a random duration between minDelay and maxDelay.
func (r *Randomizer) Wait() {
	if r.minDelay == r.maxDelay {
		time.Sleep(r.minDelay)
		return
	}
	if r.maxDelay < r.minDelay {
		r.minDelay, r.maxDelay = r.maxDelay, r.minDelay
	}
	diff := r.maxDelay - r.minDelay
	n := r.rng.Int63n(int64(diff) + 1)
	time.Sleep(r.minDelay + time.Duration(n))
}
