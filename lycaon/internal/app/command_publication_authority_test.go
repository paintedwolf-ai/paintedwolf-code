package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/approvaloutcome"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func TestProgramPublicationRespectsCurrentEffectAuthority(t *testing.T) {
	for _, c := range []struct {
		name, source    string
		effects         []toolcontract.CommandEffectClass
		review          bool
		missingProgress bool
	}{
		{"read only", "cat dir/a.txt", []toolcontract.CommandEffectClass{toolcontract.CommandReadOnly}, false, false},
		{"refused external", "python3 -c 'print(42)'", []toolcontract.CommandEffectClass{toolcontract.CommandReadOnly, toolcontract.CommandMutation}, false, false},
		{"current write review", "cat dir/a.txt", []toolcontract.CommandEffectClass{toolcontract.CommandReadOnly, toolcontract.CommandMutation}, true, false},
		{"missing progress", "cat dir/a.txt", []toolcontract.CommandEffectClass{toolcontract.CommandReadOnly, toolcontract.CommandMutation}, false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, service, p := documentProgram(t)
			dir := filepath.Join(f.Dir, "dir")
			path := filepath.Join(dir, "a.txt")
			testutil.FailErr(t, "directory", os.Mkdir(dir, 0o755))
			testutil.FailErr(t, "saved bytes", os.WriteFile(path, []byte("saved\n"), 0o644))
			doc, err := service.Open(t.Context(), p, "dir/a.txt", "r1", "", "window", nil)
			testutil.FailErr(t, "open", err)
			testutil.FailErr(t, "hold publication", os.Chmod(dir, 0o555))
			t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
			accepted, err := service.ApplyAgentEdit(t.Context(), editordoc.AgentEdit{ProjectID: p.ID, DocumentID: doc.ID, ExpectedRevision: doc.Revision, Content: "agent held\n", OperationID: uuid.NewString(), SessionID: f.Session, ToolCallID: "earlier-edit", ToolName: "edit"})
			testutil.FailErr(t, "accept held edit", err)
			if accepted.Saved || accepted.PublicationError == nil {
				t.Fatalf("edit=%+v", accepted)
			}
			testutil.FailErr(t, "permit disk save", os.Chmod(dir, 0o755))
			effects, err := toolcontract.NewCommandEffects(c.effects...)
			testutil.FailErr(t, "restricted effects", err)
			f.Runtime.Boundary.SetProfileSource(func(_ context.Context, _ string) []sandbox.ToolProfile {
				return []sandbox.ToolProfile{{ID: "fixture", CommandEffects: effects}}
			})
			if c.missingProgress {
				f.Runtime.Progress = publicationProgressFacts{store: progress.NewMemoryStore()}
			}
			reviewCalls := 0
			if c.review {
				original := f.Runtime.Owners
				f.Runtime.Owners = func(tc tools.ToolContext, paths *projectpaths.ProgramPaths, memory *toolcontract.ProgramMemory, spill tooloutput.SpillAdmission) (command.ProgramOwners, error) {
					owners, err := original(tc, paths, memory, spill)
					owners.PublishDocument = (&native.WriteTool{Boundary: f.Runtime.Boundary, ContentApply: publicationReviewGate{calls: &reviewCalls}}).PublishAccepted
					return owners, err
				}
			}
			result := callDocumentProgram(t, f, c.source)
			disk, err := os.ReadFile(path)
			testutil.FailErr(t, "disk after command", err)
			if string(disk) != "saved\n" {
				t.Fatalf("command published without authority: disk=%q result=%+v", disk, result)
			}
			current, err := service.CurrentSnapshot(t.Context(), p.ID, doc.ID)
			testutil.FailErr(t, "held draft", err)
			if !current.Dirty || current.Draft != "agent held\n" {
				t.Fatalf("held draft=%+v", current)
			}
			if c.name == "read only" && (result.Status != 0 || result.Stdout != "saved\n") {
				t.Fatalf("read=%+v", result)
			}
			if c.review && (reviewCalls != 1 || result.StopCode != approvaloutcome.CodeApprovalDenied) {
				t.Fatalf("review calls=%d result=%+v", reviewCalls, result)
			}
			if c.missingProgress && (result.StopCode != "PROGRESS_MISSING" || result.CompletedEffects != 0) {
				t.Fatalf("progress=%+v", result)
			}
			if c.name == "refused external" && result.StopCode != "COMMAND_EFFECT_NOT_ALLOWED" {
				t.Fatalf("external=%+v", result)
			}
		})
	}
}

type publicationReviewGate struct{ calls *int }

func (g publicationReviewGate) GateApply(_ context.Context, _, _ string, _ *string, _ string, _ tools.ToolContext) (string, error) {
	*g.calls++
	return "", &toolrejection.ToolReject{Code: approvaloutcome.CodeApprovalDenied}
}

func TestProgramPublicationRedirectEffectsAdmitBeforeOpening(t *testing.T) {
	for _, source := range []string{": > clean.txt", "true > clean.txt", ": > clean.txt 1>&2", "true > clean.txt 1>&2", "> clean.txt", "python3 -c 'print(42)' > clean.txt"} {
		t.Run(source, func(t *testing.T) {
			f, _, _ := documentProgram(t)
			effects, err := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
			testutil.FailErr(t, "effects", err)
			f.Runtime.Boundary.SetProfileSource(func(context.Context, string) []sandbox.ToolProfile {
				return []sandbox.ToolProfile{{ID: "fixture", CommandEffects: effects}}
			})
			result := callDocumentProgram(t, f, source)
			if result.StopCode != "COMMAND_EFFECT_NOT_ALLOWED" || result.CompletedEffects != 0 {
				t.Fatalf("result=%+v", result)
			}
			if _, err := os.Stat(filepath.Join(f.Dir, "clean.txt")); !os.IsNotExist(err) {
				t.Fatalf("unadmitted destination exists: %v", err)
			}
		})
	}
}

func TestProgramPublicationSettlesBeforeRedirectCapture(t *testing.T) {
	for _, source := range []string{"cat < dir/a.txt", "cat dir/a.txt > copy.txt; cat copy.txt"} {
		t.Run(source, func(t *testing.T) {
			f, service, p := documentProgram(t)
			dir := filepath.Join(f.Dir, "dir")
			path := filepath.Join(dir, "a.txt")
			testutil.FailErr(t, "directory", os.Mkdir(dir, 0o755))
			testutil.FailErr(t, "saved", os.WriteFile(path, []byte("saved\n"), 0o644))
			doc, err := service.Open(t.Context(), p, "dir/a.txt", "r1", "", "window", nil)
			testutil.FailErr(t, "open", err)
			testutil.FailErr(t, "hold", os.Chmod(dir, 0o555))
			t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
			held, err := service.ApplyAgentEdit(t.Context(), editordoc.AgentEdit{ProjectID: p.ID, DocumentID: doc.ID, ExpectedRevision: doc.Revision, Content: "accepted\n", OperationID: uuid.NewString(), SessionID: f.Session, ToolCallID: "earlier", ToolName: "edit"})
			testutil.FailErr(t, "accepted", err)
			if held.Saved || held.PublicationError == nil {
				t.Fatalf("edit=%+v", held)
			}
			testutil.FailErr(t, "permit", os.Chmod(dir, 0o755))
			result := callDocumentProgram(t, f, source)
			if result.Status != 0 || result.Stdout != "accepted\n" || result.StopCode != "" {
				t.Fatalf("result=%+v", result)
			}
			disk, err := os.ReadFile(path)
			testutil.FailErr(t, "published", err)
			if string(disk) != "accepted\n" {
				t.Fatalf("disk=%q", disk)
			}
		})
	}
	f, _, _ := documentProgram(t)
	result := callDocumentProgram(t, f, "true < missing.txt")
	if result.Status == 0 {
		t.Fatalf("missing input did not prevent producer: %+v", result)
	}
}

type publicationProgressFacts struct{ store progress.Store }

func (p publicationProgressFacts) ProgramProgressState(ctx context.Context, session string) (progress.PrerequisiteState, error) {
	return progress.PrerequisiteState{SessionID: session, RootSessionID: session, Content: p.store.Get(ctx, session)}, nil
}

func TestProgramPublicationRedirectRequiresCurrentWriteProgress(t *testing.T) {
	for _, source := range []string{": > new.txt", "true > existing.txt", "cat existing.txt > new.txt", "true > new.txt 1>&2"} {
		t.Run(source, func(t *testing.T) {
			f, _, _ := documentProgram(t)
			path := filepath.Join(f.Dir, "existing.txt")
			testutil.FailErr(t, "existing", os.WriteFile(path, []byte("saved\n"), 0o644))
			f.Runtime.Progress = publicationProgressFacts{store: progress.NewMemoryStore()}
			pure := callDocumentProgram(t, f, ":")
			if pure.Status != 0 || pure.StopCode != "" {
				t.Fatalf("pure=%+v", pure)
			}
			result := callDocumentProgram(t, f, source)
			if result.StopCode != "PROGRESS_MISSING" || result.CompletedEffects != 0 {
				t.Fatalf("result=%+v", result)
			}
			disk, err := os.ReadFile(path)
			testutil.FailErr(t, "existing after refusal", err)
			if string(disk) != "saved\n" {
				t.Fatalf("disk=%q", disk)
			}
			if _, err := os.Stat(filepath.Join(f.Dir, "new.txt")); !os.IsNotExist(err) {
				t.Fatalf("destination exists: %v", err)
			}
		})
	}
}

func TestProgramPublicationReviewRaceStopsBeforeRead(t *testing.T) {
	f, service, p := documentProgram(t)
	dir := filepath.Join(f.Dir, "dir")
	path := filepath.Join(dir, "a.txt")
	testutil.FailErr(t, "directory", os.Mkdir(dir, 0o755))
	testutil.FailErr(t, "saved", os.WriteFile(path, []byte("saved\n"), 0o644))
	doc, err := service.Open(t.Context(), p, "dir/a.txt", "r1", "", "window", nil)
	testutil.FailErr(t, "open", err)
	testutil.FailErr(t, "hold", os.Chmod(dir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	held, err := service.ApplyAgentEdit(t.Context(), editordoc.AgentEdit{ProjectID: p.ID, DocumentID: doc.ID, ExpectedRevision: doc.Revision, Content: "accepted\n", OperationID: uuid.NewString(), SessionID: f.Session, ToolCallID: "earlier", ToolName: "edit"})
	testutil.FailErr(t, "accepted", err)
	if held.Saved || held.PublicationError == nil {
		t.Fatalf("edit=%+v", held)
	}
	testutil.FailErr(t, "permit", os.Chmod(dir, 0o755))
	calls := 0
	original := f.Runtime.Owners
	f.Runtime.Owners = func(tc tools.ToolContext, paths *projectpaths.ProgramPaths, memory *toolcontract.ProgramMemory, spill tooloutput.SpillAdmission) (command.ProgramOwners, error) {
		owners, err := original(tc, paths, memory, spill)
		owners.PublishDocument = (&native.WriteTool{Boundary: f.Runtime.Boundary, ContentApply: publicationRaceGate{change: func(ctx context.Context) error {
			calls++
			current, err := service.CurrentSnapshot(ctx, p.ID, doc.ID)
			if err != nil {
				return err
			}
			_, err = service.ReplaceSnapshot(ctx, doc.ID, p.ID, editordoc.SnapshotReplacement{DocumentCommand: editordoc.DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: current.Revision}, Content: "human during review\n", EOL: "lf"})
			return err
		}}}).PublishAccepted
		return owners, err
	}
	result := callDocumentProgram(t, f, "cat dir/a.txt; true > later.txt")
	if calls != 1 || result.StopCode != toolrejection.ToolOwnerFailedCode || result.CompletedEffects != 0 || result.Stdout != "" {
		t.Fatalf("review calls=%d result=%+v", calls, result)
	}
	disk, err := os.ReadFile(path)
	testutil.FailErr(t, "disk", err)
	if string(disk) != "saved\n" {
		t.Fatalf("disk=%q", disk)
	}
	current, err := service.CurrentSnapshot(t.Context(), p.ID, doc.ID)
	testutil.FailErr(t, "human draft", err)
	if !current.Dirty || current.Draft != "human during review\n" {
		t.Fatalf("draft=%+v", current)
	}
	if _, err := os.Stat(filepath.Join(f.Dir, "later.txt")); !os.IsNotExist(err) {
		t.Fatalf("later child ran: %v", err)
	}
}

type publicationRaceGate struct{ change func(context.Context) error }

func (g publicationRaceGate) GateApply(ctx context.Context, _, _ string, _ *string, proposed string, _ tools.ToolContext) (string, error) {
	return proposed, g.change(ctx)
}
