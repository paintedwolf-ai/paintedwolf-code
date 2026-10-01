package sourcecatalog

import (
	"container/list"
	"context"
	"io"
	"os"
	"sync"
)

const structuralSpillBlockCacheBytes = 8 << 20

type structuralSpillBlockKey struct {
	spill  uint64
	offset int64
}

type structuralSpillBlockEntry struct {
	key     structuralSpillBlockKey
	body    []byte
	element *list.Element
}

type structuralSpillBlockCache struct {
	mu      sync.Mutex
	entries map[structuralSpillBlockKey]*structuralSpillBlockEntry
	lru     list.List
	bytes   int64
	limit   int64
}

var structuralSpillBlocks = structuralSpillBlockCache{entries: make(map[structuralSpillBlockKey]*structuralSpillBlockEntry), limit: structuralSpillBlockCacheBytes}

func readStructuralSpillBlockCached(ctx context.Context, file *os.File, spill uint64, dst []byte, offset, end int64) error {
	for len(dst) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		blockStart := offset / structuralSegmentReadBlock * structuralSegmentReadBlock
		blockEnd := blockStart + structuralSegmentReadBlock
		// Only full blocks are immutable while the spill is still growing.
		if blockEnd > end {
			amount := min(int64(len(dst)), end-offset)
			if amount <= 0 {
				return io.ErrUnexpectedEOF
			}
			if err := readAllAt(file, dst[:amount], offset); err != nil {
				return err
			}
			dst, offset = dst[amount:], offset+amount
			continue
		}
		blockOffset := int(offset - blockStart)
		block, err := structuralSpillBlocks.read(ctx, file, spill, blockStart, end)
		if err != nil {
			return err
		}
		if blockOffset < 0 || blockOffset >= len(block) {
			return io.ErrUnexpectedEOF
		}
		copied := copy(dst, block[blockOffset:])
		dst = dst[copied:]
		offset += int64(copied)
	}
	return nil
}

func (c *structuralSpillBlockCache) read(ctx context.Context, file *os.File, spill uint64, offset, end int64) ([]byte, error) {
	key := structuralSpillBlockKey{spill: spill, offset: offset}
	c.mu.Lock()
	if entry := c.entries[key]; entry != nil {
		c.lru.MoveToBack(entry.element)
		body := entry.body
		c.mu.Unlock()
		return body, nil
	}
	c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	amount := min(int64(structuralSegmentReadBlock), end-offset)
	if amount <= 0 {
		return nil, io.ErrUnexpectedEOF
	}
	body := make([]byte, int(amount))
	if err := readAllAt(file, body, offset); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry := c.entries[key]; entry != nil {
		c.lru.MoveToBack(entry.element)
		return entry.body, nil
	}
	entry := &structuralSpillBlockEntry{key: key, body: body}
	entry.element = c.lru.PushBack(entry)
	c.entries[key] = entry
	c.bytes += int64(len(body))
	c.evictLocked()
	return body, nil
}

func (c *structuralSpillBlockCache) evictLocked() {
	for c.bytes > c.limit && c.lru.Len() > 0 {
		front := c.lru.Front()
		entry := front.Value.(*structuralSpillBlockEntry)
		c.lru.Remove(front)
		delete(c.entries, entry.key)
		c.bytes -= int64(len(entry.body))
	}
}

func clearStructuralSpillBlocks(spill uint64) {
	structuralSpillBlocks.clear(spill)
}

func (c *structuralSpillBlockCache) clear(spill uint64) {
	if spill == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, entry := range c.entries {
		if key.spill != spill {
			continue
		}
		c.lru.Remove(entry.element)
		delete(c.entries, key)
		c.bytes -= int64(len(entry.body))
	}
}
