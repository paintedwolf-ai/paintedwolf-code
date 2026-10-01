package modelfeed

import "strings"

// imageFamilies covers image-only families with incomplete modality metadata.
var imageFamilies = map[string]struct{}{
	"dall-e":           {},
	"flux":             {},
	"gpt-image":        {},
	"ideogram":         {},
	"imagen":           {},
	"lucid":            {},
	"nano-banana":      {},
	"recraft":          {},
	"stable-diffusion": {},
	"topazlabs":        {},
}

// nonConversationFamilies identifies non-chat endpoints by structured family metadata.
var nonConversationFamilies = map[string]struct{}{
	"codestral-embed":  {},
	"cohere-embed":     {},
	"gemini-embedding": {},
	"lyria":            {},
	"mistral-embed":    {},
	"text-embedding":   {},
	"titan-embed":      {},
}

// ConversationEligible requires text output and assistant capability metadata.
func ConversationEligible(m Model) bool {
	if !outputContainsText(m.Modalities.Output) {
		return false
	}
	if imagePrimary(m) {
		return false
	}
	if nonConversationFamily(m) {
		return false
	}
	if !assistantSignal(m) {
		return false
	}
	return true
}

func nonConversationFamily(m Model) bool {
	family := strings.ToLower(strings.TrimSpace(m.Family))
	_, exists := nonConversationFamilies[family]
	return exists
}

func outputContainsText(outputs []string) bool {
	for _, o := range outputs {
		if strings.EqualFold(strings.TrimSpace(o), "text") {
			return true
		}
	}
	return false
}

func imagePrimary(m Model) bool {
	outs := normalizeOutputs(m.Modalities.Output)
	if len(outs) == 1 && outs[0] == "image" {
		return true
	}
	fam := strings.ToLower(strings.TrimSpace(m.Family))
	if fam == "" {
		return false
	}
	_, ok := imageFamilies[fam]
	return ok
}

func normalizeOutputs(outputs []string) []string {
	out := make([]string, 0, len(outputs))
	for _, o := range outputs {
		o = strings.ToLower(strings.TrimSpace(o))
		if o == "" {
			continue
		}
		out = append(out, o)
	}
	return out
}

func assistantSignal(m Model) bool {
	if m.ToolCall != nil && *m.ToolCall || m.Temperature || m.Reasoning != nil && *m.Reasoning {
		return true
	}
	return m.StructuredOutput != nil && *m.StructuredOutput
}
