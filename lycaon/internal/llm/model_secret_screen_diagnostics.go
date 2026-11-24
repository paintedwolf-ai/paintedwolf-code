package llm

import (
	"context"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/secretmatch"
)

func runScreenMatcherPass(
	ctx context.Context,
	matcher *secretmatch.Matcher,
	callID, projectID string,
	req modelcall.CompletionRequest,
) (context.Context, modelcall.CompletionRequest, map[secretmatch.SecretFingerprint]bool, []locatedModelSecret) {
	started := time.Now()
	classification := matcher.ClassificationSnapshot(ctx)
	ctx = matcher.WithClassifications(ctx, classification)
	req = redactModelRequest(ctx, matcher, req, keepOnlyReferences)
	hits := inspectModelRequest(ctx, matcher, req)
	if len(hits) > 0 && containsNonDisclosable(hits) {
		req = redactModelRequest(ctx, matcher, req, func(hit secretmatch.Match) bool {
			return hit.NonDisclosable
		})
		hits = inspectModelRequest(ctx, matcher, req)
	}
	secretmatch.EmitPhaseRecord(ctx, secretmatch.ScreenPhaseRecord{
		CallID:     callID,
		ProjectID:  projectID,
		Phase:      "matcher_redaction",
		DurationMS: time.Since(started).Milliseconds(),
		Hits:       len(hits),
	})
	return ctx, req, classification, hits
}

func emitPersonDecision(ctx context.Context, callID, projectID string, durationMS int64, hits int, decision string, failed bool) {
	secretmatch.EmitPhaseRecord(ctx, secretmatch.ScreenPhaseRecord{
		CallID:     callID,
		ProjectID:  projectID,
		Phase:      "person_decision",
		DurationMS: durationMS,
		Hits:       hits,
		Decision:   decision,
		Failed:     failed,
	})
}

func resolvePersonDecision(
	ctx context.Context,
	s *ModelSecretScreen,
	req modelcall.CompletionRequest,
	callID, projectID, rootSessionID, sessionID string,
	hits []locatedModelSecret,
	resolution secretmatch.Resolution,
	askStart time.Time,
) (modelcall.CompletionRequest, error) {
	decision := string(resolution.Decision)
	switch resolution.Decision {
	case secretmatch.SendUnchanged:
		emitPersonDecision(ctx, callID, projectID, time.Since(askStart).Milliseconds(), len(hits), decision, false)
		return req, nil
	case secretmatch.SendRedacted:
		out := redactModelRequest(ctx, s.matcher, req, nil)
		emitPersonDecision(ctx, callID, projectID, time.Since(askStart).Milliseconds(), len(hits), decision, false)
		return out, nil
	case secretmatch.TrackAndReplace:
		out, err := s.trackAndReplace(ctx, req, projectID, rootSessionID, sessionID, hits)
		emitPersonDecision(ctx, callID, projectID, time.Since(askStart).Milliseconds(), len(hits), decision, err != nil)
		return out, err
	case secretmatch.Withhold:
		emitPersonDecision(ctx, callID, projectID, time.Since(askStart).Milliseconds(), len(hits), decision, true)
		return modelcall.CompletionRequest{}, &ModelRequestSecretWithheldError{Guidance: resolution.Guidance}
	case secretmatch.Unanswered:
		emitPersonDecision(ctx, callID, projectID, time.Since(askStart).Milliseconds(), len(hits), decision, true)
		return modelcall.CompletionRequest{}, ErrModelRequestSecretUnanswered
	default:
		emitPersonDecision(ctx, callID, projectID, time.Since(askStart).Milliseconds(), len(hits), "unknown", true)
		return modelcall.CompletionRequest{}, &ModelRequestSecretScreenFaultError{
			Stage: secretmatch.FaultStageRaise,
			Err:   fmt.Errorf("unknown secret screen decision %q", resolution.Decision),
		}
	}
}

func emitCapacityWait(ctx context.Context, mode string, started time.Time, failed bool) {
	waitMS := time.Since(started).Milliseconds()
	lifecycleFromContext(ctx).record(ctx, "inference_capacity_wait", "mode", mode, "wait_ms", waitMS, "failed", failed)
	secretmatch.EmitPhaseRecord(ctx, secretmatch.ScreenPhaseRecord{
		CallID:     secretmatch.ScreenCallIDFrom(ctx),
		Phase:      "inference_capacity_wait",
		Mode:       mode,
		DurationMS: waitMS,
		Failed:     failed,
	})
}
