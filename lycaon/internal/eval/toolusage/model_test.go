package toolusage

import (
	"testing"

	"github.com/lycaon/lycaon/internal/logview"
)

func TestCoordinatorModelPrefersAgentSurfaceOverAuxiliary(t *testing.T) {
	tree := &logview.SessionTree{Agents: []*logview.Agent{
		{ParentID: "coordinator", Turns: []logview.LLMRecord{{Model: "worker", ProviderID: "worker-provider", Surface: "worker"}}},
		{Turns: []logview.LLMRecord{
			{Model: "auxiliary", ProviderID: "auxiliary-provider"},
			{Model: "coordinator", ProviderID: "coordinator-provider", Surface: "investigate"},
		}},
	}}
	model, provider := coordinatorModel(tree)
	if model != "coordinator" || provider != "coordinator-provider" {
		t.Fatalf("wrong coordinator identity: model=%q provider=%q", model, provider)
	}
}

func TestCoordinatorModelKeepsProviderPairedWithItsCall(t *testing.T) {
	records := []logview.LLMRecord{
		{SessionID: "coordinator", ProviderID: "unrelated-provider"},
		{SessionID: "coordinator", Model: "coordinator", Surface: "investigate"},
	}
	p := AnalyzeCapture("replay", "", nil, records)
	if p.Model != "coordinator" || p.ProviderID != "" {
		t.Fatalf("mixed identities from separate calls: model=%q provider=%q", p.Model, p.ProviderID)
	}
}

func TestCoordinatorModelFallsBackWithoutSurfaceMetadata(t *testing.T) {
	records := []logview.LLMRecord{{SessionID: "coordinator", Model: "fallback", ProviderID: "provider"}}
	p := AnalyzeCapture("replay", "", nil, records)
	if p.Model != "fallback" || p.ProviderID != "provider" {
		t.Fatalf("lost model without surface metadata: model=%q provider=%q", p.Model, p.ProviderID)
	}
}
