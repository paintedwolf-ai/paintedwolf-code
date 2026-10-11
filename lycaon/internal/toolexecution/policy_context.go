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
		ToolAccess:              tc.Turn.ToolAccess,
		ToolName:                tool,
		ToolArgs:                args,
		ResolvedFiles:           ResolvedApprovalFiles(tool, args, tc),
		HostResources:           append([]string(nil), tc.Host.HostResources...),
		HostResourceFamilies:    append([]string(nil), tc.Host.HostResourceFamilies...),
		ProjectID:               tc.Identity.ProjectID,
		ProjectDir:              tc.ActiveRootPath(),
		SessionID:               tc.Identity.SessionID,
		ActionID:                tc.Identity.ToolCallID,
		ParentSessionID:         tc.Identity.ParentSessionID,
		RootSessionID:           tc.ChatSessionID(),
		ConfineRequest:          confReq,
		SocketGrants:            append([]confine.SocketGrant(nil), tc.Socket.SocketGrants...),
		SocketScopes:            append([]string(nil), tc.Socket.SocketScopes...),
		SocketGrantStates:       append([]string(nil), tc.Socket.SocketGrantStates...),
		AuthorizedSocketDigests: append([]string(nil), tc.Socket.AuthorizedSocketDigests...),
		AuthorizedDirectIP:      tc.Direct.DirectIPAuthorized,
		DirectIPRequested:       tc.Direct.DirectIPRequested,
		DirectIPVisibility:      directIPVisibility(tc.Direct.DirectIPRequested),
		DeclaredDestinations:    append([]string(nil), tc.Direct.DirectIPDeclared...),
		PackageExecution:        tc.Files.PackageExecution,
	}
	if e.Metadata.registry != nil {
		if meta, ok := e.Metadata.registry.Meta(tool); ok {
			policyEval.ApprovalCategory = meta.ApprovalCategory
			policyEval.ApprovalSubject = meta.ApprovalSubject
		}
	}
	return policyEval
}
