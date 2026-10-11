package toolhost

import (
	"github.com/lycaon/lycaon/internal/toolapproval"

	"github.com/lycaon/lycaon/internal/session/approvalstate"
)

type gateRepeatLedgerAdapter struct {
	rt *approvalstate.GateRepeatLedger
}

func (a gateRepeatLedgerAdapter) NoteAsk(chatSessionID, reasonKey, subject string) toolapproval.GateRepeatSnapshot {
	if a.rt == nil {
		return toolapproval.GateRepeatSnapshot{ReasonKey: reasonKey}
	}
	snap := a.rt.NoteAsk(chatSessionID, reasonKey, subject)
	return toolapproval.GateRepeatSnapshot{
		ReasonKey:         snap.ReasonKey,
		Count:             snap.Count,
		Subjects:          append([]string(nil), snap.Subjects...),
		SubjectsTruncated: snap.SubjectsTruncated,
		Suppressed:        snap.Suppressed,
	}
}

func (a gateRepeatLedgerAdapter) NoteSuppressed(chatSessionID, reasonKey, subject string) {
	if a.rt != nil {
		a.rt.NoteSuppressed(chatSessionID, reasonKey, subject)
	}
}
