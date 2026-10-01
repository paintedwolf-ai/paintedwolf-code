package sourcefeed

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSourceObserverReceivesCommittedWorkspaceChangesOnly(t *testing.T) {
	calls := 0
	stop := Subscribe("project", "workspace", func(Notice) { calls++ })
	defer stop()
	change := Change{ProjectID: "project", WorkspaceID: "workspace", WorkspaceKind: api.SourceWorkspaceKindProject, RootID: "root", Path: "file.txt", Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginUser}
	// No sink is needed to deliver content-free in-process observations.
	before := bound.Swap(nil)
	defer bound.Store(before)
	delivery, err := EmitTx(t.Context(), nil, change)
	testutil.FailErr(t, "stage source event", err)
	if calls != 0 {
		t.Fatal("observer ran before commit")
	}
	delivery.DeliverCommitted()
	delivery.DeliverCommitted()
	if calls != 1 {
		t.Fatalf("committed deliveries = %d", calls)
	}
	change.WorkspaceID = "another"
	testutil.FailErr(t, "publish other workspace", Emit(t.Context(), change))
	if calls != 1 {
		t.Fatal("observer crossed workspace scope")
	}
	stop()
	change.WorkspaceID = "workspace"
	testutil.FailErr(t, "publish after unsubscribe", Emit(t.Context(), change))
	if calls != 1 {
		t.Fatal("released observer was notified")
	}
}

func TestSourceNoticeSeparatesFileWritesFromMembershipChanges(t *testing.T) {
	file, directory := false, true
	for _, test := range []struct {
		name   string
		event  api.SourceChangesEvent
		writes bool
	}{
		{"file write", api.SourceChangesEvent{Changes: []api.SourceChange{{Op: api.SourceChangeOpWrite, IsDir: &file}}}, true},
		{"directory write", api.SourceChangesEvent{Changes: []api.SourceChange{{Op: api.SourceChangeOpWrite, IsDir: &directory}}}, false},
		{"unknown entry", api.SourceChangesEvent{Changes: []api.SourceChange{{Op: api.SourceChangeOpWrite}}}, false},
		{"deletion", api.SourceChangesEvent{Changes: []api.SourceChange{{Op: api.SourceChangeOpDelete}}}, false},
		{"ledger signal", api.SourceChangesEvent{}, false},
		{"continuity gap", api.SourceChangesEvent{Resync: true, Changes: []api.SourceChange{{Op: api.SourceChangeOpWrite, IsDir: &file}}}, false},
		{"git change", api.SourceChangesEvent{GitChanged: true, Changes: []api.SourceChange{{Op: api.SourceChangeOpWrite, IsDir: &file}}}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if notice := changeNotice(test.event); notice.FileWritesOnly != test.writes {
				t.Fatalf("notice=%+v", notice)
			}
		})
	}
}
