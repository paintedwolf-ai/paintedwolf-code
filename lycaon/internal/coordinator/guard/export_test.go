package guard

import (
	"context"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/pkg/api"
)

func formatFirstOARBlock(gc *oar.GuardContext, rejectFmt *guidance.StaticRejectFormatter, anchors ...string) (string, bool) {
	if gc == nil || rejectFmt == nil {
		return "", false
	}
	p := hostFinishOARPipeline()
	if p == nil {
		return "", false
	}
	for _, anchor := range anchors {
		res, err := p.EvaluateBlock(context.Background(), anchor, gc)
		if err != nil || res == nil || res.Decision == nil {
			continue
		}
		d := res.Decision
		formatted, err := rejectFmt.Format(d.Code, d.Data)
		if err != nil {
			return "", false
		}
		return formatted, true
	}
	return "", false
}

func hostFinishOARPipeline() *oar.GuardPipeline {
	hostFinishPipelineOnce.Do(func() {
		root := moduleConfigRoot()
		schemaDir := filepath.Join(filepath.Dir(root), "schemas")
		if _, err := filepath.Abs(schemaDir); err != nil {
			schemaDir = filepath.Join(root, "..", "schemas")
		}
		loader, err := oar.NewLoader(schemaDir)
		if err != nil {
			return
		}
		rs, err := loader.LoadEffectivePolicy()
		if err != nil {
			return
		}
		p := oar.NewGuardPipeline(rs, loader, oar.NewCounterStore())
		p.EnableAnchor(oar.AnchorCoordinatorCloseoutCheck)
		p.EnableAnchor(oar.AnchorCoordinatorMidTurnNote)
		p.EnableAnchor(oar.AnchorCoordinatorPreInvoke)
		p.EnableAnchor(oar.AnchorToolPreInvoke)
		hostFinishPipeline = p
	})
	return hostFinishPipeline
}

func resultHasCode(res *oar.PipelineResult, code string) bool {
	if res == nil || res.Decision == nil || code == "" {
		return false
	}
	return res.Decision.Code == code
}

// LastInvestigateChainBoundary returns the history index after the most recent
// investigate chain break (task dispatch or worker lifecycle), or 0 at session start.
func LastInvestigateChainBoundary(history []api.Message) int {
	boundary := 0
	for i, msg := range history {
		if breaksInvestigateChain(msg) {
			boundary = i + 1
		}
	}
	return boundary
}

// CoordinatorToolMessagesSinceInvestigateChain returns parent transcript messages
// since the last investigate chain boundary for coordinator grounding evidence.
func CoordinatorToolMessagesSinceInvestigateChain(history []api.Message) []api.Message {
	since := LastInvestigateChainBoundary(history)
	if since < 0 {
		since = 0
	}
	if since >= len(history) {
		return nil
	}
	return append([]api.Message(nil), history[since:]...)
}
