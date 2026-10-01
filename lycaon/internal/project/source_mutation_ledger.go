package project

import (
	"strings"

	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/pkg/api"
)

func (p sourceMutationPlan) ledgerInput(operationID string) sourceledger.RecordInput {
	in := sourceledger.RecordInput{
		ProjectID: p.ProjectID, BranchID: p.BranchID,
		RootID: p.RootID, Path: p.Path, Origin: api.SourceChangeOriginUser, PersonID: p.PersonID,
		SessionID: strings.TrimSpace(p.SessionID), Turn: p.Turn, OperationID: operationID,
		EntryKind: sourceledger.EntryKindFile, FileID: p.FileID,
		DerivedFromVersionID: p.DerivedFromVersionID, Cause: p.Cause,
	}
	if p.Agent != nil {
		in.Origin, in.PersonID = api.SourceChangeOriginAgent, ""
		in.JobID, in.ToolCallID, in.ToolName = p.Agent.JobID, p.Agent.ToolCallID, p.Agent.ToolName
	}
	if p.EntryKind == SourceEntryFolder {
		in.EntryKind = sourceledger.EntryKindDirectory
	}
	switch p.Kind {
	case "write":
		in.Op, in.Before, in.After, in.BeforeSHA256, in.AfterSHA256 = api.SourceChangeOpWrite, p.Before, p.After, p.BaseSHA256, p.AfterSHA
		in.BeforeSize, in.AfterSize = int64(len(p.Before)), int64(len(p.After))
	case "create", "copy", "restore":
		in.Op = api.SourceChangeOpCreate
		in.After, in.AfterSHA256, in.AfterSize = p.After, p.AfterSHA, p.AfterSize
		if p.Kind == "create" && p.EntryKind == SourceEntryFile && p.AfterSHA == "" {
			in.After, in.AfterSHA256 = []byte{}, textfile.SHA256(nil)
		}
	case "rename":
		in.Op, in.FromPath = api.SourceChangeOpRename, p.FromPath
		in.Before, in.After = p.Before, p.After
		in.BeforeSHA256, in.AfterSHA256 = p.BaseSHA256, p.AfterSHA
		in.BeforeSize, in.AfterSize = p.BeforeSize, p.AfterSize
	case "delete":
		in.Op = api.SourceChangeOpDelete
		in.Before, in.BeforeSHA256, in.BeforeSize = p.Before, p.BaseSHA256, p.BeforeSize
	}
	return in
}

func (p sourceMutationPlan) ledgerInputs(operationID string) []sourceledger.RecordInput {
	if p.AgentEffect != nil {
		return []sourceledger.RecordInput{p.AgentEffect.Record}
	}
	if p.Kind != "batch_write" {
		return []sourceledger.RecordInput{p.ledgerInput(operationID)}
	}
	inputs := make([]sourceledger.RecordInput, 0, len(p.Writes))
	for _, write := range p.Writes {
		if !write.Changed {
			continue
		}
		in := write.ledgerInput(operationID)
		in.BatchID = p.BatchID
		inputs = append(inputs, in)
	}
	return inputs
}

// sourceChanges flattens one plan into file changes.
func (p sourceMutationPlan) sourceChanges() []sourcefeed.Change {
	if p.AgentEffect != nil {
		return []sourcefeed.Change{p.AgentEffect.Change}
	}
	if p.Kind == "batch_write" {
		changes := make([]sourcefeed.Change, 0, len(p.Writes))
		for _, write := range p.Writes {
			if write.Changed {
				changes = append(changes, write.sourceChanges()...)
			}
		}
		return changes
	}
	absPath := p.AbsPath
	if absPath == "" {
		absPath = p.ToAbs
	}
	change := sourcefeed.Change{ProjectID: p.ProjectID, WorkspaceID: p.WorkspaceID,
		WorkspaceKind: api.SourceWorkspaceKindProject, RootID: p.RootID, Path: p.Path,
		Origin: api.SourceChangeOriginUser, SessionID: strings.TrimSpace(p.SessionID), Turn: p.Turn, AbsPath: absPath}
	if p.Agent != nil {
		change.Origin, change.JobID, change.ToolCallID = api.SourceChangeOriginAgent, p.Agent.JobID, p.Agent.ToolCallID
		if p.Agent.WorkspaceKind != "" {
			change.WorkspaceKind = p.Agent.WorkspaceKind
		}
	}
	switch p.Kind {
	case "write":
		change.Op, change.AfterSHA256 = api.SourceChangeOpWrite, p.AfterSHA
	case "create", "restore":
		change.Op = api.SourceChangeOpCreate
		isDir := p.EntryKind == SourceEntryFolder
		change.IsDir = &isDir
	case "copy":
		change.Op = api.SourceChangeOpCreate
	case "rename":
		change.Op, change.FromPath = api.SourceChangeOpRename, p.FromPath
		change.AbsPath, change.FromAbsPath = p.ToAbs, p.FromAbs
	case "delete":
		change.Op = api.SourceChangeOpDelete
	}
	return []sourcefeed.Change{change}
}
