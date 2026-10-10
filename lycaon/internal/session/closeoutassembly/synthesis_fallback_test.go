package closeoutassembly

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

type readySynthesis struct{}

func (readySynthesis) ForSession(context.Context, *api.Session) surface.ImplementSessionState {
	return surface.ImplementSessionState{WrapupGatesLoaded: true, BatchReadyForSynthesis: true}
}

type retainedScaffold struct{}

func (retainedScaffold) CurrentPhase(context.Context, string) string { return "work" }
func (retainedScaffold) ScaffoldVarsForSession(context.Context, string) (map[string]any, error) {
	return map[string]any{"evidence_digest": " Retained grounded summary. "}, nil
}

type forbiddenEmptyCuration struct{ t *testing.T }

func (c forbiddenEmptyCuration) Curate(context.Context, evidence.Ledger, llm.CurationFocus, int) (llm.CurationResult, error) {
	c.t.Error("curator invoked without source messages")
	return llm.CurationResult{}, errors.New("no evidence")
}

func TestSynthesisAssemblyPreservesRetainedDigestWhenTranscriptIsEmpty(t *testing.T) {
	s := New(closeoutSession{}, nil, readySynthesis{}, nil)
	s.SetWorkflow(&WorkflowDomains{Policy: retainedScaffold{}})
	s.SetSynthesisCurator(forbiddenEmptyCuration{t})
	got := s.SynthesisEvidenceForAssembly(t.Context(), &api.Session{ID: "parent"}, "implement_synthesis")
	if got != "Retained grounded summary." {
		t.Fatalf("fallback evidence=%q", got)
	}
	if got = s.SynthesisEvidenceForAssembly(t.Context(), &api.Session{ID: "child", ParentSessionID: "parent"}, "implement_synthesis"); got != "" {
		t.Fatalf("coordinator digest leaked into worker: %q", got)
	}
}
