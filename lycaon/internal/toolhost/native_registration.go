package toolhost

import (
	"github.com/lycaon/lycaon/internal/survey"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	nativejq "github.com/lycaon/lycaon/internal/tools/native/jq"
	surveytools "github.com/lycaon/lycaon/internal/tools/native/survey"
)

func registerCoreNativeTools(
	register func(name string, handler tools.ToolHandler) error,
	read *surveytools.ReadTool,
	write *native.WriteTool,
	edit *native.EditTool,
	replaceLines *native.ReplaceLinesTool,
	codeRewrite *native.CodeRewriteTool,
	restoreVersion *native.RestoreVersionTool,
	find *surveytools.FindTool,
	summarizeTool *surveytools.SummarizeTool,
	grep *surveytools.GrepTool,
	jqTool *nativejq.Tool,
	jqEdit *native.JqEditTool,
	stat *surveytools.StatTool,
	wc *surveytools.WcTool,
	listDir *surveytools.ListDirTool,
	chmod *native.ChmodTool,
	deleteTool *native.DeleteTool,
	copyTool *native.CopyTool,
	moveTool *native.MoveTool,
	mkdirTool *native.MkdirTool,
	diffTool *surveytools.DiffTool,
	extractArchiveTool *native.ExtractArchiveTool,
	chownTool *native.ChownTool,
	surveyRepo *survey.RepoTool,
) error {
	for _, pair := range []struct {
		name    string
		handler tools.ToolHandler
	}{
		{"read", read.Run},
		{"write", write.Run},
		{"edit", edit.Run},
		{"replace_lines", replaceLines.Run},
		{"code_rewrite", codeRewrite.Run},
		{"restore_version", restoreVersion.Run},
		{"find", find.Run},
		{"summarize", summarizeTool.Run},
		{"grep", grep.Run},
		{nativejq.ToolName, jqTool.Run},
		{nativejq.EditToolName, jqEdit.Run},
		{"stat", stat.Run},
		{"wc", wc.Run},
		{"list_dir", listDir.Run},
		{"chmod", chmod.Run},
		{"delete", deleteTool.Run},
		{"copy", copyTool.Run},
		{"move", moveTool.Run},
		{"mkdir", mkdirTool.Run},
		{"chown", chownTool.Run},
		{"diff", diffTool.Run},
		{"extract_archive", extractArchiveTool.Run},
	} {
		if err := register(pair.name, pair.handler); err != nil {
			return err
		}
	}
	return register("survey_repo", surveyRepo.Run)
}
