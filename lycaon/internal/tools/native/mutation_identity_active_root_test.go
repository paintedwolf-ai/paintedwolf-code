package native

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/pkg/api"
)

type recordingMutations struct {
	mutations []tools.PrimaryMutation
}

func (r *recordingMutations) RecordPrimaryMutation(_ context.Context, sessionID, rootID, relPath string) {
	r.mutations = append(r.mutations, tools.PrimaryMutation{RootID: rootID, RelPath: relPath})
}

// A write in a non-primary session folder records the mutation and the file
// edit under the root the call resolved, so rewind keys and Den metadata
// never re-resolve display strings.
func TestWriteToolRecordsMutationIdentityAgainstTheActiveRoot(t *testing.T) {
	base := t.TempDir()
	primary, secondary := filepath.Join(base, "app"), filepath.Join(base, "den")
	roots := []projectroot.RootRef{
		{ID: "primary", Label: "app", Path: primary, IsPrimary: true},
		{ID: "active", Label: "den", Path: secondary},
	}
	for _, dir := range []string{primary, secondary} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	recorder := &recordingMutations{}
	tctx := tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "sess", Agent: "implement"},
		Source: tools.InvocationSource{Roots: roots, ActiveRootID: "active",
			SourceWorkspaceKind: api.SourceWorkspaceKindProject,
			RepoFileCount:       100, RepoFileCountKnown: true,
			MutationRecorder: recorder},
		Effects: tools.InvocationEffects{Out: &tools.ToolInvocationOut{}},
	}

	tool := &WriteTool{Boundary: testfixture.Boundary(t)}
	_, err := tool.Apply(context.Background(), WriteInput{Path: "notes.txt", Content: "hi"}, tctx)
	if err != nil {
		t.Fatalf("write: %v", err)
	}

	// assertProfileWriteScope resolves (and records) before the write resolves
	// again; that double record predates WR08, so assert identity, not count.
	if len(recorder.mutations) == 0 {
		t.Fatal("write recorded no primary mutation")
	}
	for _, got := range recorder.mutations {
		if got.RootID != "active" || got.RelPath != "notes.txt" {
			t.Fatalf("rewind key = %+v, want the active root identity", got)
		}
	}
	edit := tctx.Effects.Out.FileEdit
	if edit == nil {
		t.Fatal("write lost its file edit capture")
	}
	if edit.RootID != "active" || edit.Path != "notes.txt" {
		t.Fatalf("file edit = %+v, want the active root identity", edit)
	}
}
