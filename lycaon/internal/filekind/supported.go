package filekind

import (
	"strings"
)

// supportedLanguages contains the grammar identifiers used by editor navigation.
var supportedLanguages = []string{
	"apex", "bash", "c", "c_sharp", "cairo", "circom", "clojure", "commonlisp",
	"cpp", "dart", "dockerfile", "elixir", "go", "groovy", "hack", "hcl", "html", "java",
	"javascript", "json", "jsonnet", "julia", "kotlin", "lua", "move", "ocaml",
	"perl", "php", "powershell", "promql", "proto", "python", "ql", "r", "ruby", "rust", "scala",
	"scheme", "solidity", "swift", "typescript", "vue", "xml", "yaml",
}

// SupportedLanguages returns a sorted copy of the supported grammar names.
func SupportedLanguages() []string {
	out := make([]string, len(supportedLanguages))
	copy(out, supportedLanguages)
	return out
}

// LanguageForPath resolves a path to a supported grammar name, or "" when the
// file maps to no supported language.
func LanguageForPath(path string) string {
	entry := GrammarForPath(path)
	if entry == nil {
		return ""
	}
	name := strings.ToLower(entry.Name)
	for _, supported := range supportedLanguages {
		if supported == name {
			return name
		}
	}
	return ""
}
