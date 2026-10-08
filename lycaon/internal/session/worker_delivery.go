package session

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/pkg/api"
)

type workerDelivery struct {
	envelope WorkerCompletionEnvelope
	blocked  bool
}

// [OAR-PROF-10] The final envelope crosses policy once before parent delivery.
// Provisional report checks have their own native anchor and cannot replace it.
func (m *Manager) evaluateWorkerDelivery(ctx context.Context, env WorkerCompletionEnvelope) (workerDelivery, error) {
	out := workerDelivery{envelope: env}
	if env.State == string(api.WorkerSummaryStatusNeedsDecision) || m.oarPipeline == nil || !m.oarPipeline.AnchorEnforced(oar.AnchorWorkerFinalize) {
		return out, nil
	}
	gc := oar.NewGuardContext()
	m.fillOARSessionFacts(ctx, gc, nil, "", nil)
	gc.Session.SessionID = env.ChildSessionID
	gc.Session.Profile = "worker"
	gc.Session.WorkerLeg = true
	m.registerWorkerSessionFacts(ctx, gc, env.ChildSessionID)
	gc.Grounding.WorkerSummaryPresent = strings.TrimSpace(env.Summary) != ""
	gc.SetContentSegments([]oar.ContentSegment{{
		Content: FormatWorkerCompletionEnvelope(env), Role: "assistant", Origin: "peer",
		Authority: "none", TrustTier: "untrusted", Source: env.ChildSessionID,
	}})
	res, err := m.oarPipeline.EvaluateBlock(ctx, oar.AnchorWorkerFinalize, gc)
	if err != nil {
		return out, fmt.Errorf("evaluate worker delivery: %w", err)
	}
	if res == nil || res.Decision == nil {
		return out, nil
	}
	feedback, blocked, err := m.renderOARResult(ctx, oar.AnchorWorkerFinalize, res)
	if err != nil {
		return out, fmt.Errorf("render worker delivery policy: %w", err)
	}
	text := res.Decision.Code
	if feedback != nil && feedback.Body != "" {
		text = feedback.Body
	}
	if blocked {
		out.blocked = true
		out.envelope = WorkerCompletionEnvelope{
			JobID: env.JobID, ChildSessionID: env.ChildSessionID, AgentType: env.AgentType,
			State: string(api.WorkerSummaryStatusPartial), MergeStatus: env.MergeStatus,
			HintCode: res.Decision.Code, Summary: text, Digest: text,
		}
	} else if res.Decision.Effect == oar.EffectWarn || res.Decision.Effect == oar.EffectNudge {
		out.envelope.Summary = strings.TrimSpace(env.Summary + "\n" + text)
		out.envelope.Digest = strings.TrimSpace(env.Digest + "\n" + text)
	}
	return out, nil
}

// [OAR-FACT-11] Stored session metadata is read only by a selected fact consumer.
func (m *Manager) registerWorkerSessionFacts(ctx context.Context, gc *oar.GuardContext, sessionID string) {
	var once sync.Once
	var sessionErr error
	var workerSession *api.Session
	bindSession := func(gc *oar.GuardContext) error {
		once.Do(func() {
			if m.store == nil {
				return
			}
			sess, err := m.store.Get(ctx, sessionID)
			if err != nil {
				sessionErr = err
				return
			}
			if sess == nil {
				sessionErr = fmt.Errorf("[OAR-FACT-26] worker session %s unavailable", sessionID)
				return
			}
			workerSession = sess
			m.fillOARSessionFacts(ctx, gc, sess, "", nil)
		})
		return sessionErr
	}
	for _, name := range []string{"session_posture", "principal", "principal_roles"} {
		if name == "principal" && gc.Session.Principal != "" {
			continue
		}
		if name == "principal_roles" && len(gc.Session.PrincipalRoles) != 0 {
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
		profile, err := m.promptToolProfile(ctx, workerSession)
		gc.Session.PermissionProfile = profile
		return err
	})
}
