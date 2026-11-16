package prompts

import (
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// evidenceMints is the mints template fact: whether the reader's tools can mint
// each evidence kind, so copy names a handle only where one can be held.
func evidenceMints(reachHeld map[string]bool) map[string]bool {
	minted, err := evidence.ActiveBinding().MintedKinds(reachHeld)
	if err != nil {
		return map[string]bool{}
	}
	return minted
}

// evidenceMintsForCommand narrows mints by the effects command is allowed on the
// surface: mutating native operations require command mutation, and command's
// terminal_capture variant (tui#) requires external execution.
func evidenceMintsForCommand(enabled map[string]bool, commandMutates, commandExternalExec bool) map[string]bool {
	var classes []toolcontract.CommandEffectClass
	if commandMutates {
		classes = append(classes, toolcontract.CommandMutation)
	}
	if commandExternalExec {
		classes = append(classes, toolcontract.CommandExternalExec)
	}
	effects, _ := toolcontract.NewCommandEffects(classes...)
	return evidenceMints(readerHeldSet(enabled, effects))
}
