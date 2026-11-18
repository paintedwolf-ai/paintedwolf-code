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

// Pin marks a document revision a native operation captured, so a launch
// leaves that draft to the operation. A source capture releases it when its
// bytes are done with; a destination's pin lapses when its write moves the
// document on.
func (d *ProgramDrafts) Pin(documentID string, revision int64) (release func()) {
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
