package workeroutcomes

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/pkg/api"
)

type FinalizePipeline interface {
	AnchorEnforced(string) bool
	EvaluateBlock(context.Context, string, *oar.GuardContext) (*oar.PipelineResult, error)
}
type SessionFacts interface {
	FillSessionFacts(context.Context, *oar.GuardContext, *api.Session, string, map[string]any)
}
type PolicyFeedback interface {
	RenderResult(context.Context, string, *oar.PipelineResult) (*guidance.Refusal, bool, error)
}
type SessionReader interface {
	Get(context.Context, string) (*api.Session, error)
}
type ProfileReader interface {
	PromptToolProfile(context.Context, *api.Session) (string, error)
}

type DeliveryPolicy struct {
	pipeline FinalizePipeline
	facts    SessionFacts
	feedback PolicyFeedback
	sessions SessionReader
	profiles ProfileReader
}

type DeliveryPolicyPorts struct {
	Pipeline FinalizePipeline
	Facts    SessionFacts
	Feedback PolicyFeedback
	Sessions SessionReader
	Profiles ProfileReader
}

func NewDeliveryPolicy(ports DeliveryPolicyPorts) *DeliveryPolicy {
	return &DeliveryPolicy{pipeline: ports.Pipeline, facts: ports.Facts, feedback: ports.Feedback, sessions: ports.Sessions, profiles: ports.Profiles}
}
func (m *DeliveryPolicy) SetPipeline(pipeline FinalizePipeline) { m.pipeline = pipeline }

type DeliveryResult struct {
	Envelope workercompletion.WorkerCompletionEnvelope
	Blocked  bool
}

// [OAR-PROF-10] The final envelope crosses policy once before parent delivery.
// Provisional report checks have their own native anchor and cannot replace it.
func (m *DeliveryPolicy) Evaluate(ctx context.Context, env workercompletion.WorkerCompletionEnvelope) (DeliveryResult, error) {
	out := DeliveryResult{Envelope: env}
	if env.State == string(api.WorkerSummaryStatusNeedsDecision) || m.pipeline == nil || !m.pipeline.AnchorEnforced(oar.AnchorWorkerFinalize) {
		return out, nil
	}
	if m.facts == nil || m.feedback == nil {
		return out, fmt.Errorf("worker delivery policy services unavailable")
	}
	gc := oar.NewGuardContext()
	m.facts.FillSessionFacts(ctx, gc, nil, "", nil)
	gc.SessionID = env.ChildSessionID
	gc.Profile = "worker"
	gc.WorkerLeg = true
	m.registerWorkerSessionFacts(ctx, gc, env.ChildSessionID)
	gc.WorkerSummaryPresent = strings.TrimSpace(env.Summary) != ""
	gc.SetContentSegments([]oar.ContentSegment{{
		Content: workercompletion.FormatWorkerCompletionEnvelope(env), Role: "assistant", Origin: "peer",
		Authority: "none", TrustTier: "untrusted", Source: env.ChildSessionID,
	}})
	res, err := m.pipeline.EvaluateBlock(ctx, oar.AnchorWorkerFinalize, gc)
	if err != nil {
		return out, fmt.Errorf("evaluate worker delivery: %w", err)
	}
	if res == nil || res.Decision == nil {
		return out, nil
	}
	feedback, blocked, err := m.feedback.RenderResult(ctx, oar.AnchorWorkerFinalize, res)
	if err != nil {
		return out, fmt.Errorf("render worker delivery policy: %w", err)
	}
	text := res.Decision.Code
	if feedback != nil && feedback.Body != "" {
		text = feedback.Body
	}
	if blocked {
		out.Blocked = true
		out.Envelope = workercompletion.WorkerCompletionEnvelope{
			JobID: env.JobID, ChildSessionID: env.ChildSessionID, AgentType: env.AgentType,
			State: string(api.WorkerSummaryStatusPartial), MergeStatus: env.MergeStatus,
			HintCode: res.Decision.Code, Summary: text, Digest: text,
		}
	} else if res.Decision.Effect == oar.EffectWarn || res.Decision.Effect == oar.EffectNudge {
		out.Envelope.Summary = strings.TrimSpace(env.Summary + "\n" + text)
		out.Envelope.Digest = strings.TrimSpace(env.Digest + "\n" + text)
	}
	return out, nil
}

// [OAR-FACT-11] Stored session metadata is read only by a selected fact consumer.
func (m *DeliveryPolicy) registerWorkerSessionFacts(ctx context.Context, gc *oar.GuardContext, sessionID string) {
	var once sync.Once
	var sessionErr error
	var workerSession *api.Session
	bindSession := func(gc *oar.GuardContext) error {
		once.Do(func() {
			if m.sessions == nil {
				return
			}
			sess, err := m.sessions.Get(ctx, sessionID)
			if err != nil {
				sessionErr = err
				return
			}
			if sess == nil {
				sessionErr = fmt.Errorf("[OAR-FACT-26] worker session %s unavailable", sessionID)
				return
			}
			workerSession = sess
			m.facts.FillSessionFacts(ctx, gc, sess, "", nil)
		})
		return sessionErr
	}
	for _, name := range []string{"session_posture", "principal", "principal_roles"} {
		if name == "principal" && gc.Principal != "" {
			continue
		}
		if name == "principal_roles" && len(gc.PrincipalRoles) != 0 {
			continue
		}
		gc.RegisterProvider(name, bindSession)
	}
	gc.RegisterProvider("permission_profile", func(gc *oar.GuardContext) error {
		if err := bindSession(gc); err != nil {
			return err
		}
		if workerSession == nil {
			return nil
		}
		profile, err := m.profiles.PromptToolProfile(ctx, workerSession)
		gc.PermissionProfile = profile
		return err
	})
}
