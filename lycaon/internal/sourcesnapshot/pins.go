package sourcesnapshot

import (
	"context"
	"sync"
)

// pinSet holds the manifests and chunks that work in this process still
// reads or is about to reference. The sweep deletes only what is both
// unreferenced in the store and unpinned, under the same lock, so a chunk
// written before its manifest commits, or a base a capture reads from,
// survives a concurrent sweep.
type pinSet struct {
	mu        sync.Mutex
	manifests map[string]int
	chunks    map[string]int
}

func newPinSet() *pinSet {
	return &pinSet{manifests: make(map[string]int), chunks: make(map[string]int)}
}

// pin is a release handle for one set of held ids.
type pin struct {
	set       *pinSet
	manifests []string
	chunks    []string
}

func (p *pinSet) holdManifests(ids ...string) *pin {
	held := &pin{set: p}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, id := range ids {
		if id == "" {
			continue
		}
		p.manifests[id]++
		held.manifests = append(held.manifests, id)
	}
	return held
}

func (p *pin) addChunk(id string) {
	if p == nil {
		return
	}
	p.set.mu.Lock()
	p.set.chunks[id]++
	p.set.mu.Unlock()
	p.chunks = append(p.chunks, id)
}

func (p *pin) addManifest(id string) {
	if p == nil {
		return
	}
	p.set.mu.Lock()
	p.set.manifests[id]++
	p.set.mu.Unlock()
	p.manifests = append(p.manifests, id)
}

func (p *pin) release() {
	if p == nil || p.set == nil {
		return
	}
	p.set.mu.Lock()
	defer p.set.mu.Unlock()
	for _, id := range p.manifests {
		release(p.set.manifests, id)
	}
	for _, id := range p.chunks {
		release(p.set.chunks, id)
	}
	p.manifests, p.chunks = nil, nil
}

func release(counts map[string]int, id string) {
	if counts[id] <= 1 {
		delete(counts, id)
		return
	}
	counts[id]--
}

// sweepUnpinned runs one sweep transaction with the pin set frozen, so no
// pin taken during it can name a row the transaction deletes.
func (p *pinSet) sweepUnpinned(ctx context.Context, fn func(manifestPinned, chunkPinned func(string) bool) error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn(
		func(id string) bool { return p.manifests[id] > 0 },
		func(id string) bool { return p.chunks[id] > 0 },
	)
}
