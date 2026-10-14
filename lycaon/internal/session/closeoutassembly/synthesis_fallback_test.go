package closeoutassembly

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/verification"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"path/filepath"
	"strings"
	"testing"
)

type readySynthesis struct{}

func (readySynthesis) ForSession(context.Context, *api.Session) surface.ImplementSessionState {
	return surface.ImplementSessionState{WrapupGatesLoaded: true, BatchReadyForSynthesis: true}
}

type observedSourceChecks struct {
	checks *verification.SourceCheckObservations
}

func (s observedSourceChecks) SourceObservations(context.Context, *api.Session, []api.Message) *verification.SourceCheckObservations {
	return s.checks
}

func TestSynthesisAssemblyProjectsHistoricalChecksWithoutChecklist(t *testing.T) {
	registry, err := anchor.LoadRegistryFromConfigRoot()
	testutil.FailErr(t, "load OAR bindings", err)
	previous := anchor.DefaultRegistry()
	anchor.SetDefaultRegistry(registry)
	t.Cleanup(func() { anchor.SetDefaultRegistry(previous) })
	s := New(closeoutSession{}, nil, readySynthesis{}, nil)
	s.SetPrompts(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: filepath.Join(testutil.CheckoutRoot(t), "lycaon")}))
	for _, match := range []verification.SourceMatch{verification.SourceCurrent, verification.SourceChanged, verification.SourceUnknown} {
		t.Run(string(match), func(t *testing.T) {
			s.SetSourceVerification(observedSourceChecks{&verification.SourceCheckObservations{
				Checks: []verification.SourceCheckObservation{{
					CheckID: "internal-check-id", SourceRevision: "internal-source-hash", RootDigest: "internal-root-hash",
					Command: "go test ./parser", Cwd: "module", Verdict: "passed", Match: match, PriorIntent: true,
				}},
			}})
			got := s.SynthesisEvidenceForAssembly(t.Context(), &api.Session{ID: "parent"}, "implement_investigate")
			for _, want := range []string{`"command":"go test ./parser"`, `"cwd":"module"`, `"recorded_verdict":"passed"`, `"source_match":"` + string(match) + `"`, `"prior_user_intent":true`, `"required_by_workflow":false`, "A historical pass does not prove changed source passes"} {
				if !strings.Contains(got, want) {
					t.Fatalf("assembled OAR projection lacks %q: %s", want, got)
				}
			}
			for _, hidden := range []string{"internal-check-id", "internal-source-hash", "internal-root-hash", `"check_id"`, `"recorded_source_revision"`, `"recorded_root_digest"`} {
				if strings.Contains(got, hidden) {
					t.Fatalf("assembled OAR projection exposed internal identity %q: %s", hidden, got)
				}
			}
		})
	}
	s.SetSourceVerification(observedSourceChecks{})
	if got := s.SynthesisEvidenceForAssembly(t.Context(), &api.Session{ID: "parent"}, "implement_investigate"); got != "" {
		t.Fatalf("no recorded checks emitted standing guidance: %s", got)
	}
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

func TestExecutionObservationsKeepChecklistSeparateFromSourceProof(t *testing.T) {
	checklist := func(content string) api.Message {
		return api.Message{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Tool: "update_progress", Outcome: "completed", ToolArgs: map[string]any{"content": content}}}
	}
	messages := []api.Message{checklist("old"), {Role: api.MessageRoleUser, Content: "new request"}, checklist("- [x] Tests pass"), {Role: api.MessageRoleAssistant, Content: "Verified"}}
	checks := &verification.SourceCheckObservations{Checks: []verification.SourceCheckObservation{{Verdict: "passed", Match: verification.SourceChanged, PriorIntent: true}}}
	var out executionObservations
	testutil.FailErr(t, "decode observations", json.Unmarshal([]byte(executionObservationJSON(messages, checks)), &out))
	if out.ModelChecklist != "- [x] Tests pass" || out.SourceChecks.Checks[0].Match != verification.SourceChanged {
		t.Fatalf("checklist overrode source facts: %+v", out)
	}
	if got := executionObservationJSON(messages, nil); got != "" {
		t.Fatalf("checklist invented execution evidence: %s", got)
	}
	messages = append(messages, api.Message{Role: api.MessageRoleUser, Content: "another task"})
	var later executionObservations
	testutil.FailErr(t, "decode fresh later observations", json.Unmarshal([]byte(executionObservationJSON(messages, checks)), &later))
	if later.ModelChecklist != "" || !later.SourceChecks.Checks[0].PriorIntent {
		t.Fatalf("lost historical distinction: %+v", later)
	}
}
