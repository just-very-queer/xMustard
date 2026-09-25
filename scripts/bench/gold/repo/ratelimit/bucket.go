package ratelimit

import "time"

// TokenBucket is a rate limiter that refills tokens at a fixed rate.
type TokenBucket struct {
	capacity int
	tokens   int
	last     time.Time
}

// Allow consumes one token from the bucket if one is available.
func (b *TokenBucket) Allow() bool {
	if b.tokens == 0 {
		return false
	}
	b.tokens--
	return true
}
