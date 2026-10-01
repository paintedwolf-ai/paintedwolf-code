package stream

import (
	"strings"
	"testing"
)

func TestStreamReplayCacheEvictsOldest(t *testing.T) {
	c := newStreamReplayCache(3)
	for _, id := range []string{"m1", "m2", "m3", "m4"} {
		c.put(id, streamReplayEntry{content: id, tokens: []string{id}})
	}
	if c.order.Len() != 3 {
		t.Fatalf("cache length = %d, want 3", c.order.Len())
	}
	if _, ok := c.get("m1"); ok {
		t.Fatal("oldest entry must be evicted past capacity")
	}
	if entry, ok := c.get("m4"); !ok || entry.content != "m4" {
		t.Fatalf("newest entry missing: %+v ok=%v", entry, ok)
	}
}

func TestStreamReplayCacheRefreshDoesNotGrow(t *testing.T) {
	c := newStreamReplayCache(2)
	c.put("m1", streamReplayEntry{content: "first"})
	c.put("m2", streamReplayEntry{content: "second"})
	c.put("m1", streamReplayEntry{content: "first-updated"})
	if c.order.Len() != 2 {
		t.Fatalf("re-put grew the cache: length = %d, want 2", c.order.Len())
	}
	entry, ok := c.get("m1")
	if !ok || entry.content != "first-updated" {
		t.Fatalf("re-put must refresh in place: %+v ok=%v", entry, ok)
	}
}

func TestStreamReplayCacheGetRefreshesRecency(t *testing.T) {
	c := newStreamReplayCache(2)
	c.put("m1", streamReplayEntry{content: "one"})
	c.put("m2", streamReplayEntry{content: "two"})
	if _, ok := c.get("m1"); !ok {
		t.Fatal("m1 must still be cached")
	}
	c.put("m3", streamReplayEntry{content: "three"})
	if _, ok := c.get("m1"); !ok {
		t.Fatal("recently read entry must survive eviction")
	}
	if _, ok := c.get("m2"); ok {
		t.Fatal("least recently touched entry must be evicted")
	}
}

func TestCacheLiveStreamPreservesExactChunks(t *testing.T) {
	m := New(nil)
	m.CacheLive("sess-1", "msg-1", "hello  world", []string{"hello  ", "world"}, 2)
	content, ok := m.Content("msg-1")
	if !ok || content != "hello  world" {
		t.Fatalf("StreamContent = %q ok=%v", content, ok)
	}
	tokens, ok := m.Tokens("msg-1")
	if !ok || strings.Join(tokens, "") != content {
		t.Fatalf("StreamTokens = %v ok=%v", tokens, ok)
	}
	m.CacheReplay("msg-2", "solo", nil)
	tokens, ok = m.Tokens("msg-2")
	if ok || len(tokens) != 0 {
		t.Fatalf("missing exact chunks must remain a snapshot fallback: %v ok=%v", tokens, ok)
	}
}

func TestStreamReplayCacheBoundedByCapacity(t *testing.T) {
	m := New(nil)
	for i := 0; i < streamReplayCacheEntries*4; i++ {
		m.CacheReplay(string(rune('a'+i%26))+string(rune('0'+i/26)), "body", []string{"body"})
	}
	m.streamMu.Lock()
	n := m.streamReplay.order.Len()
	m.streamMu.Unlock()
	if n > streamReplayCacheEntries {
		t.Fatalf("cache length = %d, want <= %d", n, streamReplayCacheEntries)
	}
}

func TestCacheStreamReplayDoesNotMarkMessageActive(t *testing.T) {
	m := New(nil)
	m.CacheReplay("settled", "done", []string{"done"})
	if active := m.ActiveMessageID("session"); active != "" {
		t.Fatalf("active message = %q", active)
	}
}
