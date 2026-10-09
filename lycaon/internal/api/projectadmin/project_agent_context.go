package projectadmin

import (
	"errors"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/governance"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectcontrib"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) HandleGetProjectAgentContext(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.Registry, s.responses, w, r)
	if !ok {
		return
	}
	p, ok = requestscope.ProjectForRequest(s.Store, s.responses, w, r, p)
	if !ok {
		return
	}
	rootID, rootPath, err := project.ResolveWorkspaceRoot(p, r.URL.Query().Get("root_id"))
	if err != nil || rootID == "" || rootPath == "" {
		s.responses.Fail(w, wire.ApiErrorCodeRootNotFound, "root_id must name an attached project root")
		return
	}
	relPath := strings.TrimSpace(r.URL.Query().Get("path"))
	if relPath == "" {
		relPath = "."
	}
	chain, err := governance.ResolveChainMetadata(rootPath, relPath)
	if errors.Is(err, governance.ErrPathOutsideRoot) {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidPath, "path escapes the project folder")
		return
	}
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	result := wire.ProjectAgentContext{
		ProjectID:           p.ID,
		RootID:              rootID,
		Path:                filepath.ToSlash(filepath.Clean(relPath)),
		InstructionsEnabled: s.Settings.TrustSurfaces.Applies(projectcontrib.SurfaceAgentsMD, *p),
		SkillsEnabled:       s.Settings.TrustSurfaces.Applies(projectcontrib.SurfaceSkills, *p),
		Instructions:        []wire.AgentContextInstruction{},
		Skills:              []wire.AgentContextSkill{},
	}
	if result.InstructionsEnabled {
		for _, item := range chain {
			result.Instructions = append(result.Instructions, wire.AgentContextInstruction{Path: item.Path})
		}
	}
	if result.SkillsEnabled {
		loaded, _ := s.Sessions.Profiles.EffectiveSkills(r.Context(), p.ID, project.RootPaths(p))
		for _, skill := range loaded {
			if !skill.Project {
				continue
			}
			skillPath := filepath.Join(skill.Dir, "SKILL.md")
			rel, relErr := filepath.Rel(rootPath, skillPath)
			if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
			result.Skills = append(result.Skills, wire.AgentContextSkill{
				Name: skill.Name, Description: skill.Description, Path: filepath.ToSlash(rel),
			})
		}
	}
	httpio.WriteJSON(w, http.StatusOK, result)
}
