package tools

import (
	"context"
	"sort"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// SaveLaunchDrafts publishes person-authored drafts and reports any left unsaved.
func SaveLaunchDrafts(ctx context.Context, tc ToolContext) (toolcontract.LaunchDrafts, error) {
	program, inProgram := ProgramDraftsFrom(ctx)
	if inProgram {
		program.saving.Lock()
		defer program.saving.Unlock()
	}
	var saved EditorLaunchSave
	var err error
	if documents, ok := ProjectDocuments(tc); ok {
		saved, err = documents.SaveForAgentLaunch(ctx, tc.Identity.ProjectID, tc.Source.ProjectSourceBranch, DraftSaveCall(ctx, tc), program.Pinned)
	}
	if inProgram && program.observeLaunch != nil && (err == nil || saved.Published) {
		program.observeLaunch(ctx, saved.Published)
	}
	if err != nil {
		return toolcontract.LaunchDrafts{}, err
	}
	unsaved := make([]toolcontract.UnsavedDocument, 0, len(saved.Unsaved))
	for _, document := range saved.Unsaved {
		unsaved = append(unsaved, toolcontract.UnsavedDocument{
			Path:  projectroot.DisplayPath(tc.Source.Roots, tc.Source.ActiveRootID, document.RootID, document.Path),
			Cause: document.Cause,
		})
	}
	if inProgram {
		program.launchSaved(unsaved)
	}
	return boundLaunchDrafts(unsaved), nil
}

// DraftSaveCall attributes a save inside a program to its invoking tool.
func DraftSaveCall(ctx context.Context, tc ToolContext) EditorAgentCall {
	call := EditorAgentCall{SessionID: tc.Identity.SessionID, Turn: tc.Identity.UserTurn, ToolCallID: tc.Identity.ToolCallID, ToolName: tc.Invocation.ToolName}
	if drafts, ok := ProgramDraftsFrom(ctx); ok {
		call.ToolName = drafts.tool
	}
	return call
}

func boundLaunchDrafts(unsaved []toolcontract.UnsavedDocument) toolcontract.LaunchDrafts {
	if len(unsaved) == 0 {
		return toolcontract.LaunchDrafts{}
	}
	sort.Slice(unsaved, func(i, j int) bool { return unsaved[i].Path < unsaved[j].Path })
	if len(unsaved) <= toolcontract.MaxUnsavedDocuments {
		return toolcontract.LaunchDrafts{UnsavedDocuments: unsaved}
	}
	return toolcontract.LaunchDrafts{UnsavedDocuments: unsaved[:toolcontract.MaxUnsavedDocuments], OmittedUnsavedDocuments: len(unsaved) - toolcontract.MaxUnsavedDocuments}
}
