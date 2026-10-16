package history

import (
	"context"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/pkg/api"
)

type tokenCalibration struct {
	providerID string
	model      string
	tokens     compaction.PromptTokenCalibration
}

// ObserveTokens pairs one receiver's input usage with its host estimate.
func (m *Service) ObserveTokens(sessionID, providerID, model string, reportedPromptTokens, transcriptEstimate int) {
	if m == nil || sessionID == "" || reportedPromptTokens <= 0 || transcriptEstimate <= 0 {
		return
	}
	m.calibration.Store(sessionID, tokenCalibration{
		providerID: providerID, model: model,
		tokens: compaction.PromptTokenCalibration{
			ReportedPromptTokens: reportedPromptTokens,
			TranscriptEstimate:   transcriptEstimate,
		},
	})
}

// Calibration only reuses observations from the selected receiver.
func (m *Service) Calibration(ctx context.Context, sess *api.Session) compaction.PromptTokenCalibration {
	if m == nil || sess == nil || sess.ID == "" {
		return compaction.PromptTokenCalibration{}
	}
	providerID, model := m.Model(ctx, sess)
	cal, ok := m.calibration.Load(sess.ID)
	if !ok || cal.providerID != providerID || cal.model != model {
		return compaction.PromptTokenCalibration{}
	}
	return cal.tokens
}

func (m *Service) BudgetTokens(ctx context.Context, sess *api.Session, transcriptEstimate int) int {
	cold := compaction.ResolveColdStartOverhead("")
	if m != nil && m.Compactor != nil {
		cold = m.Compactor.Config().ColdStartOverhead(cold)
	}
	return m.Calibration(ctx, sess).ProjectBilled(transcriptEstimate, cold)
}
