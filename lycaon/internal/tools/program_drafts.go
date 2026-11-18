package tools

import (
	"context"
	"sync"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

type programDraftsKey struct{}

// ProgramDrafts is one program's view of the person's unsaved documents:
// which paths held a draft when a root was first read, which revisions its
// native operations have pinned, and which drafts its launches could not save.
type ProgramDrafts struct {
	tool string

	mu      sync.Mutex
	roots   map[string]map[string]bool
	pinned  map[EditorPinnedRevision]int
	unsaved map[string]toolcontract.UnsavedDocument
}

// NewProgramDrafts starts the view for a program run by tool.
func NewProgramDrafts(tool string) *ProgramDrafts {
	return &ProgramDrafts{tool: tool, roots: map[string]map[string]bool{}, pinned: map[EditorPinnedRevision]int{}, unsaved: map[string]toolcontract.UnsavedDocument{}}
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

type draftPinsKey struct{}

// draftPins collects the pins one program operation takes.
type draftPins struct {
	mu       sync.Mutex
	releases []func()
}

// WithDraftPins scopes the document pins of one program operation: release
// drops them once the operation has settled and verified what it captured.
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

// PinDraft marks a document revision a native operation captured, so a launch
// in the same program leaves that draft to the operation. The pin lasts until
// the operation's scope releases it; outside one, a destination captured for a
// redirect, it lapses when the write moves the document on.
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
	pin := EditorPinnedRevision{DocumentID: documentID, Revision: revision}
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

func (d *ProgramDrafts) pinnedRevisions() []EditorPinnedRevision {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	pins := make([]EditorPinnedRevision, 0, len(d.pinned))
	for pin := range d.pinned {
		pins = append(pins, pin)
	}
	return pins
}

// launchSaved records a launch save: every root's inventory is stale, and the
// drafts it left unsaved join the program's result.
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
