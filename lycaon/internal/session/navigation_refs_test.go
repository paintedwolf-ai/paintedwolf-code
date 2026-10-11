package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestAssistantCommitAndNotePersistNavigationReferences(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	testutil.FailErr(t, "make scripts folder", os.Mkdir(filepath.Join(root, "scripts"), 0o755))
	testutil.FailErr(t, "write findings", os.WriteFile(filepath.Join(root, "FINDINGS.md"), []byte("x"), 0o600))
	registry := project.NewMemoryRegistry()
	p, err := registry.Create(ctx, project.CreateParams{Roots: []project.AttachRootParams{{Path: root}}})
	testutil.FailErr(t, "create project", err)
	mem := sessionstore.NewMemory()
	sess, err := mem.Create(ctx, api.CreateSessionRequest{}, p.ID)
	testutil.FailErr(t, "create session", err)
	mgr := NewHost(mem, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	mgr.SetProjectRegistry(registry)

	testutil.FailErr(t, "append agent note", mgr.Runner.Transcript.Append(ctx, sess.ID, api.Message{
		ID: "note-1", Role: api.MessageRoleAssistant, Kind: api.MessageKindAgentNote,
		Content: "See scripts/.", Visibility: api.MessageVisibilityTranscript,
	}))
	testutil.FailErr(t, "append provisional answer", mgr.Runner.Transcript.Append(ctx, sess.ID, api.Message{
		ID: "answer-1", Role: api.MessageRoleAssistant, Content: "See FINDINGS.md.",
		SourceContext: &api.SourceContext{Locations: []api.NavigationTarget{{ProjectID: p.ID, RootID: p.Roots[0].ID, Path: "FINDINGS.md", EntryKind: api.NavigationEntryKindFile}}},
		Visibility:    api.MessageVisibilityInternal,
	}))
	testutil.FailErr(t, "commit provisional answer", mgr.Runner.Transcript.Update(ctx, sess.ID, "answer-1", api.Message{
		ID: "answer-1", Role: api.MessageRoleAssistant, Content: "See FINDINGS.md.",
		Visibility: api.MessageVisibilityTranscript,
	}))
	msgs, err := mem.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "read messages", err)
	if len(msgs) != 2 || len(msgs[0].NavigationRefs) != 1 || len(msgs[1].NavigationRefs) != 1 {
		t.Fatalf("messages = %+v", msgs)
	}
	if msgs[0].NavigationRefs[0].Status != api.NavigationPending || msgs[1].NavigationRefs[0].Path != "FINDINGS.md" {
		t.Fatalf("navigation refs = %+v / %+v", msgs[0].NavigationRefs, msgs[1].NavigationRefs)
	}
}

func TestNavigationParsesWorktreeAbsolutePathsWithoutFilesystemValidation(t *testing.T) {
	registry := project.NewMemoryRegistry()
	p, err := registry.Create(t.Context(), project.CreateParams{Roots: []project.AttachRootParams{{Path: t.TempDir()}}})
	testutil.FailErr(t, "project", err)
	mem := sessionstore.NewMemory()
	sess, err := mem.Create(t.Context(), api.CreateSessionRequest{}, p.ID)
	testutil.FailErr(t, "session", err)
	branch := filepath.Join(t.TempDir(), "not-materialized")
	testutil.FailErr(t, "worktree binding", mem.PutWorktreeBinding(t.Context(), sessionstore.WorktreeBinding{SessionID: sess.ID, ProjectID: p.ID, Toplevel: p.Roots[0].Path, WorktreePath: branch, Branch: "work", BaseBranch: "main"}))
	mgr := NewHost(mem, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	mgr.SetProjectRegistry(registry)
	rows := []api.Message{{Role: api.MessageRoleAssistant, Content: "[file](" + filepath.ToSlash(filepath.Join(branch, "src/a.go")) + ")", Visibility: api.MessageVisibilityTranscript}}
	mgr.Runner.Transcript.NavigationRefs(t.Context(), sess.ID, rows)
	if len(rows[0].NavigationRefs) != 1 || rows[0].NavigationRefs[0].Path != "src/a.go" || rows[0].NavigationRefs[0].RootID != p.Roots[0].ID {
		t.Fatalf("references=%+v", rows[0].NavigationRefs)
	}
}
