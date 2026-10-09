package toolhost

import (
	"context"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/repomap"
	"github.com/lycaon/lycaon/internal/tools/fileage"
	surveytools "github.com/lycaon/lycaon/internal/tools/native/survey"
	"github.com/lycaon/lycaon/internal/toolscope"
)

// SurveyServices owns source survey tools and shared repository caches.
type SurveyServices struct {
	readTool       *surveytools.ReadTool
	listDirTool    *surveytools.ListDirTool
	grepTool       *surveytools.GrepTool
	findTool       *surveytools.FindTool
	summarizeTool  *surveytools.SummarizeTool
	fileAge        *fileage.Provider
	gitStatusCache *atomic.Pointer[git.StatusCache]
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
	r.gitStatusCache.Store(cache)
}

func (r *SurveyServices) SetReadEvidenceLedger(ledger guidance.EvidenceLedgerReader) {
	if r == nil || r.readTool == nil {
		return
	}
	r.readTool.Ledger = ledger
}

func (r *SurveyServices) SetListDirUnionBrief(fn repomap.UnionOrientationBrief) {
	if r == nil || r.listDirTool == nil {
		return
	}
	r.listDirTool.UnionBrief = fn
}

func (r *SurveyServices) SetScopeGuards(cfg toolscope.Config, fileCount func(projectDir string) (int, bool)) {
	if r == nil {
		return
	}
	toolscope.SetGlobal(cfg)
	cfgCopy := cfg
	if r.grepTool != nil {
		r.grepTool.Scope = &cfgCopy
		r.grepTool.FileCount = fileCount
	}

}
