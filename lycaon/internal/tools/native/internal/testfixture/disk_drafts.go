package testfixture

import (
	"context"
	"sort"
	"sync"

	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/tools"
)

// DiskDrafts publishes fixture drafts to a real tree.
type DiskDrafts struct {
	tools.EditorDocuments
	// Publish writes a saved draft to the tree.
	Publish func(path, text string) error
	// Blocked names, by path, why a draft cannot be saved.
	Blocked map[string]string
	// Err fails every launch save.
	Err error
	// OnSave observes each launch save as it begins.
	OnSave func()

	mu     sync.Mutex
	drafts map[string]string
	calls  []tools.EditorAgentCall
}

// Type holds text as the person's unsaved draft of path.
func (d *DiskDrafts) Type(path, text string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.drafts == nil {
		d.drafts = map[string]string{}
	}
	d.drafts[path] = text
}

// Calls returns the launch saves asked for so far.
func (d *DiskDrafts) Calls() []tools.EditorAgentCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]tools.EditorAgentCall(nil), d.calls...)
}

// ForgetCalls drops the recorded saves.
func (d *DiskDrafts) ForgetCalls() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = nil
}

func (d *DiskDrafts) SaveForAgentLaunch(_ context.Context, _ string, _ sourcebranch.ID, call tools.EditorAgentCall, _ func(string, int64) bool) (tools.EditorLaunchSave, error) {
	if d.OnSave != nil {
		d.OnSave()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, call)
	if d.Err != nil {
		return tools.EditorLaunchSave{}, d.Err
	}
	var save tools.EditorLaunchSave
	for _, path := range d.heldPaths() {
		if cause, held := d.Blocked[path]; held {
			save.Unsaved = append(save.Unsaved, tools.EditorUnsavedDocument{RootID: "r1", Path: path, Cause: cause})
			continue
		}
		if err := d.Publish(path, d.drafts[path]); err != nil {
			return save, err
		}
		delete(d.drafts, path)
		save.Published = true
	}
	return save, nil
}

// OpenDocument leaves reads on disk.
func (d *DiskDrafts) OpenDocument(context.Context, string, sourcebranch.ID, string, string) (tools.EditorDocumentText, bool, error) {
	return tools.EditorDocumentText{}, false, nil
}

func (d *DiskDrafts) VisitAgentPublications(context.Context, string, sourcebranch.ID, string, int, string, func(tools.EditorDocumentPublicationCandidate) error) error {
	return nil
}

// VisitUnsavedDocuments offers the drafts no save has published.
func (d *DiskDrafts) VisitUnsavedDocuments(_ context.Context, _ string, _ sourcebranch.ID, _, path string, descendants bool, visit func(tools.EditorDocumentState) error) error {
	d.mu.Lock()
	held := d.heldPaths()
	d.mu.Unlock()
	for _, candidate := range held {
		under := path == "" || candidate == path || (descendants && len(candidate) > len(path) && candidate[:len(path)+1] == path+"/")
		if !under {
			continue
		}
		if err := visit(tools.EditorDocumentState{ID: "doc-" + candidate, RootID: "r1", Path: candidate, Revision: 1}); err != nil {
			return err
		}
	}
	return nil
}

func (d *DiskDrafts) heldPaths() []string {
	paths := make([]string, 0, len(d.drafts))
	for path := range d.drafts {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}
