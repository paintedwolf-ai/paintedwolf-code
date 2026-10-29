package chats

import (
	"context"
	"github.com/lycaon/lycaon/internal/session/store"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type retirementRoot struct {
	session *wire.Session
	tree    []string
}
type retirementPlan struct {
	roots []retirementRoot
	total int
}

// Selection uses project metadata and indexed tree identities, never full
// presentation hydration for unrelated projects or retained transcript history.
func planProjectRetirement(ctx context.Context, sessions Store, projectID string) (retirementPlan, error) {
	plan := retirementPlan{}
	for _, archived := range []bool{false, true} {
		query := store.SummaryQuery{ProjectID: projectID, Archived: archived, Sort: wire.SessionListSortCreated, Order: wire.SessionListOrderAsc, Limit: wire.MaxSessionListLimit}
		for {
			page, err := sessions.ListProjectSummaries(ctx, query)
			if err != nil {
				return plan, err
			}
			for _, row := range page.Sessions {
				tree, err := sessions.SessionTreeIDs(ctx, row.ID)
				if err != nil {
					return plan, err
				}
				plan.roots = append(plan.roots, retirementRoot{session: &wire.Session{ID: row.ID, ProjectID: projectID, Status: row.Status}, tree: tree})
				plan.total += len(tree)
			}
			if page.Next == nil {
				break
			}
			query.After = page.Next
		}
	}
	return plan, nil
}
