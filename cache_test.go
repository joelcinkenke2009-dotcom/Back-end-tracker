package main

import (
	"bytes"
	"testing"
	"time"
)

func TestMemoryCacheCopiesValues(t *testing.T) {
	cache := NewMemoryCache(4)
	original := []byte("value")
	cache.Set("key", original, time.Minute)
	original[0] = 'X'
	got, ok := cache.Get("key")
	if !ok || !bytes.Equal(got, []byte("value")) {
		t.Fatalf("Get() = %q, %v; want value, true", got, ok)
	}
	got[0] = 'Y'
	again, ok := cache.Get("key")
	if !ok || !bytes.Equal(again, []byte("value")) {
		t.Fatalf("cached value was mutated: %q, %v", again, ok)
	}
}

func TestMemoryCacheExpiresAndDeletesPrefix(t *testing.T) {
	cache := NewMemoryCache(4)
	cache.Set("analytics:user:summary", []byte("a"), time.Millisecond)
	cache.Set("analytics:user:live", []byte("b"), time.Minute)
	cache.Set("analytics:other:summary", []byte("c"), time.Minute)
	time.Sleep(5 * time.Millisecond)
	if _, ok := cache.Get("analytics:user:summary"); ok {
		t.Fatal("expired entry was returned")
	}
	cache.DeletePrefix("analytics:user:")
	if _, ok := cache.Get("analytics:user:live"); ok {
		t.Fatal("prefix entry was not deleted")
	}
	if _, ok := cache.Get("analytics:other:summary"); !ok {
		t.Fatal("unrelated entry was deleted")
	}
}
