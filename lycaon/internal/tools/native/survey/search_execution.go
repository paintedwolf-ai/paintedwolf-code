package survey

import (
	"context"
	"errors"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools/safecmd"
	"github.com/lycaon/lycaon/pkg/api"
)

func compileSurveyGlob(argument, pattern string) (sandbox.EntryGlob, error) {
	filter, err := sandbox.CompileEntryGlob(pattern)
	if err != nil {
		return sandbox.EntryGlob{}, safecmd.Reject("SURVEY_GLOB_INVALID", map[string]any{
			"argument": argument, "pattern": pattern, "detail": err.Error(),
		})
	}
	return filter, nil
}

func grepExecutionError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return safecmd.Reject("GREP_DEADLINE_EXCEEDED", map[string]any{
			"timeout_ms": safecmd.GrepTimeout.Milliseconds(),
		})
	}
	return err
}

func (s *grepSearch) publishProgress(phase string) {
	if s.tctx.ReportProgress != nil {
		s.tctx.ReportProgress(api.ToolProgress{
			Phase: phase, FilesSearched: s.textScanned + s.prefilterSkipped,
			FilesSkipped: s.binarySkipped + s.unreadable, Matches: s.skipped + len(s.resp.Matches),
		})
	}
}
