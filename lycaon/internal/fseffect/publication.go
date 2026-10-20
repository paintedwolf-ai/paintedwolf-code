package fseffect

// PublicationState is an observed transition of the existing replacement door.
// It is independent of command status, review acceptance, and journal settlement.
type PublicationState uint8

const (
	// PublicationNotCommitted includes preparation and all refusals before rename.
	PublicationNotCommitted PublicationState = iota
	// PublicationReplaced means the held destination entry was replaced. A later
	// error must preserve recovery intent rather than replay the replacement.
	PublicationReplaced
	// PublicationDurable additionally observes successful directory synchronization.
	PublicationDurable
	// PublicationVerified additionally confirms the committed bytes and digest.
	PublicationVerified
)

// Committed reports whether the publication crossed its irreversible rename.
func (s PublicationState) Committed() bool {
	return s >= PublicationReplaced && s <= PublicationVerified
}
