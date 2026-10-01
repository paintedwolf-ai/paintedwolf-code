package editordoc

import (
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/project"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourcerewind"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSourceRewindSelectivelyUndoesAgentTextAndPreservesSavedHumanText(t *testing.T) {
	f, ledger := newLedgerAgentFixture(t, map[string]string{"a.txt": "one\ntwo\n"})
	ctx := t.Context()
	sessionID := "chat-1"
	anchor := uuid.NewString()
	testdbseed.InsertSession(t, f.store.db, sessionID, f.project.ID)
	sessions := sessionstore.NewSQL(f.store.db.(db.ReadHandle))
	testutil.FailErr(t, "append selected ask", sessions.AppendMessages(ctx, sessionID, api.Message{ID: anchor, Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "change the first line"}))
	d := f.open(t, "a.txt")
	request := agentEdit(d, "one agent\ntwo\n")
	request.Turn = 1
	result, err := f.service.ApplyAgentEdit(ctx, request)
	testutil.FailErr(t, "agent edit", err)
	if !result.Saved {
		t.Fatal("agent change did not reach disk")
	}
	d = result.Document
	d, err = f.service.ReplaceSnapshot(ctx, d.ID, f.project.ID, SnapshotReplacement{DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: d.Revision}, Content: "one agent\ntwo human\n", EOL: "lf"})
	testutil.FailErr(t, "human edit", err)
	d, err = f.service.Save(ctx, f.project, d.ID, "window", uuid.NewString(), "", 0, d.Revision)
	testutil.FailErr(t, "save human edit", err)
	service := &sourcerewind.Service{Ledger: ledger, Mutations: project.NewSourceMutationService(f.store.db, ledger), Documents: f.service}
	plan, err := service.Prepare(ctx, f.project, sessionID, []string{anchor})
	testutil.FailErr(t, "preview selective rewind", err)
	if len(plan.Files) != 1 || string(plan.Files[0].Target.Content) != "one\ntwo human\n" {
		t.Fatalf("plan=%+v", plan)
	}
	if f.disk(t, "a.txt") != "one agent\ntwo human\n" {
		t.Fatal("preview changed the working file")
	}
	repeated, err := service.Prepare(ctx, f.project, sessionID, []string{anchor})
	testutil.FailErr(t, "repeat semantic preview", err)
	if !reflect.DeepEqual(plan.Files, repeated.Files) {
		t.Fatal("semantic preview changed without a document edit")
	}
	person, err := f.store.people.HostOwner(ctx)
	testutil.FailErr(t, "resolve rewind actor", err)
	plan.PersonID = person.ID
	testutil.FailErr(t, "apply selective rewind", service.Apply(ctx, f.project, plan, func() error { return nil }))
	if got := f.disk(t, "a.txt"); got != "one\ntwo human\n" {
		t.Fatalf("disk=%q", got)
	}
	document, err := f.store.Get(ctx, d.ID)
	testutil.FailErr(t, "read accepted document", err)
	if document.Draft != "one\ntwo human\n" || document.Dirty {
		t.Fatalf("document=%+v", document)
	}
	testutil.FailErr(t, "compensate selective rewind", service.RollbackProject(ctx, f.project, plan, func() error { return nil }))
	if got := f.disk(t, "a.txt"); got != "one agent\ntwo human\n" {
		t.Fatalf("compensated disk=%q", got)
	}
	retry, err := service.Prepare(ctx, f.project, sessionID, []string{anchor})
	testutil.FailErr(t, "preview compensated retry", err)
	if len(retry.Files) != 1 || string(retry.Files[0].Target.Content) != "one\ntwo human\n" {
		t.Fatalf("retry=%+v", retry)
	}
	retry.PersonID = person.ID
	testutil.FailErr(t, "retry selective rewind", service.Apply(ctx, f.project, retry, func() error { return nil }))
	if got := f.disk(t, "a.txt"); got != "one\ntwo human\n" {
		t.Fatalf("retry disk=%q", got)
	}
	versions, err := ledger.QueryFileVersions(ctx, f.project.ID, d.FileID, 10, 0)
	testutil.FailErr(t, "read rewind versions", err)
	if len(versions.Versions) == 0 || versions.Versions[0].Origin != api.SourceChangeOriginUser || versions.Versions[0].Cause != "session_rewind" {
		t.Fatalf("versions=%+v", versions.Versions)
	}
}
