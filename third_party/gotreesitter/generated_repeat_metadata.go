package gotreesitter

import "strings"

// InferGeneratedRepeatAuxMetadata fills GeneratedRepeatAux for older language
// blobs that predate the explicit metadata bit.
func InferGeneratedRepeatAuxMetadata(lang *Language) {
	if lang == nil {
		return
	}
	for i := range lang.SymbolMetadata {
		meta := &lang.SymbolMetadata[i]
		if meta.GeneratedRepeatAux || meta.Visible || meta.Named || meta.Supertype {
			continue
		}
		if lang.TokenCount > 0 && uint32(i) < lang.TokenCount {
			continue
		}
		name := meta.Name
		if name == "" && i < len(lang.SymbolNames) {
			name = lang.SymbolNames[i]
		}
		if isGeneratedRepeatAuxSymbolName(name) {
			meta.GeneratedRepeatAux = true
		}
	}
}

func isGeneratedRepeatAuxSymbolName(name string) bool {
	idx := strings.LastIndex(name, "_repeat")
	if idx <= 0 {
		return false
	}
	digits := name[idx+len("_repeat"):]
	if digits == "" {
		return false
	}
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return false
		}
	}
	return true
}
