package openaicompat

import (
	"github.com/lycaon/lycaon/internal/llm/providerwire"
)

// Google's documented marker represents imported history, not model reasoning.
const googleImportedThoughtSignature = "skip_thought_signature_validator"

func projectGoogleToolHistory(calls []toolCallWire) {
	if len(calls) == 0 {
		return
	}
	// A parallel response signs only its first call. Preserve the whole group.
	for _, call := range calls {
		google, _ := call.ExtraContent["google"].(map[string]any)
		if signature, _ := google["thought_signature"].(string); signature != "" {
			return
		}
	}
	extra := providerwire.CloneMetadata(calls[0].ExtraContent)
	if extra == nil {
		extra = make(map[string]any)
	}
	google, _ := extra["google"].(map[string]any)
	google = providerwire.CloneMetadata(google)
	if google == nil {
		google = make(map[string]any)
	}
	google["thought_signature"] = googleImportedThoughtSignature
	extra["google"] = google
	calls[0].ExtraContent = extra
}
