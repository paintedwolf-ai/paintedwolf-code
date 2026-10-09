package toolexecution

import (
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/tools"
)

func (e *Boundary) preInvokePolicyContext(
	tool, profileID string,
	args map[string]any,
	tc tools.ToolContext,
	confReq confine.Request,
) platform.PolicyContext {
	policyEval := platform.PolicyContext{
		ProfileID:               profileID,
		ToolAccess:              tc.ToolAccess,
		ToolName:                tool,
		ToolArgs:                args,
		ResolvedFiles:           ResolvedApprovalFiles(tool, args, tc),
		HostResources:           append([]string(nil), tc.HostResources...),
		HostResourceFamilies:    append([]string(nil), tc.HostResourceFamilies...),
		ProjectID:               tc.ProjectID,
		ProjectDir:              tc.ActiveRootPath(),
		SessionID:               tc.SessionID,
		ActionID:                tc.ToolCallID,
		ParentSessionID:         tc.ParentSessionID,
		RootSessionID:           tc.ChatSessionID(),
		ConfineRequest:          confReq,
		SocketGrants:            append([]confine.SocketGrant(nil), tc.SocketGrants...),
		SocketScopes:            append([]string(nil), tc.SocketScopes...),
		SocketGrantStates:       append([]string(nil), tc.SocketGrantStates...),
		AuthorizedSocketDigests: append([]string(nil), tc.AuthorizedSocketDigests...),
		AuthorizedDirectIP:      tc.DirectIPAuthorized,
		DirectIPRequested:       tc.DirectIPRequested,
		DirectIPVisibility:      directIPVisibility(tc.DirectIPRequested),
		DeclaredDestinations:    append([]string(nil), tc.DirectIPDeclared...),
		PackageExecution:        tc.PackageExecution,
	}
	if e.Metadata.registry != nil {
		if meta, ok := e.Metadata.registry.Meta(tool); ok {
			policyEval.ApprovalCategory = meta.ApprovalCategory
			policyEval.ApprovalSubject = meta.ApprovalSubject
		}
	}
	return policyEval
}
