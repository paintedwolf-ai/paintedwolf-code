package sourcetree

import (
	"context"
	"encoding/json"
	"path"
	"strings"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
)

// ReviewBuilder streams deleted addresses into sparse disk facts with bounded memory.
type ReviewBuilder struct {
	ctx         context.Context
	project     string
	catalog     *sourcecatalog.Catalog
	roots       map[string]sourcecatalog.Root
	facts       map[string]*sourcecatalog.TreeOverlay
	pendingRoot string
	pending     []sourcecatalog.OverlayNode
	bytes       int
	closed      bool
}

func (v *View) ReviewBuilder(ctx context.Context) *ReviewBuilder {
	return &ReviewBuilder{ctx: ctx, project: v.scope.Project, catalog: v.catalog, roots: v.roots, facts: make(map[string]*sourcecatalog.TreeOverlay)}
}
func (b *ReviewBuilder) Add(address Address) error {
	if b.closed {
		return pagedview.ErrExpired
	}
	if !validAddress(address) || address.Path == "." || len(address.Path) > 65536 {
		return ErrAddress
	}
	root, ok := b.roots[address.Root]
	if !ok {
		return ErrUnknownRoot
	}
	if b.pendingRoot != address.Root {
		if err := b.flush(); err != nil {
			return err
		}
		b.pendingRoot = address.Root
	}
	if b.facts[address.Root] == nil {
		overlay, err := b.catalog.NewTreeOverlay(b.ctx, b.project, root)
		if err != nil {
			return err
		}
		b.facts[address.Root] = overlay
		b.pending = append(b.pending, sourcecatalog.OverlayNode{Path: ".", Parent: ".", Directory: true})
		b.bytes += 194
	}
	parts := strings.Split(address.Path, "/")
	parent := "."
	for i, segment := range parts {
		next := path.Join(parent, segment)
		size := 192 + len(next) + len(parent)
		if len(b.pending) == pagedview.MaxRows || b.bytes+size > pagedview.MaxFrameBytes {
			if err := b.flush(); err != nil {
				return err
			}
		}
		b.pending = append(b.pending, sourcecatalog.OverlayNode{Path: next, Parent: parent, Depth: i + 1, Directory: i < len(parts)-1})
		b.bytes += size
		parent = next
	}
	return nil
}
func (b *ReviewBuilder) flush() error {
	if len(b.pending) == 0 {
		return nil
	}
	if err := b.facts[b.pendingRoot].Add(b.ctx, b.pending); err != nil {
		return err
	}
	clear(b.pending)
	b.pending = b.pending[:0]
	b.bytes = 0
	return nil
}
func (b *ReviewBuilder) Finish() (*ReviewSet, error) {
	if b.closed {
		return nil, pagedview.ErrExpired
	}
	if err := b.flush(); err != nil {
		return nil, err
	}
	var fingerprint pagedview.Fingerprint
	for root, overlay := range b.facts {
		facts, err := overlay.FactFingerprint(b.ctx)
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(struct {
			Root  string
			Facts pagedview.Fingerprint
		}{root, facts})
		if err != nil {
			return nil, err
		}
		fingerprint = fingerprint.Combine(pagedview.FingerprintOf(encoded))
	}
	b.closed = true
	facts := b.facts
	b.facts = nil
	if len(facts) == 0 {
		return nil, nil
	}
	return newReviewSet(facts, fingerprint), nil
}
func (b *ReviewBuilder) Close() {
	if b.closed {
		return
	}
	b.closed = true
	for _, overlay := range b.facts {
		disposeReviewOverlay(overlay)
	}
	b.facts = nil
	clear(b.pending)
	b.pending = nil
}
