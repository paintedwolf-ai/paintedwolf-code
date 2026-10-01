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
	project, err := project.CreateWithRoot(t.Context(), server.ProjectRegistry, root)
	testutil.FailErr(t, "create project", err)
	physical, err := server.ProjectRegistry.Get(t.Context(), project.ID)
	testutil.FailErr(t, "resolve workspace", err)
	stream, unsubscribe, err := hub.Subscribe(t.Context(), events.Subscription{Project: project.ID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe to project events", err)
	defer unsubscribe()

	request := wire.SourceTreeViewCreate{Kind: "tree", ClientID: "window:main", OperationID: uuid.NewString(), WorkspaceID: physical.WorkspaceID()}
	created := readSourceViewResponse(t, callSourceViewHandler(t, server.HandleCreateSourceView, project.ID, "", request), http.StatusCreated).Tree
	ready := func() *wire.SourceTreeView {
		t.Helper()
		var state *wire.SourceTreeView
		testutil.WaitFor(t, 10*time.Second, func() bool {
			state = readSourceViewResponse(t, callSourceViewHandler(t, server.HandleGetSourceView, project.ID, created.ID, nil), http.StatusOK).Tree
			return state.State == "ready"
		})
		return state
	}
	next := func(quiet time.Duration) (wire.SourceViewEvent, bool) {
		t.Helper()
		timer := time.NewTimer(quiet)
		defer timer.Stop()
		for {
			select {
			case envelope := <-stream:
				if envelope.Topic != wire.EventTopicSourceView {
					continue
				}
				var event wire.SourceViewEvent
				testutil.FailErr(t, "decode source view event", json.Unmarshal(envelope.Data, &event))
				if event.ViewID == created.ID {
					return event, true
				}
			case <-timer.C:
				return wire.SourceViewEvent{}, false
			}
		}
	}
	settled := ready()
	for {
		if _, announced := next(time.Second); !announced {
			break
		}
	}

	view, release, err := server.sourceViewRegistry().registry.Acquire(pagedview.Scope{Person: testutil.HostOwner().ID, Project: project.ID}, created.ID)
	testutil.FailErr(t, "pin tree view", err)
	defer release()
	for range 5 {
		view.notifier.Notify(true)
		time.Sleep(20 * time.Millisecond)
	}
	if event, announced := next(time.Second); announced {
		t.Fatalf("unchanged snapshot announced: %+v", event)
	}

	update := wire.SourceTreeViewUpdate{Kind: "tree", OperationID: uuid.NewString(), ExpectedIntentRevision: settled.IntentRevision,
		Command: wire.SourceTreeCommand{Disclose: &wire.SourceTreeDisclose{Kind: "disclose", Disclosures: []wire.SourceTreeDisclosure{{Address: wire.SourceTreeAddress{RootID: project.Roots[0].ID, Path: "src"}, Open: true}}}}}
	readSourceViewResponse(t, callSourceViewHandler(t, server.HandleApplySourceViewIntent, project.ID, created.ID, update), http.StatusOK)
	event, announced := next(10 * time.Second)
	if !announced || event.IntentRevision == settled.IntentRevision {
		t.Fatalf("intent change announced=%v event=%+v", announced, event)
	}
}
