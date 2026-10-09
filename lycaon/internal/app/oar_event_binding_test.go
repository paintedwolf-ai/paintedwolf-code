package app

import (
	"context"
	"encoding/json"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"testing"
	"time"
)

func TestBuildPublishesDeclaredOAREventThroughAcquiredStream(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	t.Setenv("LYCAON_API_TOKEN", "test-oar-stream")
	app, err := Build(t.Context(), testBuildConfig(t, configlayout.FindModuleRoot()))
	testutil.FailErr(t, "build host event graph", err)
	t.Cleanup(func() { _ = app.Close() })
	testdbseed.InsertProjectRoot(t, app.DB, testdbseed.DefaultProjectID, t.TempDir())
	session, err := app.Sessions.Manager.CreateForProject(t.Context(), testdbseed.DefaultProjectID, wire.SessionPostureBuild)
	testutil.FailErr(t, "create event session", err)
	stream, unsubscribe, err := app.Events.Subscribe(t.Context(), events.Subscription{Project: testdbseed.DefaultProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe host stream", err)
	defer unsubscribe()
	rule := &oar.Rule{OAR: "1.0", Namespace: "bootstrap.test", ID: "PUBLISH_EVENT", Kind: oar.KindPolicy, Anchor: oar.AnchorCoordinatorPostTurn, When: "true", Effect: oar.EffectWarn, Enforcement: "enforce", OnError: "fail_closed", OnFire: []oar.OnFireAction{oar.OnFirePublishEvent}}
	pipeline := app.Sessions.Manager.OARPipeline()
	pipeline.SetRuleSetFor(func(_ context.Context, _ string) *oar.RuleSet { return oar.NewRuleSet([]*oar.Rule{rule}) })
	facts := oar.NewGuardContext()
	facts.Session.SessionID = session.ID
	result, err := pipeline.Evaluate(t.Context(), oar.StagePostTurn, facts)
	testutil.FailErr(t, "fire declared host event", err)
	if len(result.PublishedEvents) != 1 || result.Decision == nil || result.Decision.Code != rule.ID || result.Decision.Rule != rule.Qualified() || result.Decision.Effect != oar.EffectWarn {
		t.Fatalf("declared event was not applied: %#v", result)
	}
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		select {
		case envelope := <-stream:
			if envelope.Topic != wire.EventTopicOAR {
				continue
			}
			if envelope.Scope.ProjectID != testdbseed.DefaultProjectID || envelope.Scope.SessionID != session.ID {
				t.Fatalf("event scope = %#v", envelope.Scope)
			}
			var record wire.OAROnFireEvent
			testutil.FailErr(t, "decode host OAR record", json.Unmarshal(envelope.Data, &record))
			if record.Rule != rule.Qualified() || record.Anchor != rule.Anchor || record.Effect != string(rule.Effect) {
				t.Fatalf("host record = %#v", record)
			}
			select {
			case extra := <-stream:
				if extra.Topic == wire.EventTopicOAR {
					t.Fatalf("duplicate OAR record: %#v", extra)
				}
			default:
			}
			return
		case <-deadline.C:
			t.Fatal("declared OAR event did not reach the host stream")
		}
	}
}
