package promptloop_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/pkg/api"
)

// A refused citation on the turn's last iteration was once a lost answer: the repair
// kick went out, no turn was left to read it, and the run failed with no message.
func TestFinalTurnRefusalStillDeliversTheAnswer(t *testing.T) {
	const statement = "The limiter reads left outside its lock"
	for _, tc := range []struct {
		name, response string
		code           string
		present        string
	}{
		{"invalid citation", statement + " [read#99:2].", guidance.SynthHandleNotInLegsCode, statement + "."},
		{"unverifiable range", statement + " [a.go:2-9].", guidance.SynthCitationUnverifiableCode, statement + "."},
		{"presentation refusal", statement + " [read#1:2].\n\n![chart](https://example.test/unseen.png)", guidance.PresentImageUnobservedCode, statement},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newMarkdownRun(t, &modelcall.Completion{Content: tc.response})
			r.deps.Context.Limits = loopTestLimits(1)
			var ledger = r.deps.Closeout.EvidenceLedger.(markdownRunLedger).ledger
			r.deps.Closeout.AssembleLedgerCloseout = func(_ context.Context, _, surface string, codes []string, draft guidance.CloseoutDraft, retries int) (guidance.CoordinatorCompletionReport, *api.CitationGrounding) {
				return guidance.AssembleRetainedCloseout(evidence.CitationRoots{}, surface, guidance.CloseoutEvidence{Ledger: ledger}, draft, nil, codes[len(codes)-1], retries)
			}
			evaluate := r.deps.Closeout.EvaluateCloseoutBlock
			r.deps.Closeout.EvaluateCloseoutBlock = func(ctx context.Context, sess *api.Session, gc *oar.GuardContext) (*oar.Decision, error) {
				if gc.Grounding.CitationUnverifiable {
					return &oar.Decision{Code: guidance.SynthCitationUnverifiableCode, Effect: oar.EffectBlock, Data: gc.RejectData[guidance.SynthCitationUnverifiableCode]}, nil
				}
				return evaluate(ctx, sess, gc)
			}
			result, message := r.run(t, []api.Message{{ID: "u1", Role: api.MessageRoleUser, Content: "Is the limiter safe?"}})
			if !strings.Contains(result.LastAssistantContent, tc.present) {
				t.Fatalf("delivered %q, want it to hold %q", result.LastAssistantContent, tc.present)
			}
			if message.Kind != api.MessageKindCompletionReport || message.Grounding == nil || !message.Grounding.HostAssembled || message.Grounding.Traced || message.Grounding.HintCode != tc.code {
				t.Fatalf("metadata is not honest about assembly: kind=%s grounding=%+v", message.Kind, message.Grounding)
			}
		})
	}
}

// With a turn left, the same refusal still earns the repair.
func TestRefusalWithATurnLeftStillRepairs(t *testing.T) {
	r := newMarkdownRun(t, &modelcall.Completion{Content: "It holds [read#99:2]."}, &modelcall.Completion{Content: "[cite: c1 => read#1:2]"})
	r.deps.Context.Limits = loopTestLimits(2)
	result, message := r.run(t, []api.Message{{ID: "u1", Role: api.MessageRoleUser, Content: "Does it hold?"}})
	if message.Grounding == nil || message.Grounding.HostAssembled || !strings.Contains(result.LastAssistantContent, "a.go:2") {
		t.Fatalf("repair turn did not land: %q grounding=%+v", result.LastAssistantContent, message.Grounding)
	}
}
