package promptrender

import (
	"context"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/test/contract/internal/independence"
	"github.com/lycaon/lycaon/test/contract/internal/interactions"
)

const unscopedRecipient = "unscoped"

// renderRecipient names a render's recipient by its reader's is_coordinator
// fact; a render with no reader is unscoped.
func renderRecipient(scope map[string]any) string {
	switch v, ok := scope["is_coordinator"].(bool); {
	case !ok:
		return unscopedRecipient
	case v:
		return interactions.RecipientCoordinator
	default:
		return interactions.RecipientWorker
	}
}

// occurrenceRecipient names an OAR occurrence's recipient by the session role
// its producer sets.
func occurrenceRecipient(gc *oar.GuardContext) string {
	if gc == nil || gc.Session.Role == "" {
		return unscopedRecipient
	}
	return gc.Session.Role
}

// conditionConstraints are the production invariants that hold guard atoms
// constant or coupled on a recipient.
var conditionConstraints = []interactions.AtomConstraint{
	{Atom: "is_coordinator", Recipient: interactions.RecipientCoordinator, Fixed: true, Value: independence.True,
		Source: "test/contract/invariants/promptrender/condition_ledger.go:renderRecipient partitions renders by is_coordinator"},
	{Atom: "is_coordinator", Recipient: interactions.RecipientWorker, Fixed: true, Value: independence.False,
		Source: "test/contract/invariants/promptrender/condition_ledger.go:renderRecipient partitions renders by is_coordinator"},
	{Atom: "has_git", Implies: "has_any_git_root", Recipient: interactions.RecipientCoordinator, Source: srcGitFacts},
	{Atom: "has_git", Implies: "has_any_git_root", Recipient: interactions.RecipientWorker, Source: srcGitFacts},
}

const srcGitFacts = "internal/session/readerfacts/resolver.go:gitFacts starts hasAnyGit at hasGit, so the any-root fact includes the primary root"

// StockConditionLedger discovers every stock guard and joins it with the
// atom observations of the host-state matrix.
func StockConditionLedger(ctx context.Context, s *Stock) (*interactions.ConditionLedger, error) {
	sum, err := RunCartesianMatrix(ctx, s)
	if err != nil {
		return nil, err
	}
	sites, err := interactions.DiscoverConditionSites(s.Catalog, s.Schemas)
	if err != nil {
		return nil, err
	}
	return interactions.BuildConditionLedger(sites, sum.Conditions, conditionConstraints)
}
