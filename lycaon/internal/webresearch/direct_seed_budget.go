package webresearch

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
)

const (
	// maxSeedsPerQuery caps the merged publisher-root set (plan seeds plus lead
	// hosts) crawled for one background warmSeed / pickSeeds call.
	maxSeedsPerQuery = 40
	maxLeadsPerQuery = 40
	seedExpandMin    = 4
	seedExpandMax    = 8
)

// seedBudget scales discovery breadth while limiting duplicate seed hosts.
type seedBudget struct {
	maxSeeds  int
	minLeads  int
	maxLeads  int
	expandMin int
	expandMax int
	hitTarget int
}

func seedBudgetForHits(maxResults int) seedBudget {
	if maxResults <= 0 {
		maxResults = defaultPageBudget
	}
	scale := max(maxResults, 8)
	// Lead count bounds completion size within the seed-call deadline.
	return seedBudget{
		maxSeeds:  max(3, scale/3),
		minLeads:  max(8, scale-2),
		maxLeads:  min(maxLeadsPerQuery, max(12, scale+2)),
		expandMin: seedExpandMin,
		expandMax: seedExpandMax,
		hitTarget: maxResults,
	}
}

// slimSeedBudget reduces discovery breadth for the shorter retry deadline.
func slimSeedBudget(maxResults int) seedBudget {
	if maxResults <= 0 {
		maxResults = defaultPageBudget
	}
	return seedBudget{
		maxSeeds:  2,
		minLeads:  6,
		maxLeads:  10,
		expandMin: 3,
		expandMax: 5,
		hitTarget: maxResults,
	}
}

func buildPickSeedsUserMessage(ctx context.Context, query string, period Period, taskHint string, budget seedBudget) (string, error) {
	return guidance.RenderCatalog(ctx, guidance.UtilityPickSeedsUserRef, map[string]any{
		"query":      strings.TrimSpace(query),
		"min_leads":  budget.minLeads,
		"max_leads":  budget.maxLeads,
		"max_seeds":  budget.maxSeeds,
		"expand_min": budget.expandMin,
		"expand_max": budget.expandMax,
		"hit_target": budget.hitTarget,
		// The explicit period controls recency, including queries containing a year.
		"period_current": period.IsCurrent(),
		"period":         period.String(),
		"task_hint":      strings.TrimSpace(taskHint),
	})
}

func (p seedPlan) seedHostCount() int {
	hosts := seedHostsSet(p.seeds)
	for _, lead := range p.leads {
		if h := normalizeLeadHost(lead.host); h != "" {
			hosts[h] = struct{}{}
		}
	}
	return len(hosts)
}

// Fewer seed hosts receive a larger share of the hit budget.
func perHostCapForHostCount(hosts int) int {
	switch {
	case hosts <= 2:
		return 5
	case hosts >= 7:
		return 2
	default:
		return maxHitsPerHost
	}
}
