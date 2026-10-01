//go:build gts_parsercorephase0 && !gts_no_parsercorephase0

package gotreesitter

import (
	"errors"

	core "github.com/odvcencio/gotreesitter/internal/parsercorephase0"
)

// These methods are test-only adapters for the frozen v20 contract. Their
// names do not overlap legacy scheduler helpers.
func (s *diagnosticParserCoreGenericScheduler) G18AdoptUpdatedReductionSiblingOwned(
	owner core.SchedulerTransactionToken,
	source int,
	head core.Head,
	rank core.CleanPathRankSelection,
	lineage uint16,
	set core.AlternativeSet,
	setBlended bool,
	converged bool,
	resurrection bool,
	mutation G18DropCohortProducerMutationClass,
) (bool, error) {
	if mutation != g18FutureProducerSiblingAdoption {
		return false, errors.New("invalid sibling-adoption mutation class")
	}
	return s.adoptUpdatedReductionSiblingOwned(owner, source, head, rank, lineage, set, setBlended, core.DropCohortRefSet{}, converged, resurrection, core.DropCohortProducerSiblingAdoption)
}

func (s *diagnosticParserCoreGenericScheduler) G18ReconcileGenericConflictOutputsOwned(
	owner core.SchedulerTransactionToken,
	source int,
	outputs []diagnosticParserCoreHeader,
	mutation G18DropCohortProducerMutationClass,
) ([]diagnosticParserCoreHeader, int, error) {
	if mutation != g18FutureProducerConflictReconciliation {
		return nil, 0, errors.New("invalid conflict-reconciliation mutation class")
	}
	return s.reconcileGenericConflictOutputsOwnedWithMutation(owner, source, outputs, core.DropCohortProducerConflictReconciliation)
}

func (s *diagnosticParserCoreGenericScheduler) G18CanonicalizeOwned(
	owner core.SchedulerTransactionToken,
	mutation G18DropCohortProducerMutationClass,
) error {
	var expected core.DropCohortProducerMutation
	switch mutation {
	case g18FutureProducerLinearCanonicalization:
		expected = core.DropCohortProducerLinearCanonicalization
	case g18FutureProducerMappedCanonicalization:
		expected = core.DropCohortProducerMappedCanonicalization
	default:
		return errors.New("invalid canonicalizer mutation class")
	}
	return s.canonicalizeOwnedWithMutation(owner, expected)
}
