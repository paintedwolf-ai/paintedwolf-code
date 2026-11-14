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
	reachHeld := make(map[string]bool, len(enabled)+1)
	for name, ok := range enabled {
		reachHeld[name] = ok
	}
	if commandMutates {
		reachHeld[toolcontract.ReachCommandMutation] = true
	}
	minted := evidenceMints(reachHeld)
	if !commandExternalExec && reachHeld["command"] && minted["tui"] {
		withoutCommand := make(map[string]bool, len(reachHeld))
		for name, ok := range reachHeld {
			if name != "command" && name != toolcontract.ReachCommandMutation {
				withoutCommand[name] = ok
			}
		}
		minted["tui"] = evidenceMints(withoutCommand)["tui"]
	}
	return minted
}
