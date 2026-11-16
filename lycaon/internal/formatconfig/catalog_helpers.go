package formatconfig

import (
	"strings"

	"github.com/lycaon/lycaon/internal/editorconfig"
)

// positive reads a whole number in [1, 10000]; anything else is undeclared.
func positive(d document, path ...string) int {
	n, ok := d.int(path...)
	if !ok || n < 1 || n > 10_000 {
		return 0
	}
	return n
}

func tabsIf(d document, path ...string) editorconfig.IndentStyle {
	tabs, ok := d.bool(path...)
	if !ok {
		return ""
	}
	if tabs {
		return editorconfig.IndentTab
	}
	return editorconfig.IndentSpace
}

func style(d document, path ...string) editorconfig.IndentStyle {
	word, ok := d.str(path...)
	if !ok {
		return ""
	}
	return styleWord(word)
}

// styleWord reads the tab or space words tools spell differently.
func styleWord(word string) editorconfig.IndentStyle {
	switch strings.ToLower(strings.TrimSpace(word)) {
	case "tab", "tabs":
		return editorconfig.IndentTab
	case "space", "spaces":
		return editorconfig.IndentSpace
	}
	return ""
}

// eol maps each tool's line ending words; automatic choices are undeclared.
func eol(d document, path ...string) string {
	word, ok := d.str(path...)
	if !ok {
		return ""
	}
	switch strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(word, "-", ""), "_", "")) {
	case "lf", "unix":
		return "lf"
	case "crlf", "windows":
		return "crlf"
	case "cr":
		return "cr"
	}
	return ""
}

// clangStyleColumns are the column limits of clang-format's predefined styles.
var clangStyleColumns = map[string]int{
	"llvm": 80, "google": 80, "chromium": 80, "mozilla": 80, "microsoft": 120, "gnu": 79,
}

// clangColumns reads ColumnLimit, else the limit of BasedOnStyle; 0 means no limit.
func clangColumns(d document) int {
	if n, ok := d.int("ColumnLimit"); ok {
		if n < 1 || n > 10_000 {
			return 0
		}
		return n
	}
	if based, ok := d.str("BasedOnStyle"); ok {
		return clangStyleColumns[strings.ToLower(based)]
	}
	return clangStyleColumns["llvm"]
}
