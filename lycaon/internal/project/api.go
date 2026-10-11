package project

import (
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
)

func ToAPI(p *Project) api.Project {
	if p == nil {
		return api.Project{}
	}
	out := api.Project{
		ID:              p.ID,
		Roots:           make([]api.ProjectRoot, 0, len(p.Roots)),
		RootsGeneration: p.RootsGeneration,
		SessionCount:    p.SessionCount,
		Starred:         p.Starred,
		IsDraft:         p.IsDraft,
		LastOpenedAt:    p.LastOpenedAt,
		CreatedAt:       p.CreatedAt,
	}
	if p.Promotion != nil {
		out.Promotion = &api.ProjectPromotion{
			DestinationPath: p.Promotion.DestinationPath,
			InitGit:         p.Promotion.InitGit,
			Phase:           string(p.Promotion.Phase),
			LastError:       p.Promotion.LastError,
			UpdatedAt:       p.Promotion.UpdatedAt,
		}
	}
	if name := strings.TrimSpace(p.Name); name != "" {
		out.Name = &name
	}
	if p.LastActivityAt != nil {
		t := *p.LastActivityAt
		out.LastActivityAt = &t
	}
	if id := strings.TrimSpace(p.CoverArtifactID); id != "" {
		out.CoverArtifactID = &id
		if root := strings.TrimSpace(p.CoverRootSessionID); root != "" {
			out.CoverRootSessionID = &root
		}
		if src := strings.TrimSpace(p.CoverSource); src != "" {
			out.CoverSource = &src
		}
		if p.CoverUpdatedAt != nil {
			t := *p.CoverUpdatedAt
			out.CoverUpdatedAt = &t
		}
	}
	for _, r := range p.Roots {
		wr := api.ProjectRoot{
			ID:        r.ID,
			Path:      r.Path,
			Label:     r.Label,
			IsPrimary: r.IsPrimary,
			AddedAt:   r.AddedAt,
			Kind:      string(r.Kind),
		}
		if hash := strings.TrimSpace(r.GitRemoteHash); hash != "" {
			wr.GitRemoteHash = &hash
		}
		out.Roots = append(out.Roots, wr)
	}
	return out
}
