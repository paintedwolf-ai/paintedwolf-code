package toolhost

import (
	"context"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/tools/fileage"
	surveytools "github.com/lycaon/lycaon/internal/tools/native/survey"
	"github.com/lycaon/lycaon/internal/toolscope"
)

// SurveyServices owns source survey tools and shared repository caches.
type SurveyServices struct {
	summarizeTool  *surveytools.SummarizeTool
	fileAge        *fileage.Provider
	gitStatusCache *statusCacheBinding
}

func (r *SurveyServices) WarmFileAge(ctx context.Context, projectDir string) {
	if r == nil {
		return
	}
	r.fileAge.Warm(ctx, projectDir)
}

func (r *SurveyServices) InvalidateFileAge(projectDir string) {
	if r == nil {
		return
	}
	r.fileAge.Invalidate(projectDir)
}

func (r *SurveyServices) SetGitStatusCache(cache *git.StatusCache) {
	if r == nil {
		return
	}
	r.gitStatusCache.replace(cache)
}

func (r *SurveyServices) SetScopeGuards(cfg toolscope.Config) {
	if r == nil {
		return
	}
	toolscope.SetGlobal(cfg)
}
