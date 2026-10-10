package policyfacts

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/secretmint"
	sessiontree "github.com/lycaon/lycaon/internal/session/tree"
	"github.com/lycaon/lycaon/pkg/api"
)

// HarvestedFingerprint reports whether a fingerprint is in the harvest set.
type HarvestedFingerprint func(rootSessionID string, fp secretmatch.SecretFingerprint) bool

// SetCredentialSlotProvider supplies recognition from the current catalog view.
func (m *Service) SetCredentialSlotProvider(provider func(context.Context, *api.Session) *secretmint.Inspector) {
	if m != nil {
		m.credentialSlots = provider
	}
}

// SetSecretFingerprinter installs the device HMAC used for harvest identity.
func (m *Service) SetSecretFingerprinter(fp *secretmatch.Fingerprinter) {
	if m != nil {
		m.secretFP = fp
	}
}

// SetHarvestedFingerprint installs the harvest membership query.
func (m *Service) SetHarvestedFingerprint(fn HarvestedFingerprint) {
	if m != nil {
		m.harvestHas = fn
	}
}

func (m *Service) SetIgnoredCredentialCandidate(fn func(context.Context, string, string) bool) {
	m.ignoredCredentialCandidate = fn
}

// candidateContext publishes observations; rules own thresholds and counters.
func (m *Service) candidateContext(ctx context.Context, sess *api.Session, tool string, args map[string]any, candidate secretmint.Candidate) *oar.GuardContext {
	gc := oar.NewGuardContext()
	m.FillSessionFacts(ctx, gc, sess, tool, args)
	publish := func(name string, value any) {
		gc.RegisterProvider(name, func(gc *oar.GuardContext) error {
			if gc.Published == nil {
				gc.Published = map[string]any{}
			}
			gc.Published[name] = value
			return nil
		})
	}
	gc.FeedbackData = map[string]any{"assignment": candidate.Assignment}
	if m.secretFP != nil {
		gc.FeedbackData["subject"] = map[string]any{"kind": "credential", "id": string(m.secretFP.Fingerprint(candidate.Value))}
	}
	publish("paintedwolf.assignment", candidate.Assignment)
	ignored := m.ignoredCredentialCandidate != nil && m.ignoredCredentialCandidate(ctx, sess.ProjectID, candidate.Value)
	publish("paintedwolf.credential_ignored", ignored)
	var measurements secretmint.Measurements
	measured := false
	measurement := func(name string, get func(secretmint.Measurements) any) {
		gc.RegisterProvider(name, func(gc *oar.GuardContext) error {
			if !measured {
				measurements = candidate.Measurements()
				measured = true
			}
			if gc.Published == nil {
				gc.Published = map[string]any{}
			}
			gc.Published[name] = get(measurements)
			return nil
		})
	}
	measurement("paintedwolf.listed", func(m secretmint.Measurements) any { return m.Listed })
	measurement("paintedwolf.credential_length", func(m secretmint.Measurements) any { return m.Length })
	measurement("paintedwolf.credential_distinct_words", func(m secretmint.Measurements) any { return m.DistinctWords })
	measurement("paintedwolf.credential_longest_run", func(m secretmint.Measurements) any { return m.LongestRun })
	measurement("paintedwolf.credential_run_classes", func(m secretmint.Measurements) any { return m.RunClasses })
	fingerprint := func() (secretmatch.SecretFingerprint, error) {
		if m.secretFP == nil {
			return "", fmt.Errorf("[OAR-FACT-26] credential_fingerprint provider unavailable")
		}
		return m.secretFP.Fingerprint(candidate.Value), nil
	}
	gc.RegisterProvider("paintedwolf.credential_fingerprint", func(gc *oar.GuardContext) error {
		fp, err := fingerprint()
		if err != nil {
			return err
		}
		if gc.Published == nil {
			gc.Published = map[string]any{}
		}
		gc.Published["paintedwolf.credential_fingerprint"] = string(fp)
		return nil
	})
	gc.RegisterProvider("paintedwolf.credential_harvested", func(gc *oar.GuardContext) error {
		fp, err := fingerprint()
		if err != nil {
			return err
		}
		if m.harvestHas == nil {
			return fmt.Errorf("[OAR-FACT-26] credential_harvested provider unavailable")
		}
		root := sessiontree.RootID(ctx, m.store, sess.ID)
		if gc.Published == nil {
			gc.Published = map[string]any{}
		}
		gc.Published["paintedwolf.credential_harvested"] = m.harvestHas(root, fp)
		return nil
	})
	return gc
}

// evaluateCredentialCandidates emits one occurrence per literal assignment.
func (m *Service) evaluateCredentialCandidates(ctx context.Context, sess *api.Session, tool string, args map[string]any, output string) (string, guidance.ToolResultFacts) {
	facts := guidance.ToolResultFacts{}
	if m.credentialSlots == nil || m.Pipeline == nil || !m.Pipeline.AnchorEnforced(oar.AnchorCredentialAssignment) {
		return output, facts
	}
	inspector := m.credentialSlots(ctx, sess)
	for _, candidate := range inspector.Inspect(tool, args) {
		gc := m.candidateContext(ctx, sess, tool, args, candidate)
		gc.SetContentSegments([]oar.ContentSegment{{Content: output, Role: "tool", Origin: "tool", Authority: "none", TrustTier: "untrusted", Source: tool}})
		res, err := m.Pipeline.EvaluateBlock(ctx, oar.AnchorCredentialAssignment, gc)
		if err != nil {
			facts.ContentReplaced = true
			return "", facts.WithOutcome(api.ToolResultOutcomeError)
		}
		var contribution guidance.ToolResultFacts
		output, contribution = m.feedback.DeliverPostToolResult(ctx, oar.AnchorCredentialAssignment, output, res)
		facts = facts.Merge(contribution)
		if !facts.Succeeded() {
			break
		}
	}
	return output, facts
}
