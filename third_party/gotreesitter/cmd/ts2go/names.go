package main

import (
	"strings"
	"unicode"
)

func languageFuncName(name string) string {
	id := toExportedIdentifier(name)
	if id == "" {
		id = "Lang"
	}
	if unicode.IsDigit([]rune(id)[0]) {
		id = "Lang" + id
	}
	return id + "Language"
}

func toExportedIdentifier(s string) string {
	var parts []string
	var cur []rune
	flush := func() {
		if len(cur) == 0 {
			return
		}
		r := cur
		r[0] = unicode.ToUpper(r[0])
		parts = append(parts, string(r))
		cur = nil
	}

	for _, ch := range s {
		if unicode.IsLetter(ch) || unicode.IsDigit(ch) {
			cur = append(cur, ch)
			continue
		}
		flush()
	}
	flush()
	return strings.Join(parts, "")
}
