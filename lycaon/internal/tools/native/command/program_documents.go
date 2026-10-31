package command

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"sync"

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

// programDocuments coalesces observed unsaved paths outside command bytes.
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
	release, err := memory.ReserveProgramState(ctx, int64(CommandOutputBudget(tc.Host.ModelContextWindow)))
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

func commandDocumentBudgetError(err error) error {
	var limit *toolcontract.ProgramMemoryLimitError
	if errors.As(err, &limit) {
		return toolrejection.RejectInvalidArguments("COMMAND_LIMIT_EXCEEDED", map[string]any{"limit_id": "program_state_bytes", "resource_unit": "bytes", "maximum": limit.Maximum, "observed": limit.Observed})
	}
	return err
}

func (d *programDocuments) appendNotes(stderr string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	keys := make([]string, 0, len(d.notes))
	length := len(stderr)
	for key, note := range d.notes {
		if !note.delivered {
			keys = append(keys, key)
			length += len(note.text) + 1
		}
	}
	if len(keys) == 0 {
		return stderr
	}
	sort.Strings(keys)
	// The initial window and each notice's projection envelope remain held.
	var out strings.Builder
	out.Grow(length + 1)
	out.WriteString(stderr)
	if stderr != "" && stderr[len(stderr)-1] != '\n' {
		out.WriteByte('\n')
	}
	for _, key := range keys {
		note := d.notes[key]
		note.delivered = true
		out.WriteString(note.text)
		out.WriteByte('\n')
	}
	return out.String()
}

// takePaths retains the existing notice admission through completion consumption,
// even after the background job releases its last program hold.
func (d *programDocuments) takePaths() ([]string, func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	count := 0
	for _, note := range d.notes {
		if !note.delivered {
			count++
		}
	}
	if count == 0 {
		return nil, nil
	}
	paths := make([]string, 0, count)
	for _, note := range d.notes {
		if !note.delivered {
			paths = append(paths, note.path)
			note.delivered = true
		}
	}
	sort.Strings(paths)
	d.transfers++
	return paths, sync.OnceFunc(d.releaseTransfer)
}
