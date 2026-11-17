package toolapproval

import "github.com/lycaon/lycaon/internal/session/approvalstate"

// GateRepeatLedger records reason-keyed repeat counts for approval cards.
type GateRepeatLedger interface {
	NoteAsk(chatSessionID, reasonKey, subject string) approvalstate.RepeatSnapshot
	NoteSuppressed(chatSessionID, reasonKey, subject string)
}
