package tools

import (
	"context"
	"sort"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// SaveLaunchDrafts saves the person's unsaved documents before a process the
// call launches reads the tree from disk, and returns the drafts it could not
// save. Inside a program they also join the program's result.
func SaveLaunchDrafts(ctx context.Context, tc ToolContext) (toolcontract.LaunchDrafts, error) {
	documents, ok := ProjectDocuments(tc)
	if !ok {
		return toolcontract.LaunchDrafts{}, nil
	}
	left, err := documents.SaveForAgentLaunch(ctx, tc.Identity.ProjectID, tc.Source.ProjectSourceBranch, DraftSaveCall(ctx, tc))
	if err != nil {
		return toolcontract.LaunchDrafts{}, err
	}
	unsaved := make([]toolcontract.UnsavedDocument, 0, len(left))
	for _, document := range left {
		unsaved = append(unsaved, toolcontract.UnsavedDocument{
			Path:  projectroot.DisplayPath(tc.Source.Roots, tc.Source.ActiveRootID, document.RootID, document.Path),
			Cause: document.Cause,
		})
	}
	if drafts, ok := ProgramDraftsFrom(ctx); ok {
		drafts.launchSaved(unsaved)
	}
	return boundLaunchDrafts(unsaved), nil
}

// DraftSaveCall names the call that causes a document save: the program's
// tool inside a program, whose children carry operation names.
func DraftSaveCall(ctx context.Context, tc ToolContext) EditorAgentCall {
	call := EditorAgentCall{SessionID: tc.Identity.SessionID, Turn: tc.Identity.UserTurn, ToolCallID: tc.Identity.ToolCallID, ToolName: tc.Invocation.ToolName}
	if drafts, ok := ProgramDraftsFrom(ctx); ok {
		call.ToolName = drafts.tool
	}
	return call
}

func boundLaunchDrafts(unsaved []toolcontract.UnsavedDocument) toolcontract.LaunchDrafts {
	sort.Slice(unsaved, func(i, j int) bool { return unsaved[i].Path < unsaved[j].Path })
	if len(unsaved) <= toolcontract.MaxUnsavedDocuments {
		return toolcontract.LaunchDrafts{UnsavedDocuments: unsaved}
	}
	return toolcontract.LaunchDrafts{UnsavedDocuments: unsaved[:toolcontract.MaxUnsavedDocuments], OmittedUnsavedDocuments: len(unsaved) - toolcontract.MaxUnsavedDocuments}
}
