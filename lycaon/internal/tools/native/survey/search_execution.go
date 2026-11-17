package survey

import (
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools/safecmd"
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
