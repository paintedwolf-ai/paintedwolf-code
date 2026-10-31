package toolhost

import (
	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/survey"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	nativejq "github.com/lycaon/lycaon/internal/tools/native/jq"
	surveytools "github.com/lycaon/lycaon/internal/tools/native/survey"
)

// registerCommandOperations publishes the contracts command programs bind for
// work that has no model-callable tool.
func registerCommandOperations(reg *tools.DefaultRegistry, config nativemanifest.Config) error {
	names, err := toolcontract.CommandOperations()
	if err != nil {
		return err
	}
	for _, name := range names {
		if !config.HasTool(name) {
			continue
		}
		if err := reg.RegisterOperation(name); err != nil {
			return err
		}
	}
	return nil
}

func registerCoreNativeTools(
	register func(name string, handler tools.ToolHandler) error,
	edit *native.EditTool,
	replaceLines *native.ReplaceLinesTool,
	codeRewrite *native.CodeRewriteTool,
	restoreVersion *native.RestoreVersionTool,
	summarizeTool *surveytools.SummarizeTool,
	jqEdit *native.JqEditTool,
	chmod *native.ChmodTool,
	chownTool *native.ChownTool,
	surveyRepo *survey.RepoTool,
) error {
	for _, pair := range []struct {
		name    string
		handler tools.ToolHandler
	}{
		{"edit", edit.Run},
		{"replace_lines", replaceLines.Run},
		{"code_rewrite", codeRewrite.Run},
		{"restore_version", restoreVersion.Run},
		{"summarize", summarizeTool.Run},
		{nativejq.EditToolName, jqEdit.Run},
		{"chmod", chmod.Run},
		{"chown", chownTool.Run},
	} {
		if err := register(pair.name, pair.handler); err != nil {
			return err
		}
	}
	return register("survey_repo", surveyRepo.Run)
}
