package loopguard

import (
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
)

// DoomLoopMaxAttempts is the max model responses repeating identical tool+args
// before blocking.
const DoomLoopMaxAttempts = 8

// DoomLoopMaxSameCodeRejects bounds consecutive rejected responses with identical arguments.
const DoomLoopMaxSameCodeRejects = 2

// DoomLoopMaxCodeRepeats bounds a tool's rejected responses for one code.
const DoomLoopMaxCodeRepeats = 3

// DoomLoopGuard supplies repetition checks and observations for OAR.
type DoomLoopGuard interface {
	promptloop.DoomLoopGuard
	CodeRejectResponses(sessionID, tool, code string) int
	FruitlessSearchRun(sessionID, tool string, args map[string]any) int
	SetPageTargetResolver(func(sessionID, pageID string) string)
}
