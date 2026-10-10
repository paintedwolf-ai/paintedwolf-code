package history

import (
	"github.com/lycaon/lycaon/internal/llm/compaction"
)

// ObserveTokens pairs reported input usage with its host estimate.
func (m *Service) ObserveTokens(sessionID string, reportedPromptTokens, transcriptEstimate int) {
	if m == nil || sessionID == "" || reportedPromptTokens <= 0 || transcriptEstimate <= 0 {
		return
	}
	m.calibration.Store(sessionID, compaction.PromptTokenCalibration{
		ReportedPromptTokens: reportedPromptTokens,
		TranscriptEstimate:   transcriptEstimate,
	})
}

// Calibration returns the last estimate-to-reported ratio, or zero when uncalibrated.
func (m *Service) Calibration(sessionID string) compaction.PromptTokenCalibration {
	if m == nil || sessionID == "" {
		return compaction.PromptTokenCalibration{}
	}
	cal, ok := m.calibration.Load(sessionID)
	if !ok {
		return compaction.PromptTokenCalibration{}
	}
	return cal
}

func (m *Service) BudgetTokens(sessionID string, transcriptEstimate int) int {
	cold := compaction.ResolveColdStartOverhead("")
	if m != nil && m.Compactor != nil {
		cold = m.Compactor.Config().ColdStartOverhead(cold)
	}
	return m.Calibration(sessionID).ProjectBilled(transcriptEstimate, cold)
}
