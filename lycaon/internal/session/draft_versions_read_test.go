package session

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestMessageCarriesVersionCount(t *testing.T) {
	store := store.NewMemory()
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{ProjectID: testdbseed.DefaultProjectID}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create", err)

	slotID := "slot-1"
	if _, err := store.AppendDraftVersion(ctx, sess.ID, slotID, "v0", "CODE_A"); err != nil {
		testutil.FailErr(t, "AppendDraftVersion", err)
	}
	msg := api.Message{
		ID:                slotID,
		Role:              api.MessageRoleAssistant,
		Content:           "final",
		Kind:              api.MessageKindDraft,
		DraftStatus:       api.DraftStatusCommitted,
		DraftVersionCount: 2,
		CreatedAt:         time.Now().UTC(),
	}
	if err := store.AppendMessages(ctx, sess.ID, msg); err != nil {
		testutil.FailErr(t, "AppendMessages", err)
	}
	msgs, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages", err)
	if len(msgs) != 1 || msgs[0].DraftVersionCount != 2 {
		t.Fatalf("messages = %+v want draft_version_count=2", msgs)
	}
}

func TestDraftVersionsEndpointOrder(t *testing.T) {
	store := store.NewMemory()
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{ProjectID: testdbseed.DefaultProjectID}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create", err)
	if _, err := store.AppendDraftVersion(ctx, sess.ID, "slot-1", "v0", "A"); err != nil {
		testutil.FailErr(t, "AppendDraftVersion v0", err)
	}
	if _, err := store.AppendDraftVersion(ctx, sess.ID, "slot-1", "v1", "B"); err != nil {
		testutil.FailErr(t, "AppendDraftVersion v1", err)
	}
	if _, err := store.AppendDraftVersion(ctx, sess.ID, "slot-1", "v2", "C"); err != nil {
		testutil.FailErr(t, "AppendDraftVersion v2", err)
	}
	got, err := store.ListDraftVersions(ctx, sess.ID, "slot-1")
	testutil.FailErr(t, "ListDraftVersions", err)
	if len(got) != 3 || got[0].Body != "v0" || got[2].OutcomeCode != "C" {
		t.Fatalf("versions = %+v", got)
	}
}

func TestDraftNeverEmitsDeleteOpForCoordinatorDraftSlot(t *testing.T) {
	store := store.NewMemory()
	hub := events.NewMemoryHub()
	pub := &events.Publisher{Hub: hub}
	mgr := NewManager(store, nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	mgr.SetEventPublisher(pub)
	projectID := attachTestProject(t, mgr)

	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{ProjectID: projectID}, projectID)
	testutil.FailErr(t, "Create", err)

	slotID := "draft-slot-1"
	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch, unsub, err := hub.Subscribe(subCtx, events.Subscription{Project: sess.ProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "Subscribe", err)
	defer unsub()

	placeholder := api.Message{
		ID:          slotID,
		Role:        api.MessageRoleAssistant,
		Content:     "attempt one",
		Kind:        api.MessageKindDraft,
		DraftStatus: api.DraftStatusLive,
		Visibility:  api.MessageVisibilityInternal,
		CreatedAt:   time.Now().UTC(),
	}
	if err := store.AppendMessages(ctx, sess.ID, placeholder); err != nil {
		testutil.FailErr(t, "AppendMessages", err)
	}
	if _, err := store.AppendDraftVersion(ctx, sess.ID, slotID, "attempt one", "SYNTH_HANDLE_NOT_IN_LEGS"); err != nil {
		testutil.FailErr(t, "AppendDraftVersion", err)
	}
	reset := placeholder
	reset.Content = ""
	if err := mgr.Transcript.Update(ctx, sess.ID, slotID, reset); err != nil {
		testutil.FailErr(t, "updateMessage", err)
	}

	pub.FlushMessagePatches(ctx, sess.ID)
	testutil.FailErr(t, "drain publisher", pub.Close(testutil.BoundedContext(t, 5*time.Second)))
	hub.FlushDebounced()
	patches := 0
	for {
		select {
		case env, ok := <-ch:
			if !ok {
				t.Fatal("hub closed before draft events were inspected")
			}
			if env.Topic != api.EventTopicMessage {
				continue
			}
			var ev api.MessageEvent
			testutil.FailErr(t, "decode draft event", json.Unmarshal(env.Data, &ev))
			if ev.Message.ID != slotID {
				continue
			}
			if ev.Op != api.MessageChangePatch {
				t.Fatalf("draft slot emitted unexpected op %q: %+v", ev.Op, ev)
			}
			patches++
		default:
			if patches != 1 {
				t.Fatalf("draft slot emitted %d patches, want 1", patches)
			}
			return
		}
	}
}

func TestSQLStoreDraftVersionsUnknownSlotEmpty(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "draft-versions-read.db")

	store := store.NewSQL(sqlDB)
	ctx := context.Background()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())
	sess, err := store.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create", err)

	if _, err := store.AppendDraftVersion(ctx, sess.ID, "slot-1", "body", "CODE"); err != nil {
		testutil.FailErr(t, "AppendDraftVersion", err)
	}
	got, err := store.ListDraftVersions(ctx, sess.ID, "unknown-slot")
	testutil.FailErr(t, "ListDraftVersions unknown slot", err)
	if len(got) != 0 {
		t.Fatalf("unknown slot versions = %+v want empty", got)
	}
}
