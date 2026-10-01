package sourceapi

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func mapSourceContributors(authors []sourceledger.Contributor) []wire.SourceContributor {
	result := make([]wire.SourceContributor, 0, len(authors))
	for _, author := range authors {
		result = append(result, wire.SourceContributor{Origin: author.Origin, SessionID: author.SessionID,
			Turn: author.Turn, PersonID: author.PersonID, ActorLabel: author.ActorLabel,
			ToolCallID: author.ToolCallID, ToolName: author.ToolName, WorkerID: author.JobID})
	}
	return result
}

func mapSourceAttribution(attribution *sourceledger.ComparisonAttribution) *wire.SourceComparisonAttribution {
	if attribution == nil {
		return nil
	}
	return &wire.SourceComparisonAttribution{Before: mapAttributedText(attribution.Before), After: mapAttributedText(attribution.After)}
}

func mapAttributedText(runs []sourceledger.AttributedText) []wire.SourceAttributedText {
	result := make([]wire.SourceAttributedText, 0, len(runs))
	for _, run := range runs {
		result = append(result, wire.SourceAttributedText{Index: run.Index, Length: run.Length,
			Contributors: mapSourceContributors(run.Contributors), Selected: run.Selected, Visible: run.Visible})
	}
	return result
}

func (s *Handler) workspaceSourceHead(ctx context.Context, p *project.Project, fileID string) (sourceledger.BranchHead, sourcebranch.ID, error) {
	branches := make(map[sourcebranch.ID]bool)
	for _, root := range p.Roots {
		branch := p.BranchForRoot(root.ID)
		if branches[branch] {
			continue
		}
		branches[branch] = true
		head, err := s.SourceLedger.ResolveHeadByFile(ctx, p.ID, branch, fileID)
		if errors.Is(err, sourceledger.ErrHistoryNotFound) {
			continue
		}
		if err != nil {
			return sourceledger.BranchHead{}, sourcebranch.Trunk, err
		}
		for _, candidate := range p.Roots {
			if candidate.ID == head.RootID && p.BranchForRoot(candidate.ID) == branch {
				return head, branch, nil
			}
		}
	}
	return sourceledger.BranchHead{}, sourcebranch.Trunk, sourceledger.ErrHistoryNotFound
}

func workspaceSourceBranches(p *project.Project) map[string]sourcebranch.ID {
	branches := make(map[string]sourcebranch.ID, len(p.Roots))
	for _, root := range p.Roots {
		branches[root.ID] = p.BranchForRoot(root.ID)
	}
	return branches
}
