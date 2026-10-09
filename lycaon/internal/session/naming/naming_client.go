package naming

import (
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/pkg/api"
)

func (m *Service) namer(sess *api.Session, purpose, projectDir string) llm.UtilityNamer {
	if m == nil {
		return nil
	}
	if m.namers != nil {
		sessionID, projectID := "", ""
		if sess != nil {
			sessionID = sess.ID
			projectID = sess.ProjectID
		}
		namer := m.namers.Namer(purpose, projectDir, sessionID, projectID)
		if sum, ok := namer.(llm.PlaneNamer); ok && sum.Summarizer != nil {
			sum.Summarizer.Cost = m.cost
		}
		return namer
	}
	return nil
}
