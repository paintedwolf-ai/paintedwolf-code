package workflow

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type fanoutMessageStore struct {
	msgs []api.Message
}

func (s fanoutMessageStore) GetMessages(_ context.Context, _ string) ([]api.Message, error) {
	return s.msgs, nil
}

func TestStampFanoutExecuteOutputMergesTopology(t *testing.T) {
	block := "worker body for topology merge"
	envelope := session.FormatWorkerCompletionEnvelope(session.WorkerCompletionEnvelope{
		JobID:     "job-1",
		AgentType: "scout",
		State:     "complete",
		Body:      block,
	})
	msgs := []api.Message{{
		Role: api.MessageRoleTool,
		WorkerSummary: &api.WorkerSummaryMeta{
			WorkerID:  "job-1",
			AgentType: "scout",
			Status:    api.WorkerSummaryStatusComplete,
			Envelope:  envelope,
		},
		Content: envelope,
	}}
	vars := StampFanoutExecuteOutput(context.Background(), fanoutMessageStore{msgs: msgs}, "sess-1", map[string]any{})
	if _, ok := vars["evidence_digest"]; ok {
		t.Fatalf("stamp must not curate: evidence_digest = %v", vars["evidence_digest"])
	}
	out, ok := vars["topology_outputs"].(map[string]any)
	if !ok {
		t.Fatalf("topology_outputs missing: %#v", vars)
	}
	fanOut, _ := out["fan_out"].(string)
	if !strings.Contains(fanOut, "scout") || !strings.Contains(fanOut, block) {
		t.Fatalf("fan_out = %q", fanOut)
	}
}

func TestKickRenderIncludesEvidenceDigest(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	digest := "## Evidence digest (curated)\n[grep#1] routes.go:4 — GET /health\nselected/total: 1/2"
	rendered, err := engine.RenderKick(context.Background(), "coordinator-topology-synthesis", map[string]any{
		"evidence_digest": digest,
		"topology_output": "raw worker block",
	})
	testutil.FailErr(t, "RenderKick", err)
	if !strings.Contains(rendered, digest) {
		t.Fatalf("render missing digest:\n%s", rendered)
	}
	idxDigest := strings.Index(rendered, digest)
	idxTopology := strings.Index(rendered, "raw worker block")
	if idxDigest < 0 || idxTopology < 0 || idxDigest > idxTopology {
		t.Fatalf("digest must precede topology_output:\n%s", rendered)
	}
}

func TestReviewLoopDigestFormatters(t *testing.T) {
	cites := formatWorkerCitationDigest([]api.Message{{
		Role: api.MessageRoleTool,
		WorkerSummary: &api.WorkerSummaryMeta{
			AgentType: "security-reviewer",
			Status:    api.WorkerSummaryStatusComplete,
			Grounding: &api.CitationGrounding{
				CitedEvidence: []api.CitationGroundingCitedEvidence{{
					Path: "auth.go", Line: 12, Excerpt: "db.Query",
				}},
			},
		},
	}})
	if !strings.Contains(cites, "security-reviewer") || !strings.Contains(cites, "auth.go:12") {
		t.Fatalf("citation digest = %q", cites)
	}
}
