package execution

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/tools"
)

// TurnLoading supplies session decisions and their durable receipts.
type TurnLoading interface {
	ResolveToolRequest(context.Context, tools.ToolContext, string, []turnload.ToolCard) turnload.RequestOutcome
	RecordToolRequest(context.Context, tools.ToolContext, turnload.RequestOutcome, turnload.RequestToolsResult, time.Duration)
	LookupSkills(context.Context, tools.ToolContext, string, []skills.Skill) turnload.LookupOutcome
}

type turnSourceBinding struct {
	loading TurnLoading
}

// TurnSources connects handlers registered before the session host is built.
type TurnSources struct {
	source atomic.Pointer[turnSourceBinding]
}

func (s *TurnSources) Bind(loading TurnLoading) error {
	if s == nil || loading == nil {
		return fmt.Errorf("session turn loading required")
	}
	if !s.source.CompareAndSwap(nil, &turnSourceBinding{loading: loading}) {
		return fmt.Errorf("session turn loading already bound")
	}
	return nil
}

func (s *TurnSources) resolve(ctx context.Context, tctx tools.ToolContext, need string, cards []turnload.ToolCard) turnload.RequestOutcome {
	if binding := s.source.Load(); binding != nil {
		return binding.loading.ResolveToolRequest(ctx, tctx, need, cards)
	}
	return turnload.RequestOutcome{Need: need, Abstained: true, Reason: "session turn loading unavailable", Failure: turnload.RankingUnavailable}
}

func (s *TurnSources) record(ctx context.Context, tctx tools.ToolContext, outcome turnload.RequestOutcome, result turnload.RequestToolsResult, elapsed time.Duration) {
	if binding := s.source.Load(); binding != nil {
		binding.loading.RecordToolRequest(ctx, tctx, outcome, result, elapsed)
	}
}

func (s *TurnSources) lookup(ctx context.Context, tctx tools.ToolContext, need string, roster []skills.Skill) turnload.LookupOutcome {
	if binding := s.source.Load(); binding != nil {
		return binding.loading.LookupSkills(ctx, tctx, need, roster)
	}
	return turnload.LookupOutcome{Need: need, Abstained: true, Reason: "session turn loading unavailable", Failure: turnload.RankingUnavailable}
}
