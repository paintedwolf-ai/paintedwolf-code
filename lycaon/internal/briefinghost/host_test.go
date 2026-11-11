package briefinghost

import (
	"context"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/briefing"
	"github.com/lycaon/lycaon/internal/briefingstore"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type published struct {
	mu     sync.Mutex
	events []wire.BriefingEvent
}

func (p *published) Publish(_ context.Context, topic wire.EventTopic, _ events.PublishKey, data any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if topic == wire.EventTopicBriefing {
		p.events = append(p.events, data.(wire.BriefingEvent))
	}
	return nil
}

func (p *published) all() []wire.BriefingEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]wire.BriefingEvent(nil), p.events...)
}

type fixedText struct{}

func (fixedText) Generate(context.Context, briefing.Invocation) (string, string, string, error) {
	return "Rebuilt from a clean folder.", "provider", "model", nil
}

type activityProfile struct{}

func (activityProfile) ID() string              { return "tool_activity" }
func (activityProfile) Version() string         { return "1" }
func (activityProfile) Policy() briefing.Policy { return briefing.QueuedPolicy }
func (activityProfile) Resolve(context.Context, briefing.Target) (briefing.Request, error) {
	return briefing.Request{}, nil
}

func request(projectID string) briefing.Request {
	manifest := briefing.Manifest{ProfileID: "tool_activity", ProfileVersion: "1", SectionID: "change", Template: briefing.Template{ID: "change", Version: "1"}, Contract: briefing.OneSentence,
		Items: []briefing.Item{{Key: "receipt", Role: briefing.RoleObservation, SourceKind: "receipt", Inclusion: briefing.IncludedWhole, Text: "go build failed"}}}
	section := briefing.SectionPlan{ID: "change", Mode: briefing.Generated, Manifest: manifest, Prompt: briefing.Prompt{System: "s", User: "u", MaxTokens: 40}}
	return briefing.Request{ProjectID: projectID, ProfileID: "tool_activity", Version: "1", TargetKey: "card", TargetKind: "activity", Policy: briefing.QueuedPolicy, Sections: []briefing.SectionPlan{section}}
}

func TestHostPublishesSnapshotsAndResetsOnPurge(t *testing.T) {
	database := testdbfixture.Open(t, "briefing-host.db")
	project := testdbseed.DefaultProjectID
	testdbseed.InsertProjectRoot(t, database, project, t.TempDir())
	host := New(context.Background(), database, t.TempDir(), fixedText{})
	t.Cleanup(func() { _ = host.Close(context.Background()) })
	testutil.FailErr(t, "register", host.Registry.Register(activityProfile{}))
	hub := &published{}
	host.Publish(hub, nil)

	_, err := host.Engine.Request(t.Context(), request(project))
	testutil.FailErr(t, "request", err)
	host.Scheduler.Wait(t.Context())
	sent := hub.all()
	if len(sent) != 1 || sent[0].Kind != "snapshot" || sent[0].Snapshot.Result == nil || sent[0].Snapshot.Result.Summary[0].Text != "Rebuilt from a clean folder." {
		t.Fatalf("published %+v", sent)
	}

	testutil.FailErr(t, "purge", host.Purge(t.Context(), hub))
	sent = hub.all()
	if reset := sent[len(sent)-1]; reset.Kind != "reset" || reset.CacheEpoch != "2" || len(reset.ProfileIds) != 1 || reset.ProfileIds[0] != "tool_activity" {
		t.Fatalf("reset %+v", reset)
	}
	snapshot, err := host.Store.Snapshot(t.Context(), snapshotQuery(project))
	testutil.FailErr(t, "snapshot after purge", err)
	if snapshot.Status != "absent" || snapshot.CacheEpoch != "2" {
		t.Fatalf("purged target survived: %+v", snapshot)
	}
}

func snapshotQuery(project string) briefingstore.SnapshotQuery {
	return briefingstore.SnapshotQuery{ProjectID: project, ProfileID: "tool_activity", Version: "1", Policy: briefing.QueuedPolicy, TargetKey: "card"}
}
