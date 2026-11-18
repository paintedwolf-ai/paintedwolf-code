package editordoc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Separate directories isolate unwritable fixtures.
type launchDraftState struct {
	name      string
	arrange   func(t *testing.T, f agentFixture, path string) *Document
	cause     UnsavedCause
	publishes bool
	// release lets a later launch retry a blocked save.
	release func(t *testing.T, f agentFixture, path string)
}

func (f agentFixture) holdDirectory(t *testing.T, path string) {
	t.Helper()
	dir := filepath.Join(f.root, filepath.Dir(path))
	testutil.FailErr(t, "hold directory", os.Chmod(dir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
}

func (f agentFixture) releaseDirectory(t *testing.T, path string) {
	t.Helper()
	testutil.FailErr(t, "release directory", os.Chmod(filepath.Join(f.root, filepath.Dir(path)), 0o755))
}

func (f agentFixture) heldAgentEdit(t *testing.T, d *Document, path, content string) *Document {
	t.Helper()
	f.holdDirectory(t, path)
	result, err := f.service.ApplyAgentEdit(t.Context(), agentEdit(d, content))
	testutil.FailErr(t, "accept agent edit", err)
	if result.Saved || result.PublicationError == nil || result.HeldVersionID == "" {
		t.Fatalf("agent edit was not held: %+v", result)
	}
	f.releaseDirectory(t, path)
	return result.Document
}

var launchDraftStates = []launchDraftState{
	{name: "clean", arrange: func(t *testing.T, f agentFixture, path string) *Document {
		return f.open(t, path)
	}},
	{name: "person", publishes: true, arrange: func(t *testing.T, f agentFixture, path string) *Document {
		return f.typeDraft(t, f.open(t, path), "typed by the person\n")
	}},
	{name: "person-crlf", publishes: true, arrange: func(t *testing.T, f agentFixture, path string) *Document {
		f.write(t, path, "saved\r\nsecond\r\n")
		d := f.open(t, path)
		typed, err := f.service.ReplaceSnapshot(t.Context(), d.ID, f.project.ID, SnapshotReplacement{
			DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: d.Revision}, Content: "typed\nover crlf\n", EOL: "crlf"})
		testutil.FailErr(t, "type over crlf", err)
		return typed
	}},
	{name: "changed-outside", cause: UnsavedChangedOutside, arrange: func(t *testing.T, f agentFixture, path string) *Document {
		d := f.typeDraft(t, f.open(t, path), "typed before an outside write\n")
		f.write(t, path, "written outside\n")
		return d
	}},
	{name: "observed-diverged", cause: UnsavedChangedOutside, arrange: func(t *testing.T, f agentFixture, path string) *Document {
		d := f.typeDraft(t, f.open(t, path), "typed before divergence\n")
		f.write(t, path, "outside\xff\n")
		observed, err := f.service.ObserveDisk(t.Context(), f.project, d.ID, "window")
		testutil.FailErr(t, "observe disk", err)
		if !observed.Diverged {
			t.Fatal("fixture did not diverge")
		}
		return observed
	}},
	{name: "not-written", cause: UnsavedNotWritten,
		arrange: func(t *testing.T, f agentFixture, path string) *Document {
			d := f.typeDraft(t, f.open(t, path), "typed in a held directory\n")
			f.holdDirectory(t, path)
			return d
		},
		release: func(t *testing.T, f agentFixture, path string) { f.releaseDirectory(t, path) },
	},
	{name: "agent-edit-held", cause: UnsavedAgentEditHeld, arrange: func(t *testing.T, f agentFixture, path string) *Document {
		return f.heldAgentEdit(t, f.open(t, path), path, "agent text awaiting review\n")
	}},
	{name: "person-over-agent-edit-held", cause: UnsavedAgentEditHeld, arrange: func(t *testing.T, f agentFixture, path string) *Document {
		held := f.heldAgentEdit(t, f.open(t, path), path, "agent text awaiting review\n")
		return f.typeDraft(t, held, "agent text awaiting review\nand the person's line\n")
	}},
}

type launchObservation struct {
	disk     string
	draft    string
	dirty    bool
	revision int64
}

func (f agentFixture) observeLaunchDraft(t *testing.T, id, path string) launchObservation {
	t.Helper()
	d, err := f.store.Get(t.Context(), id)
	testutil.FailErr(t, "read "+path, err)
	return launchObservation{disk: f.disk(t, path), draft: d.Draft, dirty: d.Dirty, revision: d.Revision}
}

func unsavedByPath(unsaved []UnsavedDocument) map[string]UnsavedCause {
	out := make(map[string]UnsavedCause, len(save.Unsaved))
	for _, document := range unsaved {
		out[document.Path] = document.Cause
	}
	return out
}

func launchCall(tool string) AgentCall {
	return AgentCall{SessionID: "chat-launch", Turn: 5, ToolCallID: "call-launch-" + tool, ToolName: tool}
}

func (f agentFixture) launchOperations(t *testing.T, call AgentCall) int {
	t.Helper()
	var n int
	testutil.FailErr(t, "count launch operations", f.store.db.QueryRowContext(t.Context(),
		`SELECT COUNT(1) FROM source_operations WHERE tool_call_id=?`, call.ToolCallID).Scan(&n))
	return n
}

func TestSaveForAgentLaunchHoldsForEveryDraftState(t *testing.T) {
	for _, tool := range []string{"command", "verify"} {
		t.Run(tool, func(t *testing.T) {
			f, _ := newLedgerAgentFixture(t, nil)
			call := launchCall(tool)
			documents := map[string]*Document{}
			for _, state := range launchDraftStates {
				path := state.name + "/f.txt"
				testutil.FailErr(t, "make "+state.name, os.Mkdir(filepath.Join(f.root, state.name), 0o755))
				f.write(t, path, "saved\n")
				documents[path] = state.arrange(t, f, path)
			}
			before := map[string]launchObservation{}
			for path, d := range documents {
				before[path] = f.observeLaunchDraft(t, d.ID, path)
			}

			save, err := f.service.SaveForAgentLaunch(t.Context(), f.project.ID, f.project.SourceBranch, call, nil)
			testutil.FailErr(t, "launch save", err)

			got, want, published := unsavedByPath(save.Unsaved), map[string]UnsavedCause{}, 0
			for _, state := range launchDraftStates {
				path := state.name + "/f.txt"
				was, now := before[path], f.observeLaunchDraft(t, documents[path].ID, path)
				if now.draft != was.draft {
					t.Errorf("%s: the save changed the document's text: %q -> %q", state.name, was.draft, now.draft)
				}
				switch {
				case state.publishes:
					published++
					if now.dirty {
						t.Errorf("%s: still unsaved after the launch save", state.name)
					}
					if d := documents[path]; diskText(t, d, now.disk) != was.draft {
						t.Errorf("%s: disk = %q, want the draft %q", state.name, now.disk, was.draft)
					}
				case state.cause != "":
					want[path] = state.cause
					if now.disk != was.disk || !now.dirty {
						t.Errorf("%s: an unsaved draft was touched: disk %q -> %q, dirty=%v", state.name, was.disk, now.disk, now.dirty)
					}
				default:
					if now != was {
						t.Errorf("%s: a clean document changed: %+v -> %+v", state.name, was, now)
					}
				}
			}
			if save.Published != (published > 0) {
				t.Fatalf("published = %v, completed saves = %d", save.Published, published)
			}
			if !equalCauses(got, want) {
				t.Fatalf("unsaved = %v, want %v", got, want)
			}
			if n := f.launchOperations(t, call); n != published {
				t.Fatalf("ledger operations for the call = %d, want one per published draft (%d)", n, published)
			}
			f.assertLaunchAuthorship(t, call)

			settled := map[string]launchObservation{}
			for path, d := range documents {
				settled[path] = f.observeLaunchDraft(t, d.ID, path)
			}
			again, err := f.service.SaveForAgentLaunch(t.Context(), f.project.ID, f.project.SourceBranch, call, nil)
			testutil.FailErr(t, "second launch save", err)
			if again.Published || !equalCauses(unsavedByPath(again.Unsaved), want) {
				t.Fatalf("second launch unsaved = %v, want %v", unsavedByPath(again.Unsaved), want)
			}
			for path, d := range documents {
				if now := f.observeLaunchDraft(t, d.ID, path); now.disk != settled[path].disk || now.draft != settled[path].draft || now.dirty != settled[path].dirty {
					t.Errorf("%s: a second launch changed it: %+v -> %+v", path, settled[path], now)
				}
			}
			if n := f.launchOperations(t, call); n != published {
				t.Fatalf("a second launch recorded operations: %d, want %d", n, published)
			}

			for _, state := range launchDraftStates {
				if state.release == nil {
					continue
				}
				path := state.name + "/f.txt"
				state.release(t, f, path)
				delete(want, path)
				released, err := f.service.SaveForAgentLaunch(t.Context(), f.project.ID, f.project.SourceBranch, call, nil)
				testutil.FailErr(t, "launch save after release", err)
				if !equalCauses(unsavedByPath(released.Unsaved), want) {
					t.Fatalf("after releasing %s unsaved = %v, want %v", state.name, unsavedByPath(released.Unsaved), want)
				}
				if now := f.observeLaunchDraft(t, documents[path].ID, path); now.dirty || diskText(t, documents[path], now.disk) != before[path].draft {
					t.Errorf("%s: not saved once released: %+v", state.name, now)
				}
			}
		})
	}
}

func diskText(t *testing.T, d *Document, disk string) string {
	t.Helper()
	if d.EOL != "crlf" {
		return disk
	}
	if strings.Count(disk, "\n") != strings.Count(disk, "\r\n") {
		t.Errorf("a crlf document was saved with bare line feeds: %q", disk)
	}
	return strings.ReplaceAll(disk, "\r\n", "\n")
}

func equalCauses(got, want map[string]UnsavedCause) bool {
	if len(got) != len(want) {
		return false
	}
	for path, cause := range want {
		if got[path] != cause {
			return false
		}
	}
	return true
}

func (f agentFixture) assertLaunchAuthorship(t *testing.T, call AgentCall) {
	t.Helper()
	var foreign int
	testutil.FailErr(t, "operation attribution", f.store.db.QueryRowContext(t.Context(),
		`SELECT COUNT(1) FROM source_operations WHERE tool_call_id=? AND NOT (origin='agent' AND tool_name=? AND session_id=? AND turn=?)`,
		call.ToolCallID, call.ToolName, call.SessionID, call.Turn).Scan(&foreign))
	if foreign != 0 {
		t.Errorf("%d launch operations do not name the agent's call and tool %q", foreign, call.ToolName)
	}
	var agentText, personText int
	testutil.FailErr(t, "published contributions", f.store.db.QueryRowContext(t.Context(),
		`SELECT COALESCE(SUM(c.origin='agent'),0), COALESCE(SUM(c.origin!='agent'),0)
 FROM source_effect_contributions ec
 JOIN source_text_contributions c ON c.id=ec.contribution_id
 JOIN source_effects e ON e.id=ec.effect_id
 JOIN source_operations o ON o.id=e.operation_id
 WHERE o.tool_call_id=?`, call.ToolCallID).Scan(&agentText, &personText))
	if agentText != 0 || personText == 0 {
		t.Errorf("a launch save published agent text or lost the person's authorship: agent=%d person=%d", agentText, personText)
	}
}

func TestSaveForAgentLaunchTreatsEachDraftStateAlone(t *testing.T) {
	for _, state := range launchDraftStates {
		t.Run(state.name, func(t *testing.T) {
			f, _ := newLedgerAgentFixture(t, nil)
			path := state.name + "/f.txt"
			testutil.FailErr(t, "make directory", os.Mkdir(filepath.Join(f.root, state.name), 0o755))
			f.write(t, path, "saved\n")
			d := state.arrange(t, f, path)
			was := f.observeLaunchDraft(t, d.ID, path)
			save, err := f.service.SaveForAgentLaunch(t.Context(), f.project.ID, f.project.SourceBranch, launchCall("command"), nil)
			testutil.FailErr(t, "launch save", err)
			want := map[string]UnsavedCause{}
			if state.cause != "" {
				want[path] = state.cause
			}
			if save.Published != state.publishes || !equalCauses(unsavedByPath(save.Unsaved), want) {
				t.Fatalf("unsaved = %v, want %v", unsavedByPath(save.Unsaved), want)
			}
			if now := f.observeLaunchDraft(t, d.ID, path); now.dirty != (state.cause != "") || (state.publishes && diskText(t, d, now.disk) != was.draft) {
				t.Fatalf("after the launch save: %+v (was %+v)", now, was)
			}
		})
	}
}

func TestSaveForAgentLaunchFollowsLaterTyping(t *testing.T) {
	f, _ := newLedgerAgentFixture(t, map[string]string{"a.txt": "saved\n"})
	d := f.open(t, "a.txt")
	for _, text := range []string{"first\n", "first\nsecond\n", "third replaces both\n"} {
		current, err := f.store.Get(t.Context(), d.ID)
		testutil.FailErr(t, "current document", err)
		f.typeDraft(t, current, text)
		if got := f.disk(t, "a.txt"); got == text {
			t.Fatalf("typing reached disk without a save: %q", got)
		}
		save, err := f.service.SaveForAgentLaunch(t.Context(), f.project.ID, f.project.SourceBranch, launchCall("command"), nil)
		testutil.FailErr(t, "launch save", err)
		if len(save.Unsaved) != 0 || f.disk(t, "a.txt") != text {
			t.Fatalf("after typing %q: unsaved=%v disk=%q", text, save.Unsaved, f.disk(t, "a.txt"))
		}
	}
}

func TestSaveForAgentLaunchReturnsACanceledContext(t *testing.T) {
	f, _ := newLedgerAgentFixture(t, map[string]string{"a.txt": "saved\n"})
	f.typeDraft(t, f.open(t, "a.txt"), "typed\n")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	save, err := f.service.SaveForAgentLaunch(ctx, f.project.ID, f.project.SourceBranch, launchCall("command"), nil)
	if !errors.Is(err, context.Canceled) || save.Unsaved != nil || save.Published {
		t.Fatalf("unsaved=%v err=%v", save.Unsaved, err)
	}
	if f.disk(t, "a.txt") != "saved\n" {
		t.Fatal("a canceled launch saved a draft")
	}
}

func TestSaveForAgentOperationKeepsItsRefusals(t *testing.T) {
	f, _ := newLedgerAgentFixture(t, nil)
	for _, state := range launchDraftStates {
		path := state.name + "/f.txt"
		testutil.FailErr(t, "make "+state.name, os.Mkdir(filepath.Join(f.root, state.name), 0o755))
		f.write(t, path, "saved\n")
		d := state.arrange(t, f, path)
		err := f.service.SaveForAgentOperation(t.Context(), f.project.ID, f.project.SourceBranch, d.RootID, path, false, autosaveCall)
		var conflict *ExecutionConflict
		var failed *PublicationFailure
		switch state.cause {
		case UnsavedChangedOutside:
			if err == nil {
				t.Errorf("%s: a mutation saved over a change made outside the document", state.name)
			}
			if state.name == "changed-outside" && (!errors.As(err, &conflict) || conflict.Path != path) {
				t.Errorf("%s: err = %v, want a conflict at the path", state.name, err)
			}
		case UnsavedNotWritten:
			if !errors.As(err, &failed) || failed.Path != path || failed.Err == nil {
				t.Errorf("%s: err = %v, want a publication failure at the path", state.name, err)
			}
		default:
			// The path mutation already has admission to publish the held edit.
			testutil.FailErr(t, state.name+": save for a mutation", err)
			if now := f.observeLaunchDraft(t, d.ID, path); now.dirty {
				t.Errorf("%s: a mutation left the draft unsaved", state.name)
			}
		}
	}
}

func TestSaveForAgentLaunchLeavesPinnedRevisionsAlone(t *testing.T) {
	f, _ := newLedgerAgentFixture(t, map[string]string{"pinned.txt": "saved\n", "free.txt": "saved\n", "stale-pin.txt": "saved\n"})
	pinned := f.typeDraft(t, f.open(t, "pinned.txt"), "captured by a native stage\n")
	free := f.typeDraft(t, f.open(t, "free.txt"), "typed and free\n")
	stale := f.typeDraft(t, f.open(t, "stale-pin.txt"), "typed\n")
	asked := map[string]bool{}
	pins := func(documentID string, revision int64) bool {
		asked[documentID] = true
		return documentID == pinned.ID && revision == pinned.Revision || documentID == stale.ID && revision == stale.Revision-1
	}
	for range 2 {
		save, err := f.service.SaveForAgentLaunch(t.Context(), f.project.ID, f.project.SourceBranch, launchCall("command"), pins)
		testutil.FailErr(t, "launch save", err)
		if len(save.Unsaved) != 0 {
			t.Fatalf("a pinned draft was reported unsaved: %+v", save.Unsaved)
		}
		if now := f.observeLaunchDraft(t, pinned.ID, "pinned.txt"); now.disk != "saved\n" || !now.dirty || now.revision != pinned.Revision {
			t.Fatalf("the launch moved a pinned document: %+v", now)
		}
		for name, d := range map[string]*Document{"free.txt": free, "stale-pin.txt": stale} {
			if now := f.observeLaunchDraft(t, d.ID, name); now.dirty || now.disk != d.Draft {
				t.Fatalf("%s was not saved beside a pinned draft: %+v", name, now)
			}
		}
	}
	if !asked[pinned.ID] || !asked[free.ID] || !asked[stale.ID] {
		t.Fatalf("the pin rule was not asked about every draft: %v", asked)
	}
	moved := f.typeDraft(t, pinned, "captured by a native stage\nthen typed on\n")
	save, err := f.service.SaveForAgentLaunch(t.Context(), f.project.ID, f.project.SourceBranch, launchCall("command"), pins)
	testutil.FailErr(t, "launch save after the document moved", err)
	if now := f.observeLaunchDraft(t, moved.ID, "pinned.txt"); len(save.Unsaved) != 0 || now.dirty || now.disk != moved.Draft {
		t.Fatalf("a document past its pin was not saved: unsaved=%+v %+v", save.Unsaved, now)
	}
}
