package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/sourceledger"
	reposurvey "github.com/lycaon/lycaon/internal/survey"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/native/literal"
	"github.com/lycaon/lycaon/internal/tools/native/mutation"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func documentProgram(t *testing.T) (*programtest.Fixture, *editordoc.Service, *project.Project) {
	t.Helper()
	confine.TestingSetAutoConfine(t)
	t.Setenv("LYCAON_BYPASS_APPROVALS", "0")
	t.Setenv("LYCAON_SANDBOX", "on")
	t.Setenv("LYCAON_SANDBOX_NETWORK", "deny")
	f := programtest.New(t, programtest.Options{})
	p := &project.Project{ID: testdbseed.DefaultProjectID, Roots: []project.Root{{ID: "r1", ProjectID: testdbseed.DefaultProjectID, Path: f.Dir, IsPrimary: true}}}
	testdbseed.InsertProjectRootWithID(t, f.Database, p.ID, "r1", f.Dir)
	ledger := sourceledger.New(f.Database, "")
	service := editordoc.New(editordoc.NewStore(f.Database), ledger, ledger.History, adapterRoots{p: p})
	t.Cleanup(func() { _ = service.Close(context.Background()) })
	f.Context = func(_ string, tc *tools.ToolContext) {
		tc.Source.EditorDocuments = editorDocumentsAdapter{service: service}
		tc.Source.SourceLedger = ledger
	}
	repo := &reposurvey.RepoTool{Boundary: f.Runtime.Boundary, Caps: reposurvey.DefaultCaps(), BaseCatalog: &reposurvey.Catalog{Bundles: map[string]reposurvey.Bundle{
		"layout_overview": {ID: "layout_overview", Probes: []reposurvey.Probe{{Kind: reposurvey.ProbeGrep, Pattern: "marker", Path: ".", Label: "content"}}},
	}}}
	testutil.FailErr(t, "register shared repo owner", f.Runtime.Registry.Register("survey_repo", repo.Run))
	owners, err := literal.NewOwners(native.ExecutionRevisions{Boundary: f.Runtime.Boundary}, f.Recorder, literal.DomainOwners{Repo: repo})
	testutil.FailErr(t, "literal owners", err)
	boundary := f.Runtime.Boundary
	mutations, err := mutation.NewOwners(native.ExecutionRevisions{Boundary: boundary}, f.Recorder, mutation.Operations{
		Write: &native.WriteTool{Boundary: boundary}, Delete: &native.DeleteTool{Boundary: boundary}, Move: &native.MoveTool{Boundary: boundary}, Copy: &native.CopyTool{Boundary: boundary}, Mkdir: &native.MkdirTool{Boundary: boundary}, Chmod: &native.ChmodTool{Boundary: boundary}, Chown: &native.ChownTool{Boundary: boundary}, Times: &native.TimesTool{Boundary: boundary}, Extract: &native.ExtractArchiveTool{Boundary: boundary},
	})
	testutil.FailErr(t, "mutation owners", err)
	f.Runtime.Owners = func(tc tools.ToolContext, paths *projectpaths.ProgramPaths, memory *toolcontract.ProgramMemory, admission tooloutput.SpillAdmission) (command.ProgramOwners, error) {
		literalOwners := owners.ForProgram(memory)
		mutationProgram := *mutations
		mutationProgram.Revisions.Memory, mutationProgram.Admission = memory, admission
		mutations := &mutationProgram
		publication := &mutation.Publication{Owners: mutations, Program: mutation.Program{Paths: paths}, Tools: tc}
		prepare := func(_ context.Context, paths *projectpaths.ProgramPaths, _ commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
			prepared, err := literalOwners.Prepare(paths, request)
			return command.NativePrepared{Tool: prepared.Operation, Review: prepared.Review, Invoke: prepared.InvokeOwner}, err
		}
		return command.ProgramOwners{PublishDocument: mutations.Operations.Write.PublishAccepted, Publication: func(admit tools.CommandOutputAdmission) commandprogram.OutputPublication {
			copy := *publication
			copy.Admission = admit
			return &copy
		}, Input: publication, Redirections: func(frame *projectpaths.ProgramPaths, admit tools.CommandOutputAdmission) (commandprogram.InputRedirection, commandprogram.OutputPublication) {
			pub := &mutation.Publication{Owners: mutations, Program: mutation.Program{Paths: frame}, Tools: tc, Admission: admit}
			return pub, pub
		}, Natives: map[string]command.NativeOperation{
			"cat": {Tool: "read", Prepare: prepare}, "repo-map": {Tool: "survey_repo", Prepare: prepare},
		}}, nil
	}
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	return f, service, p
}

func callDocumentProgram(t *testing.T, f *programtest.Fixture, source string) command.ProgramOutcome {
	t.Helper()
	raw, err := f.Call(t, t.Context(), source)
	testutil.FailErr(t, "command", err)
	var result command.ProgramOutcome
	testutil.FailErr(t, "program result", json.Unmarshal([]byte(raw), &result))
	return result
}

func TestProgramPublishesOwnAgentDraftBeforeNativeReadAndGoTest(t *testing.T) {
	f, service, p := documentProgram(t)
	testutil.FailErr(t, "mkdir", os.Mkdir(filepath.Join(f.Dir, "pkg"), 0o755))
	initial := "package pkg\nconst Value=0\n"
	accepted := "package pkg\nconst Value=42\n"
	testutil.FailErr(t, "source", os.WriteFile(filepath.Join(f.Dir, "pkg", "value.go"), []byte(initial), 0o644))
	testutil.FailErr(t, "module", os.WriteFile(filepath.Join(f.Dir, "go.mod"), []byte("module example/probe\ngo 1.23\n"), 0o644))
	testutil.FailErr(t, "test fixture", os.WriteFile(filepath.Join(f.Dir, "pkg", "value_test.go"), []byte("package pkg\nimport \"testing\"\nfunc TestValue(t *testing.T){if Value!=42{t.Fatal(Value)}}\n"), 0o644))
	doc, err := service.Open(t.Context(), p, "pkg/value.go", "r1", "", "window", nil)
	testutil.FailErr(t, "open", err)
	dir := filepath.Join(f.Dir, "pkg")
	testutil.FailErr(t, "hold publication", os.Chmod(dir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	applied, err := service.ApplyAgentEdit(t.Context(), editordoc.AgentEdit{ProjectID: p.ID, DocumentID: doc.ID, ExpectedRevision: doc.Revision, Content: accepted, OperationID: uuid.NewString(), SessionID: f.Session, ToolCallID: "edit-call", ToolName: "edit"})
	testutil.FailErr(t, "accept unsaved agent edit", err)
	if applied.Saved || applied.PublicationError == nil {
		t.Fatalf("edit=%+v", applied)
	}
	testutil.FailErr(t, "permit save", os.Chmod(dir, 0o755))
	result := callDocumentProgram(t, f, fmt.Sprintf("cat pkg/value.go; GOCACHE=%q GOMODCACHE=%q GOPROXY=off go test ./pkg", filepath.Join(f.Dir, "cache"), filepath.Join(f.Dir, "modcache")))
	if result.Status != 0 || !strings.HasPrefix(result.Stdout, accepted) || result.Stderr != "" {
		t.Fatalf("program=%+v", result)
	}
	saved, err := os.ReadFile(filepath.Join(dir, "value.go"))
	testutil.FailErr(t, "saved source", err)
	if string(saved) != accepted {
		t.Fatalf("saved=%q", saved)
	}
	page, err := f.Recorder.ReadProgramPage(t.Context(), f.Session, result.ProgramID, "", 100)
	testutil.FailErr(t, "preparation receipt", err)
	publications := 0
	for _, execution := range page.Executions {
		if execution.AcceptedRevision == fmt.Sprintf("document:%s@%d", doc.ID, applied.Document.Revision) {
			publications++
			if execution.NativeCommand != "write" || execution.EffectMode != "publication" || !execution.OwnerInvoked || execution.PublishedRevision == "" || execution.EffectCertainty != "completed" {
				t.Fatalf("publication receipt=%+v", execution)
			}
		}
	}
	if publications != 1 {
		t.Fatalf("preparation executions=%d receipts=%+v", publications, page.Executions)
	}
}

func TestProgramHumanDraftKeepsOneSavedFileViewAndReadNote(t *testing.T) {
	f, service, p := documentProgram(t)
	testutil.FailErr(t, "saved file", os.WriteFile(filepath.Join(f.Dir, "a.txt"), []byte("saved\n"), 0o644))
	noDraft := callDocumentProgram(t, f, "cat a.txt")
	if noDraft.Status != 0 || noDraft.Stdout != "saved\n" || noDraft.Stderr != "" {
		t.Fatalf("no draft=%+v", noDraft)
	}
	doc, err := service.Open(t.Context(), p, "a.txt", "r1", "", "window", nil)
	testutil.FailErr(t, "open", err)
	base, err := service.AgentReadBase(t.Context(), p.ID, f.Session, doc.ID, nil)
	testutil.FailErr(t, "real command delivery establishes editor read", err)
	if !base.ReadWhole || base.Revision != doc.Revision {
		t.Fatalf("command read basis=%+v", base)
	}
	typed, err := service.ReplaceSnapshot(t.Context(), doc.ID, p.ID, editordoc.SnapshotReplacement{DocumentCommand: editordoc.DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: doc.Revision}, Content: "human unsaved\n", EOL: "lf"})
	testutil.FailErr(t, "human types", err)
	result := callDocumentProgram(t, f, `cat a.txt; python3 -c 'from pathlib import Path; print(Path("a.txt").read_text(),end="")'; cat a.txt`)
	expected := fmt.Sprintf("command: %s has unsaved changes in the editor; commands read the saved file.\n", filepath.Join(f.Dir, "a.txt"))
	if result.Status != 0 || result.Stdout != "saved\nsaved\nsaved\n" || result.Stderr != expected {
		t.Fatalf("human draft=%+v", result)
	}

	nativeWrite := callDocumentProgram(t, f, "cat a.txt > a.txt")
	if nativeWrite.StopCode != "COMMAND_REVISION_CONFLICT" || nativeWrite.CompletedEffects != 0 {
		t.Fatalf("native draft write=%+v", nativeWrite)
	}
	page, err := f.Recorder.ReadProgramPage(t.Context(), f.Session, nativeWrite.ProgramID, "", 100)
	testutil.FailErr(t, "refused redirect receipt", err)
	refusedWrite := 0
	for _, execution := range page.Executions {
		if execution.StopCode == "COMMAND_REVISION_CONFLICT" && !execution.OwnerStarted && !execution.OwnerInvoked && execution.EffectCertainty == "none" {
			refusedWrite++
		}
	}
	if refusedWrite != 1 {
		t.Fatalf("refused writes=%d executions=%+v", refusedWrite, page.Executions)
	}
	saved, err := os.ReadFile(filepath.Join(f.Dir, "a.txt"))
	testutil.FailErr(t, "saved file after refused native write", err)
	if string(saved) != "saved\n" {
		t.Fatalf("draft path overwritten: %q", saved)
	}
	live, err := service.CurrentSnapshot(t.Context(), p.ID, typed.ID)
	testutil.FailErr(t, "live draft", err)
	if live.Draft != "human unsaved\n" || !live.Dirty {
		t.Fatalf("draft=%+v", live)
	}
}

func TestProgramAppendStopsOnUnsavedAcceptanceWithoutReplaying(t *testing.T) {
	f, service, p := documentProgram(t)
	dir := filepath.Join(f.Dir, "dir")
	testutil.FailErr(t, "directory", os.Mkdir(dir, 0o755))
	testutil.FailErr(t, "saved target", os.WriteFile(filepath.Join(dir, "a.txt"), []byte("base\n"), 0o644))
	testutil.FailErr(t, "input", os.WriteFile(filepath.Join(f.Dir, "input.txt"), []byte("added\n"), 0o644))
	_, err := service.Open(t.Context(), p, "dir/a.txt", "r1", "", "window", nil)
	testutil.FailErr(t, "open target", err)
	testutil.FailErr(t, "hold save", os.Chmod(dir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	stopped := callDocumentProgram(t, f, "cat input.txt; cat input.txt >> dir/a.txt; cat never.txt")
	if stopped.Refusal == nil || stopped.Refusal.Code != "COMMAND_PUBLICATION_PENDING" || stopped.Stdout != "added\n" || stopped.CompletedEffects < 1 {
		t.Fatalf("stopped=%+v", stopped)
	}
	page, err := f.Recorder.ReadProgramPage(t.Context(), f.Session, stopped.ProgramID, "", 100)
	testutil.FailErr(t, "receipt", err)
	held := false
	for _, execution := range page.Executions {
		if execution.HeldVersionID != "" {
			held = true
			if execution.AcceptedRevision == "" || execution.PublishedRevision != "" {
				t.Fatalf("accepted receipt=%+v", execution)
			}
		}
	}
	if !held {
		t.Fatalf("no held effect in receipt=%+v", page.Executions)
	}
	saved, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	testutil.FailErr(t, "saved bytes", err)
	if string(saved) != "base\n" {
		t.Fatalf("unpublished disk=%q", saved)
	}
	testutil.FailErr(t, "allow save", os.Chmod(dir, 0o755))
	continued := callDocumentProgram(t, f, "cat dir/a.txt")
	if continued.Status != 0 || continued.Stdout != "base\nadded\n" {
		t.Fatalf("continued=%+v", continued)
	}
}

func TestProgramRepoMapReadsSavedContentAndReportsHumanDraft(t *testing.T) {
	f, service, p := documentProgram(t)
	testutil.FailErr(t, "saved probe content", os.WriteFile(filepath.Join(f.Dir, "a.txt"), []byte("marker saved\n"), 0o644))
	clean := callDocumentProgram(t, f, "repo-map --path a.txt")
	if clean.Status != 0 || !strings.Contains(clean.Stdout, "marker saved") || clean.Stderr != "" {
		t.Fatalf("clean map=%+v", clean)
	}
	doc, err := service.Open(t.Context(), p, "a.txt", "r1", "", "window", nil)
	testutil.FailErr(t, "open probe source", err)
	typed, err := service.ReplaceSnapshot(t.Context(), doc.ID, p.ID, editordoc.SnapshotReplacement{DocumentCommand: editordoc.DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: doc.Revision}, Content: "marker human\n", EOL: "lf"})
	testutil.FailErr(t, "human probe draft", err)
	dirty := callDocumentProgram(t, f, "repo-map --path a.txt")
	wantNote := fmt.Sprintf("command: %s has unsaved changes in the editor; commands read the saved file.\n", filepath.Join(f.Dir, "a.txt"))
	if dirty.Status != 0 || !strings.Contains(dirty.Stdout, "marker saved") || strings.Contains(dirty.Stdout, "marker human") || dirty.Stderr != wantNote {
		t.Fatalf("dirty map=%+v", dirty)
	}
	live, err := service.CurrentSnapshot(t.Context(), p.ID, typed.ID)
	testutil.FailErr(t, "preserved probe draft", err)
	if !live.Dirty || live.Draft != "marker human\n" {
		t.Fatalf("probe draft=%+v", live)
	}
	if reads := service.FreezeAgentReads(p.ID, f.Session); len(reads) != 0 {
		t.Fatalf("unmapped probe output granted editor reads=%+v", reads)
	}
}
