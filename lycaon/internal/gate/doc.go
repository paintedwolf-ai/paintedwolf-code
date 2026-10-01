// Package gate is the single decision for whether a human is interrupted.
//
// Gates fire on an action's effect, never on its cause. Each gate is a predicate
// over machine facts a layer already produced — never a score — and fires with
// the facts that made it fire. Evaluate is pure: no clock, no config, no session
// state.
//
// Several gates may fire at once: the first in citation order is the card's
// reason, and Reuse over the whole set decides what the card may offer.
//
// Facts.Ran records which producers reported. A gate whose producers did not run
// at the last stage that could have produced them becomes a fail-closed Ask
// (GateIncompleteFacts).
package gate
