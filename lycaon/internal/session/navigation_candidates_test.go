package session

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/transcript"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestNavigationCandidateRechecksOnlyTheSelectedStoredAddress(t *testing.T) {
	registry := project.NewMemoryRegistry()
	p, err := registry.Create(t.Context(), project.CreateParams{Roots: []project.AttachRootParams{{Path: t.TempDir()}}})
	testutil.FailErr(t, "create project", err)
	mem := sessionstore.NewMemory()
	sess, err := mem.Create(t.Context(), api.CreateSessionRequest{}, p.ID)
	testutil.FailErr(t, "create session", err)
	mgr := NewHost(mem, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	mgr.SetProjectRegistry(registry)
	ref := api.NavigationReference{ID: "ref-0", Syntax: "code", Mention: "same.go", ProjectID: p.ID, Path: "same.go", Status: api.NavigationAmbiguous, Line: 12, EndLine: 20,
		Candidates: []api.NavigationTarget{
			{ProjectID: p.ID, RootID: p.Roots[0].ID, Path: "a/same.go", EntryKind: "file"},
			{ProjectID: p.ID, RootID: p.Roots[0].ID, Path: "b/same.go", EntryKind: "file", WorkerID: "worker-1"},
			{ProjectID: "other-project", RootID: p.Roots[0].ID, Path: "same.go", EntryKind: "file"},
		},
	}
	const content = "See `same.go`."
	testutil.FailErr(t, "append ambiguous message", mem.AppendMessages(t.Context(), sess.ID, api.Message{ID: "m", Role: api.MessageRoleAssistant, Content: content, NavigationRefs: []api.NavigationReference{ref}}))
	index := 1
	req := api.ResolveMessageNavigationRequest{MessageID: "m", ContentSHA256: sessionstore.NavigationContentHash(content), ReferenceID: ref.ID, CandidateIndex: &index}
	calls := 0
	resolve := func(_ context.Context, _ *project.Project, _ api.Message, refs []api.NavigationReference) []api.NavigationReference {
		calls++
		if len(refs) != 1 || refs[0].Path != "b/same.go" || refs[0].WorkerID != "worker-1" || refs[0].Line != 12 || refs[0].EndLine != 20 || len(refs[0].Candidates) != 0 {
			t.Fatalf("selected references = %+v", refs)
		}
		refs[0].Deleted = true
		return refs
	}
	out, err := mgr.Runner.Transcript.ResolveNavigation(t.Context(), sess.ID, req, resolve)
	testutil.FailErr(t, "resolve selected candidate", err)
	if !out.References[0].Deleted || calls != 1 {
		t.Fatalf("resolution = %+v, calls = %d", out, calls)
	}
	stored, err := mem.GetMessage(t.Context(), sess.ID, "m")
	testutil.FailErr(t, "read original reference", err)
	if stored.NavigationRefs[0].Status != api.NavigationAmbiguous || len(stored.NavigationRefs[0].Candidates) != 3 {
		t.Fatal("candidate selection rewrote durable binding")
	}
	for _, invalid := range []int{-1, 2, 3, 32} {
		req.CandidateIndex = &invalid
		if _, err := mgr.Runner.Transcript.ResolveNavigation(t.Context(), sess.ID, req, resolve); !errors.Is(err, transcript.ErrNavigationCandidateInvalid) {
			t.Fatalf("candidate %d = %v", invalid, err)
		}
	}
	if calls != 1 {
		t.Fatal("invalid candidate reached path resolution")
	}
}
