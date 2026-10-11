package providerwire

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/pkg/api"
)

// PerceptionOutcome says whether a tool-result image reached the model.
type PerceptionOutcome string

const (
	// PerceptionAttached means the pixels ride this request.
	PerceptionAttached PerceptionOutcome = "attached"
	// PerceptionNoVisionInput means the model accepts no image input.
	PerceptionNoVisionInput PerceptionOutcome = "model_lacks_vision"
	// PerceptionOutsideWindow means newer images filled the perception window.
	PerceptionOutsideWindow PerceptionOutcome = "outside_window"
	// PerceptionBytesUnavailable means the stored bytes could not be read.
	PerceptionBytesUnavailable PerceptionOutcome = "bytes_unavailable"
	// PerceptionNotEncodable means the bytes are not an image a transport accepts.
	PerceptionNotEncodable PerceptionOutcome = "not_encodable"
)

// perceptionFactSource labels the host fact appended to a tool result that
// carried an image.
const perceptionFactSource = "perception"

// PrepareMessagesForVision decides, for every tool result with a
// perceive-flagged image, whether this request attaches its pixels. An
// attached image holds normalized inline bytes a transport sends as is.
// Either way the tool result gains a host fact naming the outcome.
func PrepareMessagesForVision(messages []api.Message, vision bool, sessionID string) []api.Message {
	candidates := perceptionCandidates(messages)
	if len(candidates) == 0 {
		return messages
	}
	out := make([]api.Message, len(messages))
	copy(out, messages)
	dropped := CurrentPerceptionWindow().Dropped(len(candidates))
	for rank, i := range candidates {
		visual, outcome := perceiveToolVisual(out[i].ToolResult.Visual, vision, rank < dropped, sessionID)
		if outcome != PerceptionAttached && outcome != PerceptionNoVisionInput {
			perceiveDropLog.Debug("tool image not attached", "artifact_id", visual.ID, "outcome", string(outcome))
		}
		tr := *out[i].ToolResult
		tr.Visual = visual
		out[i].ToolResult = &tr
		out[i].Content = withPerceptionFact(out[i], visual.ID, outcome)
	}
	return out
}

// perceptionCandidates lists, oldest first, the messages whose tool result
// carries an image meant for the model.
func perceptionCandidates(messages []api.Message) []int {
	var out []int
	for i, m := range messages {
		if m.ToolResult != nil && m.ToolResult.Visual != nil && m.ToolResult.Visual.Perceive {
			out = append(out, i)
		}
	}
	return out
}

func perceiveToolVisual(src *api.VisualArtifact, vision, outsideWindow bool, sessionID string) (*api.VisualArtifact, PerceptionOutcome) {
	v := *src
	detach := func(outcome PerceptionOutcome) (*api.VisualArtifact, PerceptionOutcome) {
		v.Bytes = nil
		v.Perceive = false
		return &v, outcome
	}
	switch {
	case !vision:
		return detach(PerceptionNoVisionInput)
	case outsideWindow:
		return detach(PerceptionOutsideWindow)
	}
	raw, mime := v.Bytes, v.Mime
	resolve := visualResolver()
	if len(raw) == 0 && v.StoreRef && resolve != nil {
		var ok bool
		if raw, mime, ok = resolve(sessionID, v.ID); !ok {
			raw = nil
		}
	}
	if len(raw) == 0 {
		return detach(PerceptionBytesUnavailable)
	}
	raw, mime, err := perceptionImage(raw, mime)
	if err != nil {
		return detach(PerceptionNotEncodable)
	}
	normalized, err := NormalizeImageBytes(raw, mime, wireImageCeiling())
	if err != nil {
		return detach(PerceptionNotEncodable)
	}
	v.Bytes = normalized.Bytes
	v.Mime = normalized.Mime
	v.StoreRef = false
	return &v, PerceptionAttached
}

// withPerceptionFact appends the outcome to the tool result text as host data.
func withPerceptionFact(m api.Message, artifactID string, outcome PerceptionOutcome) string {
	content := m.Content
	if content == "" && m.ToolResult != nil {
		content = m.ToolResult.Content
	}
	fact := struct {
		Image      string            `json:"image"`
		Reason     PerceptionOutcome `json:"reason,omitempty"`
		ArtifactID string            `json:"artifact_id,omitempty"`
	}{Image: "not_attached", Reason: outcome, ArtifactID: strings.TrimSpace(artifactID)}
	if outcome == PerceptionAttached {
		fact.Image, fact.Reason = string(PerceptionAttached), ""
	}
	body, err := json.Marshal(fact)
	if err != nil {
		return content
	}
	part := transcript.ProjectContentParts(api.MessageRoleTool, []api.MessageContentPart{{
		Content: string(body), Origin: api.MessageOriginHost,
		Authority: api.ContentAuthorityNone, TrustTier: api.ContentTrustTierTrusted,
		Source: perceptionFactSource,
	}})
	if strings.TrimSpace(content) == "" {
		return part
	}
	return content + "\n\n" + part
}

// ToolResultImage returns the image a prepared tool result attaches.
func ToolResultImage(m api.Message) (ImageWirePart, bool) {
	if m.ToolResult == nil || m.ToolResult.Visual == nil {
		return ImageWirePart{}, false
	}
	v := m.ToolResult.Visual
	if !v.Perceive || v.StoreRef || len(v.Bytes) == 0 || !IsWireImageMime(v.Mime) {
		return ImageWirePart{}, false
	}
	return ImageWirePart{
		Mime:   canonicalImageMime(v.Mime),
		Base64: base64.StdEncoding.EncodeToString(v.Bytes),
	}, true
}

// toolImageSource labels a tool-result image sent apart from its tool message.
const toolImageSource = "image"

// ToolImageCaption labels, as tool data, an image a transport sends in a user
// message after its tool result, naming the call it came from.
func ToolImageCaption(m api.Message, wireCallID string) string {
	caption := struct {
		ToolCallID string `json:"tool_call_id,omitempty"`
		Tool       string `json:"tool,omitempty"`
		ArtifactID string `json:"artifact_id,omitempty"`
	}{ToolCallID: strings.TrimSpace(wireCallID)}
	if m.ToolResult != nil {
		caption.Tool = strings.TrimSpace(m.ToolResult.Tool)
		if m.ToolResult.Visual != nil {
			caption.ArtifactID = strings.TrimSpace(m.ToolResult.Visual.ID)
		}
	}
	body, err := json.Marshal(caption)
	if err != nil {
		return ""
	}
	return transcript.ProjectContentParts(api.MessageRoleUser, []api.MessageContentPart{{
		Content: string(body), Origin: api.MessageOriginTool,
		Authority: api.ContentAuthorityNone, TrustTier: api.ContentTrustTierUntrusted,
		Source: toolImageSource,
	}})
}
