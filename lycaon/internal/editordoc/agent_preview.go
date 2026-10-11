package editordoc

import (
	"context"

	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/textfile"
)

type AgentPreview struct {
	Before                  *Document
	After                   string
	AfterSHA256             string
	BeforeBytes, AfterBytes int64
}

// PreviewAgentEdits resolves the complete edit set against one accepted head per
// document. Acceptance fences these revisions after prepared-content review.
func (s *Service) PreviewAgentEdits(ctx context.Context, inputs []AgentEdit) ([]AgentPreview, error) {
	keys, err := agentBatchKeys(inputs)
	if err != nil {
		return nil, err
	}
	s.ops.RLock()
	defer s.ops.RUnlock()
	unlock := s.lockDocuments(keys)
	defer unlock()
	out := make([]AgentPreview, len(inputs))
	for i, in := range inputs {
		d, err := s.checked(ctx, in.DocumentID, in.ProjectID)
		if err != nil {
			return nil, err
		}
		plan, err := s.planAgentEdit(ctx, d, in)
		if err != nil {
			return nil, err
		}
		limits := textfile.LimitsForRaw(projectsource.SourceWriteMaxBytes)
		before, err := textfile.EncodeBounded(serializeEOL(d.Draft, d.EOL), d.Encoding, limits)
		if err != nil {
			return nil, err
		}
		after, err := textfile.EncodeBounded(serializeEOL(plan.content, d.EOL), d.Encoding, limits)
		if err != nil {
			return nil, err
		}
		out[i] = AgentPreview{Before: d, After: plan.content, AfterSHA256: textfile.SHA256(after), BeforeBytes: int64(len(before)), AfterBytes: int64(len(after))}
	}
	return out, nil
}
