package promptrender

import (
	"github.com/lycaon/lycaon/test/contract/internal/independence"
	"github.com/lycaon/lycaon/test/contract/internal/interactions"
)

func init() {
	conditionConstraints = append(conditionConstraints, guidanceConditionConstraints...)
}

const (
	srcAbsentMember  = "a member of an absent template value resolves empty, so a test on the member holds only when the value is present"
	srcScanOnlyKind  = "internal/app/build_workflows.go registers only the scan obligation kind, and internal/workflow/obligation.go stamps obligations.<kind> only for a registered kind"
	srcRecallFields  = "config/packs/painted-wolf/platform/tools/schemas/recall.yaml declares query beside history, no projection drops history, and prompts.GuidanceRenderer.RenderWithReach derives both facts from the one recall reach"
	srcNonBlockLevel = "a warn or nudge effect is not block"
)

// guidanceConditionConstraints are the production invariants over guidance
// guard atoms.
var guidanceConditionConstraints = []interactions.AtomConstraint{
	{Atom: `blueprint_approval.status == "approved"`, Implies: "blueprint_approval", Recipient: interactions.RecipientCoordinator, Source: srcAbsentMember},
	{Atom: "review_verdict.survey_claims", Implies: "review_verdict", Recipient: interactions.RecipientCoordinator, Source: srcAbsentMember},
	{Atom: "review_verdict.survey_challenged", Implies: "review_verdict", Recipient: interactions.RecipientCoordinator, Source: srcAbsentMember},
	{Atom: "obligations.scan", Implies: "obligations", Recipient: interactions.RecipientCoordinator, Source: srcAbsentMember},
	{Atom: "obligations", Implies: "obligations.scan", Recipient: interactions.RecipientCoordinator, Source: srcScanOnlyKind},
	{Atom: "can_recall_query", Implies: "can_recall_history", Recipient: unscopedRecipient, Source: srcRecallFields},
	{Atom: `effect == "warn"`, Implies: `effect != "block"`, Recipient: unscopedRecipient, Source: srcNonBlockLevel},
	{Atom: `effect == "nudge"`, Implies: `effect != "block"`, Recipient: unscopedRecipient, Source: srcNonBlockLevel},
}

// guidanceSiteFixed are atoms one guard site holds constant because of
// where its producer renders it.
var guidanceSiteFixed = map[string][]independence.Fixed{
	// Inside the references level's source loop.
	"guidance/compaction/continuation-record@L4:C39:if": {{
		Atom: "through_ord", Value: independence.True,
		Source: "only compaction.continuationDisplayFor renders the references level; its through_ord is the continuation's ThroughOrd, at least every compacted row's ord (session_reduction.go:continuationFor), and session_entries.ord > 0 (internal/db/schema.sql)",
	}},
	// Inside the summary level's source loop, after a source without handles.
	"guidance/compaction/continuation-record@L23:C302:elif": {{
		Atom: "s.message_id", Value: independence.True,
		Source: "every compacted source carries a message id: compaction.recoverySources skips rows without one and compactedSources takes the stored message's id",
	}},
}

// guidanceDerived returns the constraints a guidance site's producer fixes
// on recipient.
func guidanceDerived(site interactions.ConditionSite, recipient string) independence.Constraints {
	var c independence.Constraints
	if recipient == unscopedRecipient {
		c.Fixed = append(c.Fixed, guidanceSiteFixed[site.ID]...)
	}
	return c
}
