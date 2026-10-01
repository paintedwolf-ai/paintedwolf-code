package scopedstore

import (
	"fmt"
	"sync"
	"testing"
)

func TestLRUBoundHoldsPastCapacity(t *testing.T) {
	c := New[int](8)
	for i := 0; i < 8*4; i++ {
		c.Store(fmt.Sprintf("session-%d", i), i)
	}
	if c.Len() != 8 {
		t.Fatalf("length = %d, want 8", c.Len())
	}
	if _, ok := c.Load("session-0"); ok {
		t.Fatal("oldest session must be evicted")
	}
	if got, ok := c.Load("session-31"); !ok || got != 31 {
		t.Fatalf("newest session = %d ok=%v", got, ok)
	}
}

func TestLRUReStoreRefreshesInPlace(t *testing.T) {
	c := New[int](2)
	c.Store("a", 1)
	c.Store("b", 2)
	c.Store("a", 3)
	if c.Len() != 2 {
		t.Fatalf("length = %d, want 2", c.Len())
	}
	if got, _ := c.Load("a"); got != 3 {
		t.Fatalf("a = %d, want 3", got)
	}
}

func TestLRULoadAndDeleteRemoves(t *testing.T) {
	c := New[string](4)
	c.Store("job-1", "digest")
	got, ok := c.LoadAndDelete("job-1")
	if !ok || got != "digest" {
		t.Fatalf("LoadAndDelete = %q ok=%v", got, ok)
	}
	if _, ok := c.LoadAndDelete("job-1"); ok {
		t.Fatal("second LoadAndDelete must miss")
	}
	if c.Len() != 0 {
		t.Fatalf("length = %d, want 0", c.Len())
	}
}

func TestLRUClearEmptiesInPlace(t *testing.T) {
	c := New[int](4)
	c.Store("a", 1)
	c.Store("b", 2)
	c.Clear()
	if c.Len() != 0 {
		t.Fatalf("length after Clear = %d, want 0", c.Len())
	}
	c.Store("c", 3)
	if got, ok := c.Load("c"); !ok || got != 3 {
		t.Fatalf("store after Clear = %d ok=%v", got, ok)
	}
}

func TestLRUConcurrentAccess(t *testing.T) {
	c := New[int](16)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			key := fmt.Sprintf("session-%d", n%8)
			c.Store(key, n)
			c.Load(key)
			c.LoadAndDelete(fmt.Sprintf("session-%d", (n+1)%8))
		}(i)
	}
	wg.Wait()
	if c.Len() > 16 {
		t.Fatalf("length = %d, want <= 16", c.Len())
	}
}

func TestLRULoadOrStoreKeepsTheFirstValue(t *testing.T) {
	c := New[int](4)
	if got, loaded := c.LoadOrStore("session-1", 7); loaded || got != 7 {
		t.Fatalf("first LoadOrStore = %d loaded=%v, want 7 false", got, loaded)
	}
	if got, loaded := c.LoadOrStore("session-1", 9); !loaded || got != 7 {
		t.Fatalf("second LoadOrStore = %d loaded=%v, want 7 true", got, loaded)
	}
	if c.Len() != 1 {
		t.Fatalf("length = %d, want 1", c.Len())
	}
}

// Both insertion paths share the capacity limit.
func TestLRULoadOrStoreEvictsPastCapacity(t *testing.T) {
	c := New[int](2)
	c.LoadOrStore("a", 1)
	c.LoadOrStore("b", 2)
	c.LoadOrStore("c", 3)
	if c.Len() != 2 {
		t.Fatalf("length = %d, want 2", c.Len())
	}
	if _, ok := c.Load("a"); ok {
		t.Fatal("least recently touched entry survived past capacity")
	}
}
