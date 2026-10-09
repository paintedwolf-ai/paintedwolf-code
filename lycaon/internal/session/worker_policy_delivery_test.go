package session_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWorkerParentDeliveryPreservesFrozenDecision(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	database := testdbfixture.Open(t, "worker-feedback.db")
	mem := store.NewSQL(database)
	mgr := session.NewManager(mem, llm.NewMockProvider(&llm.MockConfig{}), tools.NewStubRegistry(), settings.DefaultSessionLimits())
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	parent, err := mem.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create parent", err)
	child, err := mem.CreateChild(t.Context(), parent, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "create child", err)
	rule := &oar.Rule{OAR: "1.0", ID: "CHANGED_POLICY", Kind: oar.KindPolicy, Anchor: oar.AnchorWorkerReportCheck, Effect: oar.EffectBlock, Enforcement: "enforce", OnError: "fail_closed", OnFire: []oar.OnFireAction{oar.OnFireIncrementCounter}, Copy: oar.Copy{What: "Changed after queue commit"}}
	pipeline := oar.NewGuardPipeline(oar.NewRuleSet([]*oar.Rule{rule}), nil, oar.NewCounterStore())
	pipeline.EnableAnchor(oar.AnchorWorkerReportCheck)
	mgr.SetOARPipeline(pipeline, oar.NewRenderer(nil, nil))
	frozen := api.WorkerResult{Status: "partial", HintCode: "ORIGINAL_POLICY", Summary: "Retained work", PolicyFeedback: &api.WorkerPolicyFeedback{Code: "ORIGINAL_POLICY", Effect: "block", Copy: map[string]string{"what": "Original {{ literal }}", "cause": "Original measured facts", "why": "Preserve observation", "fix": "Inspect retained result", "instead": "Report uncertainty"}}, Grounding: &api.CitationGrounding{Traced: false}}
	data, err := json.Marshal(frozen)
	testutil.FailErr(t, "persist result", err)
	var result api.WorkerResult
	testutil.FailErr(t, "reload result", json.Unmarshal(data, &result))
	status, err := mgr.Workers.Results.ProjectResult(t.Context(), workeroutcomes.SummaryInput{JobID: "job-feedback", ChildSessionID: child.ID, ParentSessionID: parent.ID, AgentType: "implementer"}, result)
	testutil.FailErr(t, "project frozen result", err)
	if status != "partial" {
		t.Fatalf("partial result promoted to %s", status)
	}
	if n := pipeline.Counters().Get(child.ID, rule.Qualified(), oar.CounterFire); n != 0 {
		t.Fatalf("parent generated %d new policy occurrences", n)
	}
	messages, err := mem.GetMessages(t.Context(), parent.ID)
	testutil.FailErr(t, "read parent projection", err)
	found := false
	for _, message := range messages {
		if strings.Contains(message.Content, "Original {{ literal }}") && strings.Contains(message.Content, "ORIGINAL_POLICY") {
			found = true
		}
		if strings.Contains(message.Content, "CHANGED_POLICY") {
			t.Fatal("parent substituted current policy")
		}
	}
	if !found {
		t.Fatalf("frozen feedback missing in parent messages: %+v", messages)
	}
}
