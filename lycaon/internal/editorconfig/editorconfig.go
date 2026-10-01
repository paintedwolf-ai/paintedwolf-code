// Package editorconfig resolves the save rules a project declares in
// .editorconfig files and checks text against them.
package editorconfig

import (
	"strconv"
	"strings"
)

// IndentStyle is the declared indentation character.
type IndentStyle string

const (
	IndentSpace IndentStyle = "space"
	IndentTab   IndentStyle = "tab"
)

// EndOfLine is a declared line terminator the host can write.
type EndOfLine string

const (
	EndOfLineLF   EndOfLine = "lf"
	EndOfLineCRLF EndOfLine = "crlf"
)

// Properties are the pairs in effect for one file. Zero values are undeclared.
type Properties struct {
	IndentStyle IndentStyle
	// IndentSize is columns per level; zero when undeclared or `tab`.
	IndentSize int
	// IndentSizeTab is true for `indent_size = tab`.
	IndentSizeTab bool
	TabWidth      int
	EndOfLine     EndOfLine
	// TrimTrailingWhitespace and InsertFinalNewline distinguish false from undeclared.
	TrimTrailingWhitespace *bool
	InsertFinalNewline     *bool
	// Charset is a recognized token such as utf-8 or utf-8-bom.
	Charset string
}

// Declared reports whether any pair is in effect.
func (p Properties) Declared() bool {
	return p != Properties{}
}

// IndentWidth is the column width of one declared indentation level, or zero
// when neither style nor size is declared.
func (p Properties) IndentWidth() int {
	switch {
	case p.IndentSize > 0:
		return p.IndentSize
	case p.TabWidth > 0 && (p.IndentSizeTab || p.IndentStyle != ""):
		return p.TabWidth
	case p.IndentSizeTab || p.IndentStyle != "":
		return defaultIndentWidth
	default:
		return 0
	}
}

// Width used when a style is declared without a size.
const defaultIndentWidth = 4

// Section is one bracketed glob and its raw pairs; a nil Pattern is the preamble.
type Section struct {
	Pattern *string
	Pairs   map[string]string
}

// File is one parsed .editorconfig.
type File struct {
	// Dir is the root-relative, slash-separated directory holding the file ("" = root).
	Dir      string
	Root     bool
	Sections []Section
}

// Parse reads one .editorconfig body. Keys are case-insensitive; unrecognized
// lines are ignored.
func Parse(dir, content string) File {
	f := File{Dir: dir, Sections: []Section{{Pairs: map[string]string{}}}}
	current := &f.Sections[0]
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if len(line) > 2 && line[0] == '[' && line[len(line)-1] == ']' {
			pattern := line[1 : len(line)-1]
			f.Sections = append(f.Sections, Section{Pattern: &pattern, Pairs: map[string]string{}})
			current = &f.Sections[len(f.Sections)-1]
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		if key == "" {
			continue
		}
		if current.Pattern == nil && key == "root" && strings.EqualFold(value, "true") {
			f.Root = true
		}
		current.Pairs[key] = value
	}
	return f
}

// Resolve applies configs ordered farthest from the file first, so nearer
// files and later sections win.
func Resolve(path string, configs []File) Properties {
	file := strings.TrimPrefix(strings.ReplaceAll(path, "\\", "/"), "./")
	var props Properties
	for _, config := range configs {
		dir := strings.TrimSuffix(config.Dir, "/")
		rel := file
		if dir != "" {
			rel = strings.TrimPrefix(file, dir+"/")
		}
		for _, section := range config.Sections {
			if section.Pattern == nil || !MatchPattern(*section.Pattern, rel) {
				continue
			}
			props = props.merge(section.Pairs)
		}
	}
	return props
}

func isUnset(raw string) bool { return strings.EqualFold(raw, "unset") }

func (p Properties) merge(raw map[string]string) Properties {
	next := p
	if v, ok := raw["indent_style"]; ok {
		switch strings.ToLower(v) {
		case "unset":
			next.IndentStyle = ""
		case "space":
			next.IndentStyle = IndentSpace
		case "tab":
			next.IndentStyle = IndentTab
		}
	}
	if v, ok := raw["indent_size"]; ok {
		switch {
		case isUnset(v):
			next.IndentSize, next.IndentSizeTab = 0, false
		case strings.EqualFold(v, "tab"):
			next.IndentSize, next.IndentSizeTab = 0, true
		default:
			if n, ok := boundedInt(v, 64); ok {
				next.IndentSize, next.IndentSizeTab = n, false
			}
		}
	}
	if v, ok := raw["tab_width"]; ok {
		if isUnset(v) {
			next.TabWidth = 0
		} else if n, ok := boundedInt(v, 10_000); ok {
			next.TabWidth = n
		}
	}
	if v, ok := raw["end_of_line"]; ok {
		switch strings.ToLower(v) {
		case "unset":
			next.EndOfLine = ""
		case "lf":
			next.EndOfLine = EndOfLineLF
		case "crlf":
			next.EndOfLine = EndOfLineCRLF
		}
		// `cr` has no host line terminator and is ignored.
	}
	if v, ok := raw["trim_trailing_whitespace"]; ok {
		next.TrimTrailingWhitespace = mergeBool(next.TrimTrailingWhitespace, v)
	}
	if v, ok := raw["insert_final_newline"]; ok {
		next.InsertFinalNewline = mergeBool(next.InsertFinalNewline, v)
	}
	if v, ok := raw["charset"]; ok {
		switch cs := strings.ToLower(v); cs {
		case "unset":
			next.Charset = ""
		case "latin1", "utf-8", "utf-8-bom", "utf-16be", "utf-16le":
			next.Charset = cs
		}
	}
	return next
}

func mergeBool(current *bool, raw string) *bool {
	switch strings.ToLower(raw) {
	case "unset":
		return nil
	case "true":
		v := true
		return &v
	case "false":
		v := false
		return &v
	default:
		return current
	}
}

func boundedInt(raw string, max int) (int, bool) {
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 || n > max {
		return 0, false
	}
	return n, true
}
