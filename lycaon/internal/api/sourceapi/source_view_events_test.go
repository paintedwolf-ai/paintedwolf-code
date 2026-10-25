package sourceapi

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// Progress that leaves the snapshot unchanged announces nothing; an intent change does.
func TestSourceViewNotificationsAnnounceOnlyObservableChanges(t *testing.T) {
	hub := events.NewMemoryHub()
	server := newSourceHandlerFixture(t, func(d *Deps) { d.Events = hub })
	root := t.TempDir()
	testutil.FailErr(t, "create directory", os.Mkdir(filepath.Join(root, "src"), 0o700))
	testutil.FailErr(t, "create file", os.WriteFile(filepath.Join(root, "src", "main.go"), []byte("package main\n"), 0o600))
	project, err := project.CreateWithRoot(t.Context(), server.Workspace.ProjectRegistry, root)
	testutil.FailErr(t, "create project", err)
	physical, err := server.Workspace.ProjectRegistry.Get(t.Context(), project.ID)
	testutil.FailErr(t, "resolve workspace", err)
	stream, unsubscribe, err := hub.Subscribe(t.Context(), events.Subscription{Project: project.ID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe to project events", err)
	defer unsubscribe()

	request := wire.SourceTreeViewCreate{Kind: "tree", ClientID: "window:main", OperationID: uuid.NewString(), WorkspaceID: physical.WorkspaceID()}
	created := readSourceViewResponse(t, callSourceViewHandler(t, server.Views.HandleCreateSourceView, project.ID, "", request), http.StatusCreated).Tree
	ready := func() *wire.SourceTreeView {
		t.Helper()
		var state *wire.SourceTreeView
		testutil.WaitFor(t, 10*time.Second, func() bool {
			state = readSourceViewResponse(t, callSourceViewHandler(t, server.Views.HandleGetSourceView, project.ID, created.ID, nil), http.StatusOK).Tree
			return state.State == "ready"
		})
		return state
	}
	awaitEvent := func(wantProjection string) wire.SourceViewEvent {
		t.Helper()
		for {
			select {
			case envelope := <-stream:
				if envelope.Topic != wire.EventTopicSourceView {
					continue
				}
				var event wire.SourceViewEvent
				testutil.FailErr(t, "decode source view event", json.Unmarshal(envelope.Data, &event))
				if event.ViewID != created.ID {
					continue
				}
				if wantProjection != "" && event.ProjectionRevision != wantProjection {
					continue
				}
				return event
			case <-t.Context().Done():
				t.Fatal("timed out waiting for source view event")
				return wire.SourceViewEvent{}
			}
		}
	}
	settled := ready()
	awaitEvent(settled.ProjectionRevision)
drainSettled:
	for {
		select {
		case <-stream:
		default:
			break drainSettled
		}
	}

	view, release, err := server.Views.sourceViewRegistry().registry.Acquire(pagedview.Scope{Person: testutil.HostOwner().ID, Project: project.ID}, created.ID)
	testutil.FailErr(t, "pin tree view", err)
	defer release()
	for range 5 {
		server.Views.publishSourceView(view)
		view.notifier.Notify(true)
	}
checkUnchanged:
	for {
		select {
		case envelope := <-stream:
			if envelope.Topic != wire.EventTopicSourceView {
				continue
			}
			var event wire.SourceViewEvent
			testutil.FailErr(t, "decode source view event", json.Unmarshal(envelope.Data, &event))
			if event.ViewID == created.ID {
				t.Fatalf("unchanged snapshot announced: %+v", event)
			}
		default:
			break checkUnchanged
		}
	}

	update := wire.SourceTreeViewUpdate{Kind: "tree", OperationID: uuid.NewString(), ExpectedIntentRevision: settled.IntentRevision,
		Command: wire.SourceTreeCommand{Disclose: &wire.SourceTreeDisclose{Kind: "disclose", Disclosures: []wire.SourceTreeDisclosure{{Address: wire.SourceTreeAddress{RootID: project.Roots[0].ID, Path: "src"}, Open: true}}}}}
	readSourceViewResponse(t, callSourceViewHandler(t, server.Trees.HandleApplySourceViewIntent, project.ID, created.ID, update), http.StatusOK)
	event := awaitEvent("")
	if event.IntentRevision == settled.IntentRevision {
		t.Fatalf("intent change event=%+v", event)
	}
}
