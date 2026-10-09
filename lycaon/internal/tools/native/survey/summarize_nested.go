package survey

import (
	"github.com/lycaon/lycaon/internal/sandbox"
)

func (g *summaryNestedRepos) surveyPruneOpts(base sandbox.SurveyOptions) sandbox.SurveyOptions {
	if g != nil && g.enabled {
		base.PruneNestedVCS = true
		if g.nestedPruneCount != nil {
			base.OnNestedRepoPruned = g.noteNestedRepoPruned
		}
	}
	return base
}

func (g *summaryNestedRepos) noteNestedRepoPruned(abs string) {
	if g == nil || g.nestedPruneCount == nil {
		return
	}
	g.memoMu.Lock()
	defer g.memoMu.Unlock()
	if g.nestedPruneSeen == nil {
		g.nestedPruneSeen = map[string]struct{}{}
	}
	if _, ok := g.nestedPruneSeen[abs]; ok {
		return
	}
	g.nestedPruneSeen[abs] = struct{}{}
	*g.nestedPruneCount++
}
