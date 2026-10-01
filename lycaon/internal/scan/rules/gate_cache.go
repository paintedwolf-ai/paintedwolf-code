package rules

import (
	"bytes"
	"container/list"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"sync"

	"golang.org/x/sync/singleflight"
)

const gateCacheBytes = 16 << 20

type gateCacheEntry struct {
	key  string
	data []byte
}

type gateCache struct {
	mu      sync.Mutex
	entries map[string]*list.Element
	order   list.List
	bytes   int
	flights singleflight.Group
}

var compiledGates = gateCache{entries: make(map[string]*list.Element)}

// Hash the ordered names and bytes; edits, missing files, and selection changes stay visible.
func gateFilesKey(files []GateRuleFile) string {
	hash := sha256.New()
	var size [8]byte
	for _, file := range files {
		for _, data := range [][]byte{[]byte(file.Name), file.Data} {
			binary.LittleEndian.PutUint64(size[:], uint64(len(data)))
			_, _ = hash.Write(size[:])
			_, _ = hash.Write(data)
		}
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func (c *gateCache) get(key string) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	if item := c.entries[key]; item != nil {
		c.order.MoveToFront(item)
		return item.Value.(gateCacheEntry).data
	}
	return nil
}

func (c *gateCache) store(key string, data []byte) {
	if len(data) > gateCacheBytes/2 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = c.order.PushFront(gateCacheEntry{key: key, data: data})
	c.bytes += len(data)
	for c.order.Len() > 8 || c.bytes > gateCacheBytes {
		oldest := c.order.Back()
		entry := oldest.Value.(gateCacheEntry)
		delete(c.entries, entry.key)
		c.bytes -= len(entry.data)
		c.order.Remove(oldest)
	}
}

func (c *gateCache) compile(files []GateRuleFile) ([]byte, error) {
	key := gateFilesKey(files)
	if data := c.get(key); data != nil {
		return bytes.Clone(data), nil
	}
	result, err, _ := c.flights.Do(key, func() (any, error) {
		if data := c.get(key); data != nil {
			return data, nil
		}
		data, err := CompileGateRuleFiles(files)
		if err != nil {
			return nil, err
		}
		c.store(key, data)
		return data, nil
	})
	if err != nil {
		return nil, err
	}
	return bytes.Clone(result.([]byte)), nil
}
