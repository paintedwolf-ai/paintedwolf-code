package session

import (
	"github.com/lycaon/lycaon/internal/agentpresence"
	"github.com/lycaon/lycaon/internal/session/instructions"
	"github.com/lycaon/lycaon/internal/session/sourcebrief"
	"github.com/lycaon/lycaon/internal/sourceledger"
)

// SetSourceLedger wires the app-scoped source mutation recorder into every tool invocation.
func (m *Host) SetSourceLedger(recorder sourceledger.Recorder) {
	if m != nil {
		m.ToolContext.SourceLedger = recorder
		m.ToolContext.SetSourceLedger(recorder)
		m.Verification.SetSourceLedger(recorder)
		checkpointer, _ := recorder.(instructions.ReviewCheckpointer)
		m.Runner.Instructions.SetReviewCheckpointer(checkpointer)
		reader, _ := recorder.(sourcebrief.Ledger)
		m.SourceBriefs.SetLedger(reader)
	}
}

// SetEditorDocuments wires the open-document view into every tool invocation,
// so a file the person has open is read and written as that document.

// SetAgentPresence installs the projection tool calls report file activity to.
func (m *Host) SetAgentPresence(tracker *agentpresence.Tracker) {
	if m != nil {
		m.Coordinator.Tools.Presence = tracker

		if tracker != nil {
			m.Chats.Naming.SetPresence(tracker)
		} else {
			m.Chats.Naming.SetPresence(nil)
		}
	}
}

// SetSourceMutations shares native effect journaling with project mutation recovery.
