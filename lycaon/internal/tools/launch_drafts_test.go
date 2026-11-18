package tools

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/pkg/api"
)

// launchDocuments is the document owner a launch save reaches: it answers
// with the drafts it was told it cannot save and records each call.
type launchDocuments struct {
	EditorDocuments
	left   []EditorUnsavedDocument
	err    error
	calls  []EditorAgentCall
	branch []sourcebranch.ID
}

func (d *launchDocuments) SaveForAgentLaunch(_ context.Context, _ string, branch sourcebranch.ID, call EditorAgentCall) ([]EditorUnsavedDocument, error) {
	d.calls = append(d.calls, call)
	d.branch = append(d.branch, branch)
	return d.left, d.err
}

func launchContext(documents EditorDocuments) ToolContext {
	var tc ToolContext
	tc.Identity = InvocationIdentity{ProjectID: "p1", SessionID: "chat-1", ToolCallID: "call-9", UserTurn: 4}
	tc.Invocation.ToolName = "terminal_send"
	tc.Source.SourceWorkspaceKind = api.SourceWorkspaceKindProject
	tc.Source.ProjectSourceBranch = sourcebranch.Trunk
	tc.Source.ActiveRootID = "r1"
	tc.Source.Roots = []projectroot.RootRef{{ID: "r1", Path: "/work/app", Label: "app"}, {ID: "r2", Path: "/work/lib", Label: "lib"}}
	tc.Source.EditorDocuments = documents
	return tc
}

// Every context that has no shared documents launches without asking for a
// save: a worker's private branch, a non-project workspace, and a call with no
// project or no document owner.
func TestSaveLaunchDraftsAsksOnlyWhereDocumentsAreShared(t *testing.T) {
	for name, strip := range map[string]func(*ToolContext){
		"worker branch":         func(tc *ToolContext) { tc.Source.WorkerBranchRoot = "/branches/job-1" },
		"worker workspace":      func(tc *ToolContext) { tc.Source.SourceWorkspaceKind = api.SourceWorkspaceKindWorker },
		"no project":            func(tc *ToolContext) { tc.Identity.ProjectID = " " },
		"no document authority": func(tc *ToolContext) { tc.Source.EditorDocuments = nil },
	} {
		t.Run(name, func(t *testing.T) {
			documents := &launchDocuments{left: []EditorUnsavedDocument{{RootID: "r1", Path: "a.go", Cause: toolcontract.UnsavedNotWritten}}}
			tc := launchContext(documents)
			strip(&tc)
			drafts := NewProgramDrafts("command")
			got, err := SaveLaunchDrafts(WithProgramDrafts(t.Context(), drafts), tc)
			testutil.FailErr(t, "launch save", err)
			if len(documents.calls) != 0 || !reflect.DeepEqual(got, toolcontract.LaunchDrafts{}) || !reflect.DeepEqual(drafts.Unsaved(), toolcontract.LaunchDrafts{}) {
				t.Fatalf("calls=%d result=%+v program=%+v", len(documents.calls), got, drafts.Unsaved())
			}
		})
	}
}

// The save is attributed to the call that launches: inside a program to the
// program's tool, whose children carry operation names, and otherwise to the
// invoked tool.
func TestSaveLaunchDraftsNamesTheLaunchingCall(t *testing.T) {
	for _, c := range []struct {
		name    string
		program string
		want    string
	}{
		{"direct tool", "", "terminal_send"},
		{"command program", "command", "command"},
		{"verify program", "verify", "verify"},
	} {
		t.Run(c.name, func(t *testing.T) {
			documents := &launchDocuments{}
			ctx := t.Context()
			if c.program != "" {
				ctx = WithProgramDrafts(ctx, NewProgramDrafts(c.program))
			}
			_, err := SaveLaunchDrafts(ctx, launchContext(documents))
			testutil.FailErr(t, "launch save", err)
			want := EditorAgentCall{SessionID: "chat-1", Turn: 4, ToolCallID: "call-9", ToolName: c.want}
			if len(documents.calls) != 1 || documents.calls[0] != want || documents.branch[0] != sourcebranch.Trunk {
				t.Fatalf("calls=%+v branch=%v, want %+v on the project branch", documents.calls, documents.branch, want)
			}
			if got := DraftSaveCall(ctx, launchContext(documents)); got != want {
				t.Fatalf("a mutation's save names %+v, want %+v", got, want)
			}
		})
	}
}

// A result names unsaved drafts the way the model addresses files, in path
// order, and never more than the bound: the rest are counted.
func TestSaveLaunchDraftsBoundsAndAddressesTheUnsavedList(t *testing.T) {
	for _, total := range []int{0, 1, toolcontract.MaxUnsavedDocuments - 1, toolcontract.MaxUnsavedDocuments, toolcontract.MaxUnsavedDocuments + 1, 4 * toolcontract.MaxUnsavedDocuments} {
		t.Run(fmt.Sprint(total), func(t *testing.T) {
			documents := &launchDocuments{}
			var want []toolcontract.UnsavedDocument
			// The owner answers in no particular order, across both roots.
			for i := total - 1; i >= 0; i-- {
				root, cause := "r1", toolcontract.UnsavedChangedOutside
				display := fmt.Sprintf("pkg/f%03d.go", i)
				if i%3 == 0 {
					root, cause, display = "r2", toolcontract.UnsavedNotWritten, fmt.Sprintf("@lib/pkg/f%03d.go", i)
				}
				documents.left = append(documents.left, EditorUnsavedDocument{RootID: root, Path: fmt.Sprintf("pkg/f%03d.go", i), Cause: cause})
				want = append(want, toolcontract.UnsavedDocument{Path: display, Cause: cause})
			}
			sort.Slice(want, func(i, j int) bool { return want[i].Path < want[j].Path })
			got, err := SaveLaunchDrafts(t.Context(), launchContext(documents))
			testutil.FailErr(t, "launch save", err)
			kept := min(total, toolcontract.MaxUnsavedDocuments)
			if len(got.UnsavedDocuments) != kept || got.OmittedUnsavedDocuments != total-kept {
				t.Fatalf("named %d and omitted %d of %d", len(got.UnsavedDocuments), got.OmittedUnsavedDocuments, total)
			}
			if kept > 0 && !reflect.DeepEqual(got.UnsavedDocuments, want[:kept]) {
				t.Fatalf("unsaved = %+v, want %+v", got.UnsavedDocuments, want[:kept])
			}
		})
	}
}

// A program's result states every draft any of its launches left unsaved,
// once each with the latest cause, and each launch drops the program's draft
// inventory so the next native read asks again.
func TestProgramDraftsAccumulateAcrossLaunches(t *testing.T) {
	documents := &launchDocuments{}
	drafts := NewProgramDrafts("command")
	ctx := WithProgramDrafts(t.Context(), drafts)
	tc := launchContext(documents)
	loads := 0
	held := func(paths ...string) func() (map[string]bool, error) {
		return func() (map[string]bool, error) {
			loads++
			set := map[string]bool{}
			for _, path := range paths {
				set[path] = true
			}
			return set, nil
		}
	}
	holds := func(path string, load func() (map[string]bool, error)) bool {
		t.Helper()
		ok, err := drafts.Holds("r1", path, load)
		testutil.FailErr(t, "holds "+path, err)
		return ok
	}
	if !holds("a.go", held("a.go")) || holds("b.go", held("b.go")) || loads != 1 {
		t.Fatalf("the inventory is loaded once per root: loads=%d", loads)
	}

	launches := [][]EditorUnsavedDocument{
		{{RootID: "r1", Path: "a.go", Cause: toolcontract.UnsavedNotWritten}},
		{},
		{{RootID: "r1", Path: "a.go", Cause: toolcontract.UnsavedChangedOutside}, {RootID: "r2", Path: "b.go", Cause: toolcontract.UnsavedAgentEditHeld}},
	}
	for i, left := range launches {
		documents.left = left
		_, err := SaveLaunchDrafts(ctx, tc)
		testutil.FailErr(t, "launch save", err)
		before := loads
		if !holds("b.go", held("b.go")) || loads != before+1 {
			t.Fatalf("launch %d kept a stale draft inventory: loads %d -> %d", i, before, loads)
		}
	}
	want := toolcontract.LaunchDrafts{UnsavedDocuments: []toolcontract.UnsavedDocument{
		{Path: "@lib/b.go", Cause: toolcontract.UnsavedAgentEditHeld},
		{Path: "a.go", Cause: toolcontract.UnsavedChangedOutside},
	}}
	if got := drafts.Unsaved(); !reflect.DeepEqual(got, want) {
		t.Fatalf("program unsaved = %+v, want %+v", got, want)
	}
	if got := (*ProgramDrafts)(nil).Unsaved(); !reflect.DeepEqual(got, toolcontract.LaunchDrafts{}) {
		t.Fatalf("a result with no program states %+v", got)
	}
}

// A save that could not run fails the launch and states nothing as unsaved.
func TestSaveLaunchDraftsFailsWhenTheSaveCannotRun(t *testing.T) {
	failure := errors.New("document store unavailable")
	documents := &launchDocuments{err: failure, left: []EditorUnsavedDocument{{RootID: "r1", Path: "a.go", Cause: toolcontract.UnsavedNotWritten}}}
	drafts := NewProgramDrafts("command")
	got, err := SaveLaunchDrafts(WithProgramDrafts(t.Context(), drafts), launchContext(documents))
	if !errors.Is(err, failure) || !reflect.DeepEqual(got, toolcontract.LaunchDrafts{}) || !reflect.DeepEqual(drafts.Unsaved(), toolcontract.LaunchDrafts{}) {
		t.Fatalf("err=%v result=%+v program=%+v", err, got, drafts.Unsaved())
	}
}
