package tools

import (
	"context"
	"sync"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

type programDraftsKey struct{}

// ProgramDrafts tracks draft paths, pinned revisions, and launch remainders.
type ProgramDrafts struct {
	tool          string
	observeLaunch func(context.Context, bool)

	// A document lookup and its pin must precede or follow the launch save.
	saving sync.RWMutex

	mu      sync.Mutex
	roots   map[string]map[string]bool
	pinned  map[draftPin]int
	unsaved map[string]toolcontract.UnsavedDocument
}

// NewProgramDrafts optionally reports whether each launch published a draft.
func NewProgramDrafts(tool string, observeLaunch func(context.Context, bool)) *ProgramDrafts {
	return &ProgramDrafts{
		tool: tool, observeLaunch: observeLaunch, roots: map[string]map[string]bool{},
		pinned: map[draftPin]int{}, unsaved: map[string]toolcontract.UnsavedDocument{},
	}
}

// WithProgramDrafts binds a program's draft view to its evaluation.
func WithProgramDrafts(ctx context.Context, drafts *ProgramDrafts) context.Context {
	return context.WithValue(ctx, programDraftsKey{}, drafts)
}

// ProgramDraftsFrom returns the program's view; ok is false outside a program.
func ProgramDraftsFrom(ctx context.Context) (*ProgramDrafts, bool) {
	drafts, ok := ctx.Value(programDraftsKey{}).(*ProgramDrafts)
	return drafts, ok && drafts != nil
}

// Holds reports whether path in root held unsaved text, loading the root's
// paths through load the first time the root is asked about.
func (d *ProgramDrafts) Holds(rootID, path string, load func() (map[string]bool, error)) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	paths, loaded := d.roots[rootID]
	if !loaded {
		var err error
		if paths, err = load(); err != nil {
			return false, err
		}
		d.roots[rootID] = paths
	}
	return paths[path], nil
}

// draftPin is a document revision an operation captured.
type draftPin struct {
	documentID string
	revision   int64
}

type draftPinsKey struct{}

// draftPins collects the pins one program operation takes.
type draftPins struct {
	mu       sync.Mutex
	releases []func()
}

// WithDraftPins scopes pins to one program operation.
func WithDraftPins(ctx context.Context) (context.Context, func()) {
	pins := &draftPins{}
	return context.WithValue(ctx, draftPinsKey{}, pins), func() {
		pins.mu.Lock()
		releases := pins.releases
		pins.releases = nil
		pins.mu.Unlock()
		for _, release := range releases {
			release()
		}
	}
}

// PinDraft protects a captured revision from the program's launch saves.
// A redirect pin lasts until the revision changes or the program ends.
func PinDraft(ctx context.Context, documentID string, revision int64) {
	drafts, ok := ProgramDraftsFrom(ctx)
	if !ok {
		return
	}
	release := drafts.pin(documentID, revision)
	if pins, scoped := ctx.Value(draftPinsKey{}).(*draftPins); scoped {
		pins.mu.Lock()
		pins.releases = append(pins.releases, release)
		pins.mu.Unlock()
	}
}

func (d *ProgramDrafts) pin(documentID string, revision int64) (release func()) {
	pin := draftPin{documentID, revision}
	d.mu.Lock()
	d.pinned[pin]++
	d.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			d.mu.Lock()
			defer d.mu.Unlock()
			if d.pinned[pin]--; d.pinned[pin] <= 0 {
				delete(d.pinned, pin)
			}
		})
	}
}

// ReadingDrafts excludes launch saves during a program's document lookup.
func ReadingDrafts(ctx context.Context) (done func()) {
	drafts, ok := ProgramDraftsFrom(ctx)
	if !ok {
		return func() {}
	}
	drafts.saving.RLock()
	return drafts.saving.RUnlock
}

// Pinned reports whether the program holds this revision.
func (d *ProgramDrafts) Pinned(documentID string, revision int64) bool {
	if d == nil {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.pinned[draftPin{documentID, revision}] > 0
}

func (d *ProgramDrafts) launchSaved(unsaved []toolcontract.UnsavedDocument) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.roots = map[string]map[string]bool{}
	for _, document := range unsaved {
		d.unsaved[document.Path] = document
	}
}

// Unsaved lists the drafts this program's launches could not save.
func (d *ProgramDrafts) Unsaved() toolcontract.LaunchDrafts {
	if d == nil {
		return toolcontract.LaunchDrafts{}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	unsaved := make([]toolcontract.UnsavedDocument, 0, len(d.unsaved))
	for _, document := range d.unsaved {
		unsaved = append(unsaved, document)
	}
	return boundLaunchDrafts(unsaved)
}
