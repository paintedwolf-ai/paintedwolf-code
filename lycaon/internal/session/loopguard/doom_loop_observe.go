package loopguard

import (
	"strings"

	"github.com/lycaon/lycaon/internal/oar"
)

const (
	doomLoopRepeatCode          = "DOOM_LOOP_REPEAT"
	doomLoopRepeatWarnCode      = "DOOM_LOOP_REPEAT_WARN"
	doomLoopFruitlessSearchCode = "DOOM_LOOP_FRUITLESS_SEARCH"
	doomLoopCodeRepeatCode      = "DOOM_LOOP_CODE_REPEAT"
)

// ObserveDoomLoopCodeRepeat reports the bounded rejected-response count and matching action.
func ObserveDoomLoopCodeRepeat(gc *oar.GuardContext, total int, tool, code, codeInstead string) {
	if gc == nil || total <= 0 || code == "" {
		return
	}
	gc.Invocation.Tool = tool
	gc.DeriveToolClassFacts()
	gc.SetCodeRejectResponses(int64(total))
	data := map[string]any{
		"tool":  tool,
		"code":  code,
		"count": total,
	}
	if instead := strings.TrimSpace(codeInstead); instead != "" {
		data["code_instead"] = instead
	}
	gc.PutRejectData(doomLoopCodeRepeatCode, data)
}

// ObserveDoomLoopRepeat publishes doom-loop hard-block facts.
func ObserveDoomLoopRepeat(gc *oar.GuardContext, count int, tool, repeatedCode string, args map[string]any) {
	if gc == nil {
		return
	}
	gc.Invocation.Tool = tool
	gc.DeriveToolClassFacts()
	gc.SetRepeatCount(int64(count))
	if repeatedCode != "" {
		gc.Counters.SameCodeRejectRun = int64(DoomLoopMaxSameCodeRejects)
	}
	data := map[string]any{
		"count":                count,
		"tool":                 tool,
		"deferred_unactivated": gc.Counters.DeferredUnactivated,
	}
	if repeatedCode != "" {
		data["code"] = repeatedCode
	}
	putCommandOutputRepeatData(data, tool, args)
	gc.PutRejectData(doomLoopRepeatCode, data)
}

// ObserveFruitlessSearch publishes the consecutive-fruitless-search run for the
// question the current args ask. run is the count the guard returned; tool and
// pattern go into the reject data so the banner can name what came up empty.
func ObserveFruitlessSearch(gc *oar.GuardContext, run int, tool, pattern string) {
	if gc == nil || run <= 0 {
		return
	}
	gc.Invocation.Tool = tool
	gc.DeriveToolClassFacts()
	gc.SetFruitlessSearchRun(int64(run))
	gc.PutRejectData(doomLoopFruitlessSearchCode, map[string]any{
		"tool":    tool,
		"pattern": pattern,
		"count":   run,
	})
}

// ObserveDoomLoopWarn publishes soft doom-loop warn facts.
func ObserveDoomLoopWarn(gc *oar.GuardContext, count, maxAttempts int, tool string, args map[string]any) {
	if gc == nil {
		return
	}
	gc.Invocation.Tool = tool
	gc.DeriveToolClassFacts()
	gc.SetRepeatCount(int64(count))
	data := map[string]any{
		"tool":                 tool,
		"count":                count,
		"max":                  maxAttempts,
		"deferred_unactivated": gc.Counters.DeferredUnactivated,
	}
	putCommandOutputRepeatData(data, tool, args)
	gc.PutRejectData(doomLoopRepeatWarnCode, data)
}

func putCommandOutputRepeatData(data map[string]any, tool string, args map[string]any) {
	if data == nil || strings.TrimSpace(tool) != "command_output" || args == nil {
		return
	}
	handle, _ := args["handle"].(string)
	if handle = strings.TrimSpace(handle); handle != "" {
		data["handle"] = handle
	}
}
