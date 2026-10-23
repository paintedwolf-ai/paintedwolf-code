package command

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

// documentNotice is one held-draft notice, shared by initial and background delivery.
type documentNotice struct {
	path, text string
	delivered  bool
}

type programDocumentsKey struct{}

// programDocuments coalesces observed/protected unsaved paths outside command bytes.
type programDocuments struct {
	tc        tools.ToolContext
	memory    *toolcontract.ProgramMemory
	releases  []func()
	mu        sync.Mutex
	notes     map[string]*documentNotice
	transfers int
	closed    bool
}

func newProgramDocuments(ctx context.Context, tc tools.ToolContext, memory *toolcontract.ProgramMemory) (*programDocuments, error) {
	// Joining notices copies at most the existing bounded stderr window. Notice
	// text and JSON growth are admitted per entry before any producer reads it.
	release, err := memory.ReserveProgramState(ctx, programOutputBytes)
	if err != nil {
		return nil, commandDocumentBudgetError(err)
	}
	return &programDocuments{tc: tc, memory: memory, releases: []func(){release}}, nil
}

func (d *programDocuments) read(ctx context.Context, root, path string) error {
	documents, ok := tools.ProjectDocuments(d.tc)
	if !ok || root == "" {
		return nil
	}
	branch, err := d.tc.SourceBranch(root)
	if err != nil {
		return err
	}
	return documents.VisitUnsavedDocuments(ctx, d.tc.Identity.ProjectID, branch, root, path, false, func(state tools.EditorDocumentState) error {
		return d.note(ctx, state.RootID, state.Path)
	})
}

func (d *programDocuments) note(ctx context.Context, root, path string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	key := root + "/" + path
	if _, exists := d.notes[key]; exists {
		return nil
	}
	display := path
	for _, attached := range d.tc.Source.Roots {
		if attached.ID == root {
			display = filepath.Join(attached.Path, filepath.FromSlash(path))
			break
		}
	}
	release, err := d.memory.ReserveProgramState(ctx, 2048+int64(4*len(display)+len(key)))
	if err != nil {
		return commandDocumentBudgetError(err)
	}
	note, err := guidance.RenderCatalog(ctx, guidance.SurveyCommandUnsavedDraftRef, map[string]any{"path": display})
	if err != nil {
		release()
		return err
	}
	// Keep the path-list projection and both rendered/JSON result copies admitted
	// through initial marshaling or the eventual completion consumer.
	projectionRelease, err := d.memory.ReserveProgramState(ctx, 256+int64(len(display)+7*(len(note)+1)))
	if err != nil {
		release()
		return commandDocumentBudgetError(err)
	}
	if d.notes == nil {
		d.notes = map[string]*documentNotice{}
	}
	d.releases = append(d.releases, release, projectionRelease)
	d.notes[key] = &documentNotice{path: display, text: note}
	return nil
}

func (d *programDocuments) close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closed = true
	d.releaseIfIdle()
}

func (d *programDocuments) releaseTransfer() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.transfers--
	d.releaseIfIdle()
}

func (d *programDocuments) releaseIfIdle() {
	if !d.closed || d.transfers != 0 {
		return
	}
	for _, release := range d.releases {
		release()
	}
	d.releases = nil
	d.notes = nil
}

// commandDraftPaths supplies exact read-only exclusions for arbitrary external
// processes; script arguments cannot describe their eventual write set.
func commandDraftPaths(ctx context.Context, tc tools.ToolContext, memory *toolcontract.ProgramMemory) ([]string, func(), error) {
	var releases []func()
	release := func() {
		for _, done := range releases {
			done()
		}
	}
	documents, ok := tools.ProjectDocuments(tc)
	if !ok {
		return nil, release, nil
	}
	var paths []string
	seen := map[string]bool{}
	for _, root := range tc.Source.Roots {
		branch, err := tc.SourceBranch(root.ID)
		if err != nil {
			release()
			return nil, nil, err
		}
		err = documents.VisitUnsavedDocuments(ctx, tc.Identity.ProjectID, branch, root.ID, "", true, func(doc tools.EditorDocumentState) error {
			path := filepath.Join(root.Path, filepath.FromSlash(doc.Path))
			if seen[path] {
				return nil
			}
			if memory == nil {
				return fmt.Errorf("command document protection requires program memory")
			}
			done, err := memory.ReserveProgramState(ctx, 1024+int64(4*len(path)))
			if err != nil {
				return commandDocumentBudgetError(err)
			}
			releases = append(releases, done)
			if err := tools.ObserveCommandFileRead(ctx, root.ID, doc.Path); err != nil {
				return err
			}
			paths = append(paths, path)
			seen[path] = true
			return nil
		})
		if err != nil {
			release()
			return nil, nil, err
		}
	}
	return paths, release, nil
}

func commandProtectionReject(path, reason string) *toolrejection.ToolReject {
	return &toolrejection.ToolReject{Code: "COMMAND_REVISION_CONFLICT", Data: map[string]any{"path": path, "write_protection_unavailable": true, "write_protection_has_aliases": reason == "hard_link_aliases"}}
}

func deniedCommandDraft(refusals confine.SandboxRefusals, drafts []string) string {
	for _, denied := range refusals.Paths(confine.AccessWrite) {
		if denied.Layer != confine.FloorWriteConfigured {
			continue
		}
		for _, draft := range drafts {
			if confine.PathAtOrUnder(denied.Path, draft) || confine.PathAtOrUnder(draft, denied.Path) {
				return draft
			}
		}
	}
	return ""
}

func commandDocumentBudgetError(err error) error {
	var limit *toolcontract.ProgramMemoryLimitError
	if errors.As(err, &limit) {
		return toolrejection.RejectInvalidArguments("COMMAND_LIMIT_EXCEEDED", map[string]any{"limit_id": "program_state_bytes", "resource_unit": "bytes", "maximum": limit.Maximum, "observed": limit.Observed})
	}
	return err
}
