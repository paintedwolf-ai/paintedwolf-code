package providerwire

import (
	"crypto/sha256"
	"encoding/json"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/pkg/api"
)

// Identities describe the prepared host projection, not the provider's private
// tokenizer or cache key. Only digests survive the observation.
type promptCacheIdentity struct {
	standing, tools, controls [sha256.Size]byte
	history                   [][sha256.Size]byte
	valid                     bool
	retried                   bool
}

func cacheRequestIdentity(req modelcall.CompletionRequest, policy providerprofile.PromptCachePolicy) promptCacheIdentity {
	var out promptCacheIdentity
	standingEnd, historyEnd := SystemPreambleEnd(req.Messages), 0
	marked := false
	for i, m := range req.Messages {
		switch m.PromptCacheBreakpoint {
		case api.PromptCacheTierNone:
		case api.PromptCacheTierStanding:
			standingEnd = i + 1
			marked = true
		case api.PromptCacheTierHistory:
			historyEnd = i + 1
		}
	}
	if !marked && historyEnd == 0 {
		historyEnd = len(req.Messages)
	}
	h := sha256.New()
	encoder := json.NewEncoder(h)
	for i, m := range req.Messages[:max(standingEnd, historyEnd)] {
		// Projection metadata and moving cache markers are not model content.
		content := struct {
			Role      api.MessageRole
			Content   string
			Calls     []api.ToolCall
			Result    *api.ToolResult
			Artifacts []string
			Reasoning *api.ModelReasoning
		}{m.Role, m.Content, cacheToolCalls(m.ToolCalls), cacheToolResult(m.ToolResult), m.ArtifactIDs, m.ModelReasoning}
		if encoder.Encode(content) != nil { //nolint:errchkjson // Dynamic tool payloads are checked at the cache boundary.
			return out
		}
		var digest [sha256.Size]byte
		copy(digest[:], h.Sum(nil))
		if i+1 == standingEnd {
			out.standing = digest
		}
		if i < historyEnd {
			out.history = append(out.history, digest)
		}
	}
	type schema struct {
		Name, Description string
		Args              map[string]any
	}
	schemas := make([]schema, len(req.Tools))
	for i, tool := range req.Tools {
		schemas[i] = schema{tool.Name, tool.Description, tool.ArgsSchema}
	}
	var ok bool
	if out.tools, ok = cacheDigest(schemas); !ok {
		return out
	}
	controls := req.ControlCapture.Snapshot()
	out.retried = len(controls) > 1
	out.controls, out.valid = cacheDigest(struct {
		Policy    providerprofile.PromptCachePolicy
		Think     modelcall.ThinkLevel
		Override  *modelcall.ThinkingOverride
		Strict    bool
		MaxTokens int
		Format    *modelcall.ResponseFormat
		Wire      []map[string]json.RawMessage
	}{policy, req.Think, req.ThinkingOverride, req.StrictBudget, req.MaxTokens, req.ResponseFormat, controls})
	out.valid = out.valid && len(req.Messages) > 0
	return out
}

func cacheDigest(value any) ([sha256.Size]byte, bool) {
	h := sha256.New()
	if json.NewEncoder(h).Encode(value) != nil { //nolint:errchkjson // Callers treat an unsupported value as an invalid identity.
		return [sha256.Size]byte{}, false
	}
	var out [sha256.Size]byte
	copy(out[:], h.Sum(nil))
	return out, true
}

// Keep UI receipts and storage references out of the model-content comparison.
func cacheToolCalls(calls []api.ToolCall) []api.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]api.ToolCall, len(calls))
	for i, call := range calls {
		out[i] = api.ToolCall{Name: call.Name, ID: call.ID, WireID: call.WireID, Args: call.Args, ExtraContent: call.ExtraContent}
	}
	return out
}

func cacheToolResult(result *api.ToolResult) *api.ToolResult {
	if result == nil {
		return nil
	}
	out := &api.ToolResult{Content: result.Content, Tool: result.Tool, ToolCallID: result.ToolCallID}
	if v := result.Visual; v != nil && v.Perceive && !v.StoreRef && len(v.Bytes) > 0 && IsWireImageMime(v.Mime) {
		out.Visual = &api.VisualArtifact{ID: v.ID, Mime: canonicalImageMime(v.Mime), Bytes: v.Bytes, Perceive: true}
	}
	return out
}
