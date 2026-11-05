package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"
)

// MemoryCache is a bounded, process-local TTL cache. Values are copied on read
// and write so handlers cannot mutate cached data after publication.
type MemoryCache struct {
	mu      sync.RWMutex
	entries map[string]memoryCacheEntry
	max     int
}

type memoryCacheEntry struct {
	value     []byte
	expiresAt time.Time
}

func NewMemoryCache(maxEntries int) *MemoryCache {
	if maxEntries <= 0 {
		maxEntries = 10000
	}
	return &MemoryCache{entries: make(map[string]memoryCacheEntry), max: maxEntries}
}

func (c *MemoryCache) Get(key string) ([]byte, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	item, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if !time.Now().Before(item.expiresAt) {
		delete(c.entries, key)
		return nil, false
	}
	return bytes.Clone(item.value), true
}

func (c *MemoryCache) Set(key string, value []byte, ttl time.Duration) {
	if c == nil || ttl <= 0 || len(value) == 0 {
		return
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, item := range c.entries {
		if !now.Before(item.expiresAt) {
			delete(c.entries, k)
		}
	}
	if _, exists := c.entries[key]; !exists && len(c.entries) >= c.max {
		// Evict the entry that will expire first; the cache is an optimization,
		// so a miss is always preferable to unbounded memory growth.
		oldestKey := ""
		var oldest time.Time
		for k, item := range c.entries {
			if oldestKey == "" || item.expiresAt.Before(oldest) {
				oldestKey, oldest = k, item.expiresAt
			}
		}
		if oldestKey != "" {
			delete(c.entries, oldestKey)
		}
	}
	c.entries[key] = memoryCacheEntry{value: bytes.Clone(value), expiresAt: now.Add(ttl)}
}

func (c *MemoryCache) Delete(key string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	delete(c.entries, key)
	c.mu.Unlock()
}

func (c *MemoryCache) DeletePrefix(prefix string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	for key := range c.entries {
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			delete(c.entries, key)
		}
	}
	c.mu.Unlock()
}

// cacheAnalytics is mounted after authentication and plan authorization. The
// user ID is part of the key, preventing data from leaking between accounts.
func (e *Env) cacheAnalytics(ttl time.Duration, endpoint string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, ok := analyticsUser(r)
			if !ok || e.cache == nil || r.Method != http.MethodGet {
				next.ServeHTTP(w, r)
				return
			}
			key := "analytics:" + user + ":" + endpoint
			if cached, found := e.cache.Get(key); found {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.Header().Set("Cache-Control", "private, no-store")
				w.Header().Set("X-AdPulse-Cache", "HIT")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(cached)
				return
			}

			recorder := httptest.NewRecorder()
			next.ServeHTTP(recorder, r)
			for name, values := range recorder.Header() {
				for _, value := range values {
					w.Header().Add(name, value)
				}
			}
			w.Header().Set("X-AdPulse-Cache", "MISS")
			if recorder.Code == http.StatusOK && recorder.Body.Len() <= 2*1024*1024 {
				e.cache.Set(key, recorder.Body.Bytes(), ttl)
			}
			w.WriteHeader(recorder.Code)
			_, _ = w.Write(recorder.Body.Bytes())
		})
	}
}

type cachedTrackedLink struct {
	URL string `json:"url"`
	UserID string `json:"user_id"`
}

// resolveTrackedLink caches only the stable slug destination. Click quotas,
// click counters and event writes remain database-backed and are never cached.
func (e *Env) resolveTrackedLink(ctx context.Context, slug string) (string, string, error) {
	key := "link:" + slug
	if cached, ok := e.cache.Get(key); ok {
		var link cachedTrackedLink
		if json.Unmarshal(cached, &link) == nil && link.URL != "" && link.UserID != "" {
			return link.URL, link.UserID, nil
		}
		e.cache.Delete(key)
	}
	var link cachedTrackedLink
	if err := e.db.QueryRowContext(ctx, "SELECT url, user_id FROM link_Tracker_Link WHERE slug=?", slug).Scan(&link.URL, &link.UserID); err != nil {
		return "", "", err
	}
	if encoded, err := json.Marshal(link); err == nil {
		e.cache.Set(key, encoded, 5*time.Minute)
	}
	return link.URL, link.UserID, nil
}
