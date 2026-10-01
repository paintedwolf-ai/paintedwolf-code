package sourcecatalog

import "strings"

// literalFileCache holds per-file blooms in one path trie per root, so a write
// drops only the subtree under its path. Eviction is first in, first out.
type literalFileCache struct {
	roots map[string]*literalFileNode
	order []literalFileRef
	bytes int64
	count int
	seq   uint64
}

type literalFileNode struct {
	children map[string]*literalFileNode
	cached   bool
	entry    literalFileCacheEntry
	cost     int64
	seq      uint64
}

// literalFileRef queues one stored observation. Eviction skips a ref whose seq
// no longer matches its node.
type literalFileRef struct {
	root, path string
	seq        uint64
}

type literalFileStep struct {
	parent  *literalFileNode
	segment string
}

func (c *literalFileCache) lookup(root, rel string) (literalFileCacheEntry, bool) {
	node := c.find(root, rel)
	if node == nil || !node.cached {
		return literalFileCacheEntry{}, false
	}
	return node.entry, true
}

func (c *literalFileCache) find(root, rel string) *literalFileNode {
	node := c.roots[root]
	for rest := rel; node != nil && rest != ""; {
		var segment string
		segment, rest, _ = strings.Cut(rest, "/")
		node = node.children[segment]
	}
	return node
}

func (c *literalFileCache) store(root, rel string, entry literalFileCacheEntry) {
	if c.roots == nil {
		c.roots = map[string]*literalFileNode{}
	}
	node := c.roots[root]
	if node == nil {
		node = &literalFileNode{}
		c.roots[root] = node
	}
	for rest := rel; rest != ""; {
		var segment string
		segment, rest, _ = strings.Cut(rest, "/")
		child := node.children[segment]
		if child == nil {
			if node.children == nil {
				node.children = map[string]*literalFileNode{}
			}
			child = &literalFileNode{}
			node.children[segment] = child
		}
		node = child
	}
	if node.cached {
		c.bytes -= node.cost
	} else {
		c.count++
	}
	c.seq++
	node.cached, node.entry, node.seq = true, entry, c.seq
	node.cost = literalFileCost(len(root)+1+len(rel), entry)
	c.bytes += node.cost
	c.order = append(c.order, literalFileRef{root: root, path: rel, seq: c.seq})
	for c.bytes > literalFileByteCap && len(c.order) > 0 {
		oldest := c.order[0]
		c.order[0] = literalFileRef{}
		c.order = c.order[1:]
		c.evict(oldest)
	}
	if len(c.order) > 2*c.count+1024 {
		c.compact()
	}
}

// drop removes every observation at or under rel; "." removes the root.
func (c *literalFileCache) drop(root, rel string) {
	node := c.roots[root]
	if node == nil {
		return
	}
	if rel == "." || rel == "" {
		c.release(node)
		delete(c.roots, root)
		return
	}
	steps := make([]literalFileStep, 0, strings.Count(rel, "/")+1)
	for rest := rel; rest != ""; {
		var segment string
		segment, rest, _ = strings.Cut(rest, "/")
		child := node.children[segment]
		if child == nil {
			return
		}
		steps = append(steps, literalFileStep{parent: node, segment: segment})
		node = child
	}
	c.release(node)
	node.children, node.cached = nil, false
	c.prune(root, steps)
}

func (c *literalFileCache) evict(ref literalFileRef) {
	node := c.roots[ref.root]
	steps := make([]literalFileStep, 0, strings.Count(ref.path, "/")+1)
	for rest := ref.path; node != nil && rest != ""; {
		var segment string
		segment, rest, _ = strings.Cut(rest, "/")
		steps = append(steps, literalFileStep{parent: node, segment: segment})
		node = node.children[segment]
	}
	if node == nil || !node.cached || node.seq != ref.seq {
		return
	}
	node.cached = false
	node.entry = literalFileCacheEntry{}
	c.bytes -= node.cost
	c.count--
	c.prune(ref.root, steps)
}

// release uncounts a subtree the caller is detaching.
func (c *literalFileCache) release(node *literalFileNode) {
	if node.cached {
		c.bytes -= node.cost
		c.count--
	}
	for _, child := range node.children {
		c.release(child)
	}
}

// prune detaches nodes left with no observation and no children, deepest first.
func (c *literalFileCache) prune(root string, steps []literalFileStep) {
	for i := len(steps) - 1; i >= 0; i-- {
		child := steps[i].parent.children[steps[i].segment]
		if child.cached || len(child.children) > 0 {
			return
		}
		delete(steps[i].parent.children, steps[i].segment)
	}
	if top := c.roots[root]; top != nil && !top.cached && len(top.children) == 0 {
		delete(c.roots, root)
	}
}

// compact drops refs that no longer name a live observation, bounding the
// queue by the entries it orders.
func (c *literalFileCache) compact() {
	kept := c.order[:0]
	for _, ref := range c.order {
		if node := c.find(ref.root, ref.path); node != nil && node.cached && node.seq == ref.seq {
			kept = append(kept, ref)
		}
	}
	clear(c.order[len(kept):])
	c.order = kept
}
