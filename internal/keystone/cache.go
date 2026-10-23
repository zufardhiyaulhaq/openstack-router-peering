package keystone

import (
	"context"
	"crypto/sha256"
	"sync"
	"time"
)

type cached struct {
	Client
	ttl     time.Duration
	mu      sync.Mutex
	entries map[[32]byte]cacheEntry
}

type cacheEntry struct {
	tok   Token
	until time.Time
}

// NewCached caches successful Validate results by sha256(token) for min(ttl, token expiry).
func NewCached(c Client, ttl time.Duration) Client {
	return &cached{Client: c, ttl: ttl, entries: map[[32]byte]cacheEntry{}}
}

func (c *cached) Validate(ctx context.Context, token string) (Token, error) {
	key := sha256.Sum256([]byte(token))
	now := time.Now()
	c.mu.Lock()
	if e, ok := c.entries[key]; ok && now.Before(e.until) {
		c.mu.Unlock()
		return e.tok, nil
	}
	c.mu.Unlock()
	tok, err := c.Client.Validate(ctx, token)
	if err != nil {
		return tok, err
	}
	until := now.Add(c.ttl)
	if !tok.ExpiresAt.IsZero() && tok.ExpiresAt.Before(until) {
		until = tok.ExpiresAt
	}
	c.mu.Lock()
	for k, e := range c.entries { // drop expired entries so the map cannot grow forever
		if now.After(e.until) {
			delete(c.entries, k)
		}
	}
	c.entries[key] = cacheEntry{tok: tok, until: until}
	c.mu.Unlock()
	return tok, nil
}
