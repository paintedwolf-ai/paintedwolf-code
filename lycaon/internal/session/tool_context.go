package session

import (
	"github.com/lycaon/lycaon/internal/agentpresence"
	"github.com/lycaon/lycaon/internal/session/instructions"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/tools"
)

// SetSourceLedger wires the app-scoped source mutation recorder into every tool invocation.
func (m *Host) SetSourceLedger(recorder sourceledger.Recorder, history tools.SourceHistory, commands sourceledger.CommandWindowOpener, mutations tools.SourceGitMutations, checkpoints instructions.ReviewCheckpointer, observations *sourceledger.Inventory) {
	if m == nil {
		return
	}
	m.ToolContext.SetSourceLedger(recorder, history, commands, mutations, observations)
	m.Verification.SetSourceObservations(observations)
	m.Runner.Instructions.SetReviewCheckpointer(checkpoints)
	m.SourceBriefs.SetSources(history, checkpoints)
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
