package transcript

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func programEffectsFixture(t *testing.T) (*Service, *store.Memory, string) {
	t.Helper()
	mem := store.NewMemory()
	sess, err := mem.Create(t.Context(), api.CreateSessionRequest{ProviderID: "fixture", Model: "large"}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	return New(mem, nil), mem, sess.ID
}

func commandRow(callID string, edits ...api.FileEditSnapshot) api.Message {
	return api.Message{ID: "row-" + callID, Role: api.MessageRoleTool, Content: "{}", ToolResult: &api.ToolResult{Tool: "command", ToolCallID: callID, FileEdits: edits}}
}

func textEdit(path, before, after string) api.FileEditSnapshot {
	return api.FileEditSnapshot{RootID: "r1", Path: path, Before: &before, After: after}
}

// storedEdits renders a stored row's edits as path:before>after.
func storedEdits(t *testing.T, mem *store.Memory, sessionID, callID string) (string, int) {
	t.Helper()
	msg, err := mem.GetMessage(t.Context(), sessionID, "row-"+callID)
	testutil.FailErr(t, "read stored row", err)
	var parts []string
	for _, edit := range msg.ToolResult.FileEdits {
		before := "-"
		if edit.Before != nil {
			before = *edit.Before
		}
		parts = append(parts, fmt.Sprintf("%s:%s>%s", edit.Path, before, edit.After))
	}
	return strings.Join(parts, " "), msg.ToolResult.FileEditsOmitted
}

func TestProgramEffectsJoinTheStoredResultOfTheirCall(t *testing.T) {
	svc, mem, sessionID := programEffectsFixture(t)
	row := commandRow("c1", textEdit("a.go", "1", "2"))
	row.ToolResult.CardFacts = []api.ActionFact{{Kind: api.ActionFactSourceObservation, SourceWindows: []string{"pending"}}}
	testutil.FailErr(t, "store result", svc.Append(t.Context(), sessionID, row))
	gap := api.CommandFolderCoverage{Folder: "/outside", Coverage: api.SourceWindowCoverageNotObserved, Reason: api.SourceWindowGapIoError}
	svc.AppendProgramEffects(t.Context(), sessionID, "c1", tools.ProgramEffects{
		FileEdits:   []api.FileEditSnapshot{textEdit("a.go", "2", "3"), {RootID: "r1", Path: "b.go", After: "new"}},
		Omitted:     2,
		Observation: &api.ActionFact{Kind: api.ActionFactSourceObservation, SourceCoverage: []api.CommandFolderCoverage{gap}},
	})
	if got, omitted := storedEdits(t, mem, sessionID, "c1"); got != "a.go:1>3 b.go:->new" || omitted != 2 {
		t.Fatalf("stored %q with %d omitted, want the composed edit, the new file and 2", got, omitted)
	}
	stored, err := mem.GetMessage(t.Context(), sessionID, row.ID)
	testutil.FailErr(t, "read command result", err)
	if facts := stored.ToolResult.CardFacts; len(facts) != 1 || len(facts[0].SourceWindows) != 0 ||
		len(facts[0].SourceCoverage) != 1 || facts[0].SourceCoverage[0].Folder != gap.Folder {
		t.Fatalf("settled result facts = %+v", facts)
	}
}

// A program can end before its call's result is stored; either way of storing
// the row applies the edits that waited for it.
func TestProgramEffectsWaitForTheirResultRow(t *testing.T) {
	for name, appendRow := range map[string]func(*Service, context.Context, string, api.Message) error{
		"stamped append": func(svc *Service, ctx context.Context, id string, msg api.Message) error {
			return svc.Append(ctx, id, msg)
		},
		"plain append": func(svc *Service, ctx context.Context, id string, msg api.Message) error {
			return svc.AppendPlain(ctx, id, msg)
		},
	} {
		t.Run(name, func(t *testing.T) {
			svc, mem, sessionID := programEffectsFixture(t)
			svc.AppendProgramEffects(t.Context(), sessionID, "c1", tools.ProgramEffects{FileEdits: []api.FileEditSnapshot{textEdit("late.go", "1", "2")}})
			testutil.FailErr(t, "store another result", appendRow(svc, t.Context(), sessionID, commandRow("c0")))
			testutil.FailErr(t, "store result", appendRow(svc, t.Context(), sessionID, commandRow("c1")))
			if got, _ := storedEdits(t, mem, sessionID, "c1"); got != "late.go:1>2" {
				t.Fatalf("stored %q, want the edit that waited", got)
			}
			if got, _ := storedEdits(t, mem, sessionID, "c0"); got != "" {
				t.Fatalf("another call's result took %q", got)
			}
			if len(svc.programEffects.held) != 0 {
				t.Fatalf("still holding %+v", svc.programEffects.held)
			}
		})
	}
}

func TestProgramEffectsForAResultNeverStoredEndAtThePersonsNextMessage(t *testing.T) {
	svc, mem, sessionID := programEffectsFixture(t)
	svc.AppendProgramEffects(t.Context(), sessionID, "c1", tools.ProgramEffects{FileEdits: []api.FileEditSnapshot{textEdit("late.go", "1", "2")}})
	// A host prompt is not the person's message.
	testutil.FailErr(t, "store host prompt", svc.Append(t.Context(), sessionID, api.Message{Role: api.MessageRoleUser, Content: "wake", Visibility: api.MessageVisibilityInternal}))
	if len(svc.programEffects.held[sessionID]) != 1 {
		t.Fatal("a host prompt dropped the held edits")
	}
	testutil.FailErr(t, "store user message", svc.Append(t.Context(), sessionID, api.Message{Role: api.MessageRoleUser, Content: "next"}))
	testutil.FailErr(t, "store result", svc.Append(t.Context(), sessionID, commandRow("c1")))
	if got, _ := storedEdits(t, mem, sessionID, "c1"); got != "" || len(svc.programEffects.held) != 0 {
		t.Fatalf("stored %q and holding %+v after the person's next message", got, svc.programEffects.held)
	}
}

func TestProgramEffectsWithNothingToRecordHoldNothing(t *testing.T) {
	svc, _, sessionID := programEffectsFixture(t)
	svc.AppendProgramEffects(t.Context(), sessionID, "c1", tools.ProgramEffects{})
	if len(svc.programEffects.held) != 0 {
		t.Fatalf("holding %+v for a program that edited nothing", svc.programEffects.held)
	}
}

func TestProgramEffectsAreScreenedLikeTheRowTheyJoin(t *testing.T) {
	svc, mem, sessionID := programEffectsFixture(t)
	svc.SetRedactor(func(_ context.Context, msg api.Message) (api.Message, bool) {
		if msg.ToolResult == nil {
			return msg, false
		}
		result := *msg.ToolResult
		result.FileEdits = append([]api.FileEditSnapshot(nil), result.FileEdits...)
		for i := range result.FileEdits {
			result.FileEdits[i].After = strings.ReplaceAll(result.FileEdits[i].After, "hunter2", "[redacted]")
		}
		msg.ToolResult = &result
		return msg, true
	})
	testutil.FailErr(t, "store result", svc.Append(t.Context(), sessionID, commandRow("c1")))
	svc.AppendProgramEffects(t.Context(), sessionID, "c1", tools.ProgramEffects{FileEdits: []api.FileEditSnapshot{{RootID: "r1", Path: ".env", After: "TOKEN=hunter2"}}})
	if got, _ := storedEdits(t, mem, sessionID, "c1"); got != ".env:->TOKEN=[redacted]" {
		t.Fatalf("stored %q, want the appended text screened", got)
	}
}

// Whichever of the program's end and the row's storing comes first, each edit
// is recorded once.
func TestProgramEffectsAreRecordedOnceWhateverTheOrder(t *testing.T) {
	svc, mem, sessionID := programEffectsFixture(t)
	const calls = 48
	var wg sync.WaitGroup
	for i := range calls {
		callID := fmt.Sprintf("c%d", i)
		wg.Add(2)
		go func() {
			defer wg.Done()
			svc.AppendProgramEffects(context.WithoutCancel(t.Context()), sessionID, callID, tools.ProgramEffects{FileEdits: []api.FileEditSnapshot{textEdit("late.go", "1", "2")}})
		}()
		go func() {
			defer wg.Done()
			if err := svc.Append(context.WithoutCancel(t.Context()), sessionID, commandRow(callID)); err != nil {
				t.Errorf("store %s: %v", callID, err)
			}
		}()
	}
	wg.Wait()
	for i := range calls {
		if got, _ := storedEdits(t, mem, sessionID, fmt.Sprintf("c%d", i)); got != "late.go:1>2" {
			t.Fatalf("call %d stored %q, want its one edit", i, got)
		}
	}
}
