package modelcall

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
)

// ModelInputKind names what one serialized segment of a provider request carries.
type ModelInputKind string

const (
	ModelInputTools   ModelInputKind = "tools"
	ModelInputSystem  ModelInputKind = "system"
	ModelInputMessage ModelInputKind = "message"
)

// ModelInputSegment is one element of the model input as an adapter serialized
// it, with transport key order and moving cache markers normalized away. Text
// lives only as long as the capture of one request.
type ModelInputSegment struct {
	Kind   ModelInputKind
	Digest [sha256.Size]byte
	Text   []byte
}

// modelInputFields lists model input in the order providers consume it. Fields
// outside this list are transport controls and never count as input.
var modelInputFields = []struct {
	key  string
	kind ModelInputKind
}{
	{"tools", ModelInputTools},
	{"system", ModelInputSystem},
	{"instructions", ModelInputSystem},
	{"systemInstruction", ModelInputSystem},
	{"messages", ModelInputMessage},
	{"input", ModelInputMessage},
	{"contents", ModelInputMessage},
}

func modelInputSegments(wire map[string]json.RawMessage) []ModelInputSegment {
	var out []ModelInputSegment
	for _, field := range modelInputFields {
		raw, ok := wire[field.key]
		if !ok {
			continue
		}
		var elements []json.RawMessage
		if json.Unmarshal(raw, &elements) != nil {
			elements = []json.RawMessage{raw}
		}
		for _, element := range elements {
			text := normalizedModelInput(element)
			out = append(out, ModelInputSegment{Kind: field.kind, Digest: sha256.Sum256(text), Text: text})
		}
	}
	return out
}

func normalizedModelInput(raw json.RawMessage) []byte {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return raw
	}
	text, err := json.Marshal(withoutCacheMarkers(value))
	if err != nil {
		return raw
	}
	return text
}

func withoutCacheMarkers(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		delete(typed, "cache_control")
		delete(typed, "cachePoint")
		for key, child := range typed {
			typed[key] = withoutCacheMarkers(child)
		}
	case []any:
		for i, child := range typed {
			typed[i] = withoutCacheMarkers(child)
		}
	}
	return value
}

// ModelInput returns the latest dispatch's serialized model input.
func (c *RequestControlCapture) ModelInput() []ModelInputSegment {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.input
}
