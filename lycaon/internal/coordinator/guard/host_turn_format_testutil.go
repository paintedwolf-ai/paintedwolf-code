package guard

import (
	"context"
	"path/filepath"
	"sync"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
)

var (
	hostFinishPipelineOnce sync.Once
	hostFinishPipeline     *oar.GuardPipeline
)

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

// EvaluateObserveHasCode reports whether EvaluateBlock fires code for anchor (test helper).
func EvaluateObserveHasCode(gc *oar.GuardContext, anchor, code string) bool {
	if gc == nil || code == "" {
		return false
	}
	p := hostFinishOARPipeline()
	if p == nil {
		return false
	}
	res, err := p.EvaluateBlock(context.Background(), anchor, gc)
	if err != nil || res == nil {
		return false
	}
	return resultHasCode(res, code)
}

func resultHasCode(res *oar.PipelineResult, code string) bool {
	if res == nil || res.Decision == nil || code == "" {
		return false
	}
	return res.Decision.Code == code
}
