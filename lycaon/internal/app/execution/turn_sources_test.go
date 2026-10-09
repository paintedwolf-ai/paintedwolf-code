package execution

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestTurnSourcesFailClosedUntilOneSessionOwnerIsBound(t *testing.T) {
	var sources TurnSources
	// Registration captures these callbacks before the session host exists.
	resolve, record, lookup := sources.resolve, sources.record, sources.lookup
	ctx, toolContext := t.Context(), tools.ToolContext{}
	request := resolve(ctx, toolContext, "inspect diagnostics", nil)
	skill := lookup(ctx, toolContext, "inspect diagnostics", nil)
	if request.Failure != turnload.RankingUnavailable || !request.Abstained ||
		skill.Failure != turnload.RankingUnavailable || !skill.Abstained {
		t.Fatalf("unbound handlers admitted a decision: request=%+v, skill=%+v", request, skill)
	}
	record(ctx, toolContext, request, turnload.RequestToolsResult{}, time.Second)
	if err := sources.Bind(nil); err == nil {
		t.Fatal("construction accepted absent session loading")
	}
	owner := &loadingOwnerFixture{}
	if err := (*TurnSources)(nil).Bind(owner); err == nil {
		t.Fatal("construction accepted absent execution sources")
	}
	if err := sources.Bind(owner); err != nil {
		t.Fatal(err)
	}
	replacement := &loadingOwnerFixture{}
	if err := sources.Bind(replacement); err == nil {
		t.Fatal("session loading owner was replaceable")
	}
	request = resolve(ctx, toolContext, "bound request", nil)
	skill = lookup(ctx, toolContext, "bound lookup", nil)
	record(ctx, toolContext, request, turnload.RequestToolsResult{}, time.Second)
	if request.Need != "bound request" || skill.Need != "bound lookup" ||
		owner.requests != 1 || owner.lookups != 1 || owner.records != 1 {
		t.Fatalf("registered callbacks did not use their session owner: owner=%+v, request=%+v, lookup=%+v", owner, request, skill)
	}
	if *replacement != (loadingOwnerFixture{}) {
		t.Fatalf("rejected owner received a callback: %+v", replacement)
	}
}

type loadingOwnerFixture struct{ requests, records, lookups int }

func (f *loadingOwnerFixture) ResolveToolRequest(_ context.Context, _ tools.ToolContext, need string, _ []turnload.ToolCard) turnload.RequestOutcome {
	f.requests++
	return turnload.RequestOutcome{Need: need}
}

func (f *loadingOwnerFixture) RecordToolRequest(context.Context, tools.ToolContext, turnload.RequestOutcome, turnload.RequestToolsResult, time.Duration) {
	f.records++
}

func (f *loadingOwnerFixture) LookupSkills(_ context.Context, _ tools.ToolContext, need string, _ []skills.Skill) turnload.LookupOutcome {
	f.lookups++
	return turnload.LookupOutcome{Need: need}
}
