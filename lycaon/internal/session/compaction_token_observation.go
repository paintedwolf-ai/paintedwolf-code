package session

import (
	"github.com/lycaon/lycaon/internal/llm/compaction"
)

// RecordCompactionTokenObservation pairs reported input usage with its host estimate.
func (m *Manager) RecordCompactionTokenObservation(sessionID string, reportedPromptTokens, transcriptEstimate int) {
	if m == nil || sessionID == "" || reportedPromptTokens <= 0 || transcriptEstimate <= 0 {
		return
	}
	m.compactionTokenCalibration.Store(sessionID, compaction.PromptTokenCalibration{
		ReportedPromptTokens: reportedPromptTokens,
		TranscriptEstimate:   transcriptEstimate,
	})
}

// CompactionTokenCalibration returns the last estimate-to-reported ratio, or zero when uncalibrated.
func (m *Manager) CompactionTokenCalibration(sessionID string) compaction.PromptTokenCalibration {
	if m == nil || sessionID == "" {
		return compaction.PromptTokenCalibration{}
	}
	cal, ok := m.compactionTokenCalibration.Load(sessionID)
	if !ok {
		return compaction.PromptTokenCalibration{}
	}
	return cal
}

func (m *Manager) compactionBudgetTokens(sessionID string, transcriptEstimate int) int {
	cold := compaction.ResolveColdStartOverhead("")
	if m != nil && m.compactor != nil {
		cold = m.compactor.Config().ColdStartOverhead(cold)
	}
	return m.CompactionTokenCalibration(sessionID).ProjectBilled(transcriptEstimate, cold)
}
