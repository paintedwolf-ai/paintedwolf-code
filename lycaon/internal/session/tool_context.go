package session

import (
	"github.com/lycaon/lycaon/internal/agentpresence"
	"github.com/lycaon/lycaon/internal/session/instructions"
	"github.com/lycaon/lycaon/internal/session/sourcebrief"
	"github.com/lycaon/lycaon/internal/sourceeffect"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/tools"
)

// SetSourceLedger wires the app-scoped source mutation recorder into every tool invocation.
func (m *Manager) SetSourceLedger(recorder sourceledger.Recorder) {
	if m != nil {
		m.sourceLedger = recorder
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
func (m *Manager) SetEditorDocuments(documents tools.EditorDocuments) {
	if m != nil {
		m.editorDocuments = documents
		m.ToolContext.SetEditorDocuments(documents)
	}
}

// SetAgentPresence installs the projection tool calls report file activity to.
func (m *Manager) SetAgentPresence(tracker *agentpresence.Tracker) {
	if m != nil {
		m.agentPresence = tracker
		if tracker != nil {
			m.Naming.SetPresence(tracker)
		} else {
			m.Naming.SetPresence(nil)
		}
	}
}

// SetSourceMutations shares native effect journaling with project mutation recovery.
func (m *Manager) SetSourceMutations(service sourceeffect.Journal) {
	if m != nil {
		m.sourceMutations = service
		m.ToolContext.SetSourceMutations(service)
	}
}
