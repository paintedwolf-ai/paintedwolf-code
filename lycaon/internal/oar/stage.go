package oar

import "strings"

// Stage groups anchors for rendering and conformance reports.
type Stage string

const (
	StagePreInvoke   Stage = "pre_invoke"
	StageToolHandler Stage = "tool_handler"
	StagePostTool    Stage = "post_tool"
	StagePostTurn    Stage = "post_turn"
	StageFinalize    Stage = "finalize"
)

// StageFromAnchor returns the lifecycle stage for an anchor.
func StageFromAnchor(anchor string) Stage {
	anchor = strings.TrimSpace(anchor)
	switch anchor {
	case AnchorToolHandler, AnchorToolRejected:
		return StageToolHandler
	case AnchorToolPost:
		return StagePostTool
	case AnchorCoordinatorPostTurn, AnchorCoordinatorCloseoutCheck, CoreAnchorAgentPostTurn:
		return StagePostTurn
	case AnchorWorkerFinalize, AnchorWorkerReportCheck, CoreAnchorAgentFinalize:
		return StageFinalize
	case AnchorToolPreInvoke, AnchorCoordinatorPreInvoke, AnchorSessionPreInvoke:
		return StagePreInvoke
	default:
		return StagePreInvoke
	}
}
