package tools

import (
	"github.com/lycaon/lycaon/internal/toolrejection"

	"github.com/lycaon/lycaon/internal/isolation"
)

func FinalizeDirectIPForSpawn(tctx ToolContext) *toolrejection.ToolReject {
	if !tctx.Direct.DirectIPRequested {
		return nil
	}
	if tctx.Direct.DirectIPCapabilityRuntime == nil {
		return &toolrejection.ToolReject{Code: isolation.CodeDirectIPAuthorizationChanged, Data: map[string]any{
			"reason": "missing direct IP capability runtime",
		}}
	}
	ok, err := tctx.Direct.DirectIPCapabilityRuntime.ConsumePermit(tctx.Identity.SessionID, tctx.Identity.ToolCallID, tctx.Direct.DirectIPActionDigest, tctx.Direct.DirectIPRequestDigest, tctx.Direct.DirectIPConfineDigest)
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
