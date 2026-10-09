package tools

import (
	"github.com/lycaon/lycaon/internal/toolrejection"

	"github.com/lycaon/lycaon/internal/isolation"
)

func FinalizeDirectIPForSpawn(tctx ToolContext) *toolrejection.ToolReject {
	if !tctx.DirectIPRequested {
		return nil
	}
	if tctx.DirectIPCapabilityRuntime == nil {
		return &toolrejection.ToolReject{Code: isolation.CodeDirectIPAuthorizationChanged, Data: map[string]any{
			"reason": "missing direct IP capability runtime",
		}}
	}
	ok, err := tctx.DirectIPCapabilityRuntime.ConsumePermit(tctx.SessionID, tctx.ToolCallID, tctx.DirectIPActionDigest, tctx.DirectIPRequestDigest, tctx.DirectIPConfineDigest)
	if err != nil {
		return &toolrejection.ToolReject{Code: isolation.CodeDirectIPAuthorizationChanged, Data: map[string]any{
			"reason": err.Error(),
		}}
	}
	if !ok {
		return &toolrejection.ToolReject{Code: isolation.CodeDirectIPAuthorizationChanged, Data: map[string]any{
			"reason": "missing current-call direct IP permit",
		}}
	}
	return nil
}
