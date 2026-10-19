package loopguard

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/progress"
)

// DoomLoopMaxAttempts is the max model responses repeating identical tool+args
// before blocking.
const DoomLoopMaxAttempts = 8

// DoomLoopMaxSameCodeRejects bounds consecutive rejected responses with identical arguments.
const DoomLoopMaxSameCodeRejects = 2

// DoomLoopMaxCodeRepeats bounds a tool's rejected responses for one code.
const DoomLoopMaxCodeRepeats = 3

// DoomLoopGuard supplies repetition checks and observations for OAR.
type DoomLoopGuard interface {
	promptloop.DoomLoopGuard
	CodeRejectResponses(sessionID, tool, code string) int
	FruitlessSearchRun(sessionID, tool string, args map[string]any) int
	SetPageTargetResolver(func(sessionID, pageID string) string)
	SetReviewIntentResolver(func(context.Context, string) (string, error))
}

// doomLoopQuestionArgs identifies a search question independently of scope and pagination.
var doomLoopQuestionArgs = map[string][]string{
	"grep": {"pattern", "case_insensitive", "structural", "lang"},
	"find": {"name_glob", "pattern", "type"},
	// recall splits the same way: query is the question, widen and limit scope it.
	"recall": {"query"},
}

// doomLoopQuestionKey excludes scope and pagination from tracked search identity.
func doomLoopQuestionKey(tool string, args map[string]any) (string, bool) {
	name := strings.ToLower(strings.TrimSpace(tool))
	keys, ok := doomLoopQuestionArgs[name]
	if !ok {
		return "", false
	}
	question := make(map[string]any, len(keys))
	for _, key := range keys {
		if v, present := args[key]; present {
			question[key] = v
		}
	}
	if len(question) == 0 {
		return "", false
	}
	b, err := json.Marshal(question)
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256(append([]byte(name), b...))
	return fmt.Sprintf("%x", sum[:8]), true
}

// doomLoopFingerprintArgs excludes display copy and idle waits from invocation identity.
func doomLoopFingerprintArgs(tool string, args map[string]any) map[string]any {
	name := strings.ToLower(strings.TrimSpace(tool))
	switch name {
	case "summarize":
		if args == nil {
			return map[string]any{}
		}
		out := make(map[string]any, 4)
		for _, key := range []string{"path", "paths", "content", "pattern"} {
			if v, ok := args[key]; ok {
				out[key] = v
			}
		}
		return out
	case "capture_page", "measure_page", "page_act", "page_snapshot":
		return doomLoopVisualDriveArgs(args)
	case "render_view":
		return doomLoopRenderViewArgs(args)
	case "update_progress":
		return doomLoopUpdateProgressArgs(args)
	default:
		return args
	}
}

// doomLoopUpdateProgressArgs groups all-terminal checklists so rewording closed
// items cannot reset repetition tracking.
func doomLoopUpdateProgressArgs(args map[string]any) map[string]any {
	if args == nil {
		return map[string]any{}
	}
	content, _ := args["content"].(string)
	if progress.AllTerminal(content) {
		return map[string]any{"all_terminal": true}
	}
	return args
}

// doomLoopPageTargetArg groups reopened pages by target, using ids for unresolved handles.
func doomLoopPageTargetArg(tool string, args map[string]any, resolve func(string) string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "page_snapshot", "page_act", "page_close":
	default:
		return "", false
	}
	pageID, _ := args["id"].(string)
	pageID = strings.TrimSpace(pageID)
	if pageID == "" {
		return "", false
	}
	if resolve != nil {
		if target := strings.TrimSpace(resolve(pageID)); target != "" {
			return target, true
		}
	}
	return pageID, true
}

// doomLoopVisualDriveArgs retains the target and material actions, excluding captions and waits.
func doomLoopVisualDriveArgs(args map[string]any) map[string]any {
	if args == nil {
		return map[string]any{}
	}
	out := make(map[string]any, 6)
	for _, key := range []string{"url", "project_dir", "selector", "viewport", "selectors"} {
		if v, ok := args[key]; ok {
			out[key] = v
		}
	}
	if actions, ok := args["actions"]; ok {
		if material := doomLoopMaterialActions(actions); material != nil {
			out["actions"] = material
		}
	}
	return out
}

func doomLoopRenderViewArgs(args map[string]any) map[string]any {
	if args == nil {
		return map[string]any{}
	}
	out := make(map[string]any, 4)
	for _, key := range []string{"markup", "html", "svg", "viewport"} {
		if v, ok := args[key]; ok {
			out[key] = v
		}
	}
	return out
}

func doomLoopMaterialActions(raw any) any {
	list, ok := raw.([]any)
	if !ok || len(list) == 0 {
		return raw
	}
	for _, step := range list {
		m, ok := step.(map[string]any)
		if !ok {
			return raw
		}
		typ, _ := m["type"].(string)
		if !strings.EqualFold(strings.TrimSpace(typ), "wait") {
			return raw
		}
	}
	// Pure wait scripts are equivalent to a bare snapshot of the same target.
	return nil
}
