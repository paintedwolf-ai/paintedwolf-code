package toolhost

import (
	"fmt"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/native/mutation"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/summarize"
	"github.com/lycaon/lycaon/internal/survey"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/fileage"
	"github.com/lycaon/lycaon/internal/tools/native"
	skilltools "github.com/lycaon/lycaon/internal/tools/native/skills"
	surveytools "github.com/lycaon/lycaon/internal/tools/native/survey"
	"github.com/lycaon/lycaon/internal/toolschema"
)

type buildDeps struct {
	boundary      *sandbox.Boundary
	git           *git.Manager
	statusCache   *statusCacheBinding
	command       *hostcmd.Runner
	nativeConfig  nativemanifest.Config
	toolSchemas   *toolschema.Config
	surveyCatalog *survey.Catalog
	fileAge       *fileage.Provider
	rerank        decide.Reranker
}

type nativeToolServices struct {
	Survey    *SurveyServices
	Mutations *MutationServices
	Commands  *CommandServices
	Skills    *SkillServices
}

func buildNativeRegistry(deps buildDeps) (*tools.DefaultRegistry, *nativeToolServices, error) {
	reg, err := tools.NewCatalogRegistry(deps.toolSchemas)
	if err != nil {
		return nil, nil, err
	}
	boundary := deps.boundary

	register := func(name string, handler tools.ToolHandler) error {
		if !deps.nativeConfig.HasTool(name) {
			return nil
		}
		return reg.Register(name, handler)
	}

	catalog := sourcecatalog.Process()
	write := &native.WriteTool{Boundary: boundary}
	edit := &native.EditTool{Boundary: boundary}
	replaceLines := &native.ReplaceLinesTool{Boundary: boundary}
	codeRewrite := &native.CodeRewriteTool{Boundary: boundary}
	restoreVersion := &native.RestoreVersionTool{Boundary: boundary}
	summarizeCaps, err := summarize.LoadCaps()
	if err != nil {
		return nil, nil, fmt.Errorf("summarize caps: %w", err)
	}
	summarizeTool := &surveytools.SummarizeTool{Boundary: boundary, Caps: summarizeCaps, Catalog: catalog, Rerank: deps.rerank}
	jqEdit := &native.JqEditTool{Boundary: boundary}
	chmod := &native.ChmodTool{Boundary: boundary}
	deleteTool := &native.DeleteTool{Boundary: boundary}
	copyTool := &native.CopyTool{Boundary: boundary}
	moveTool := &native.MoveTool{Boundary: boundary}
	mkdirTool := &native.MkdirTool{Boundary: boundary}
	extractArchiveTool := &native.ExtractArchiveTool{Boundary: boundary}
	chownTool := &native.ChownTool{Boundary: boundary}
	surveyRepo := &survey.RepoTool{
		Boundary: boundary, BaseCatalog: deps.surveyCatalog, Caps: survey.DefaultCaps(), SourceCatalog: catalog,
	}
	var commandTool *command.CommandTool
	var verifyTool *native.VerifyTool
	var commandOutputTool *command.CommandOutputTool
	var commandStopTool *command.CommandStopTool

	if err := registerCommandOperations(reg, deps.nativeConfig); err != nil {
		return nil, nil, err
	}
	if err := registerCoreNativeTools(register, edit, replaceLines, codeRewrite, restoreVersion, summarizeTool, jqEdit, chmod, chownTool, surveyRepo); err != nil {
		return nil, nil, err
	}

	if deps.command != nil {
		if err := registerCommandTools(reg, deps.command, boundary, &commandTool, &verifyTool, &commandOutputTool, &commandStopTool); err != nil {
			return nil, nil, err
		}
	}

	if deps.nativeConfig.HasTool("git_restore") && deps.git != nil {
		restoreTool := &native.GitRestoreTool{Git: deps.git, Boundary: boundary}
		if err := reg.Register("git_restore", restoreTool.Run); err != nil {
			return nil, nil, err
		}
	}
	if deps.nativeConfig.HasTool("git_commit") && deps.git != nil {
		commitTool := &native.GitCommitTool{Git: deps.git, Boundary: boundary}
		if err := reg.Register("git_commit", commitTool.Run); err != nil {
			return nil, nil, err
		}
	}
	if err := registerGitOperationTools(reg, deps, boundary); err != nil {
		return nil, nil, err
	}
	skillsRead := &skilltools.SkillsReadTool{}
	if err := register("skills_read", skillsRead.Run); err != nil {
		return nil, nil, err
	}
	sourceHistory := &surveytools.SourceHistoryTool{Boundary: boundary}
	if err := register("source_history", sourceHistory.Run); err != nil {
		return nil, nil, err
	}
	if len(reg.List()) == 0 {
		return nil, nil, fmt.Errorf("no native tools registered")
	}
	return reg, &nativeToolServices{
		Survey:    &SurveyServices{summarizeTool: summarizeTool, fileAge: deps.fileAge, gitStatusCache: deps.statusCache},
		Mutations: &MutationServices{writeTool: write, editTool: edit, replaceLinesTool: replaceLines, codeRewriteTool: codeRewrite, restoreVersionTool: restoreVersion, jqEditTool: jqEdit},
		Commands: &CommandServices{commandTool: commandTool, verifyTool: verifyTool, commandOutputTool: commandOutputTool, commandStopTool: commandStopTool,
			owners: programOwnerTools{boundary: boundary, summarize: summarizeTool, repo: surveyRepo, catalog: catalog, git: deps.git, registry: reg,
				operations: mutation.Operations{Write: write, Delete: deleteTool, Move: moveTool, Copy: copyTool, Mkdir: mkdirTool, Chmod: chmod,
					Chown: chownTool, Times: &native.TimesTool{Boundary: boundary}, Extract: extractArchiveTool}}},
		Skills: &SkillServices{skillsReadTool: skillsRead},
	}, nil
}
