package formatconfig

import (
	"strings"

	"github.com/lycaon/lycaon/internal/editorconfig"
	"github.com/lycaon/lycaon/pkg/api"
)

func files(f format, names ...string) []configFile {
	out := make([]configFile, len(names))
	for i, name := range names {
		out[i] = configFile{name: name, format: f}
	}
	return out
}

func sectioned(name string, f format, section ...string) configFile {
	return configFile{name: name, format: f, section: section}
}

func join(groups ...[]configFile) []configFile {
	var out []configFile
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}

// catalog is ordered as the wire reports sources.
var catalog = []tool{
	{
		id: api.FormatToolPrettier,
		files: join(
			[]configFile{sectioned("package.json", formatJSON, "prettier"), sectioned("package.yaml", formatYAML, "prettier")},
			files(formatYAML, ".prettierrc"),
			files(formatJSON, ".prettierrc.json"),
			files(formatYAML, ".prettierrc.yaml", ".prettierrc.yml"),
			files(formatScript, ".prettierrc.json5", ".prettierrc.js", ".prettierrc.ts", ".prettierrc.mjs", ".prettierrc.mts",
				".prettierrc.cjs", ".prettierrc.cts", "prettier.config.js", "prettier.config.ts", "prettier.config.mjs",
				"prettier.config.mts", "prettier.config.cjs", "prettier.config.cts"),
			files(formatTOML, ".prettierrc.toml"),
		),
		defaults: Settings{LineWidth: 80, IndentStyle: editorconfig.IndentSpace, IndentSize: 2, EndOfLine: "lf"},
		settings: func(d document) Settings {
			s := Settings{LineWidth: positive(d, "printWidth"), IndentSize: positive(d, "tabWidth"), EndOfLine: eol(d, "endOfLine")}
			s.IndentStyle = tabsIf(d, "useTabs")
			return s
		},
	},
	{
		id:       api.FormatToolBiome,
		files:    join(files(formatJSON, "biome.json"), files(formatJSONC, "biome.jsonc")),
		defaults: Settings{LineWidth: 80, IndentStyle: editorconfig.IndentTab, IndentSize: 2, EndOfLine: "lf"},
		settings: func(d document) Settings {
			return Settings{
				LineWidth: positive(d, "formatter", "lineWidth"), IndentSize: positive(d, "formatter", "indentWidth"),
				IndentStyle: style(d, "formatter", "indentStyle"), EndOfLine: eol(d, "formatter", "lineEnding"),
			}
		},
	},
	{
		id:    api.FormatToolDprint,
		files: join(files(formatJSON, "dprint.json", ".dprint.json"), files(formatJSONC, "dprint.jsonc", ".dprint.jsonc")),
		settings: func(d document) Settings {
			return Settings{
				LineWidth: positive(d, "lineWidth"), IndentSize: positive(d, "indentWidth"),
				IndentStyle: tabsIf(d, "useTabs"), EndOfLine: eol(d, "newLineKind"),
			}
		},
	},
	{
		id: api.FormatToolRuff,
		files: join(files(formatTOML, ".ruff.toml", "ruff.toml"),
			[]configFile{sectioned("pyproject.toml", formatTOML, "tool", "ruff")}),
		defaults: Settings{LineWidth: 88, IndentStyle: editorconfig.IndentSpace, IndentSize: 4},
		settings: func(d document) Settings {
			return Settings{
				LineWidth: positive(d, "line-length"), IndentSize: positive(d, "indent-width"),
				IndentStyle: style(d, "format", "indent-style"), EndOfLine: eol(d, "format", "line-ending"),
			}
		},
	},
	{
		id:       api.FormatToolBlack,
		files:    []configFile{sectioned("pyproject.toml", formatTOML, "tool", "black")},
		defaults: Settings{LineWidth: 88, IndentStyle: editorconfig.IndentSpace, IndentSize: 4},
		settings: func(d document) Settings { return Settings{LineWidth: positive(d, "line-length")} },
	},
	{
		id:       api.FormatToolRustfmt,
		files:    files(formatTOML, ".rustfmt.toml", "rustfmt.toml"),
		defaults: Settings{LineWidth: 100, IndentStyle: editorconfig.IndentSpace, IndentSize: 4},
		settings: func(d document) Settings {
			return Settings{
				LineWidth: positive(d, "max_width"), IndentSize: positive(d, "tab_spaces"),
				IndentStyle: tabsIf(d, "hard_tabs"), EndOfLine: eol(d, "newline_style"),
			}
		},
	},
	{
		id:    api.FormatToolClangFormat,
		files: files(formatYAMLDocuments, ".clang-format", "_clang-format"),
		settings: func(d document) Settings {
			s := Settings{LineWidth: clangColumns(d), IndentSize: positive(d, "IndentWidth"), TabWidth: positive(d, "TabWidth")}
			if use, ok := d.str("UseTab"); ok {
				if strings.EqualFold(use, "Never") {
					s.IndentStyle = editorconfig.IndentSpace
				} else {
					s.IndentStyle = editorconfig.IndentTab
				}
			}
			s.EndOfLine = eol(d, "LineEnding")
			return s
		},
	},
	{
		id:       api.FormatToolScalafmt,
		files:    files(formatKeyValue, ".scalafmt.conf"),
		defaults: Settings{LineWidth: 80},
		settings: func(d document) Settings { return Settings{LineWidth: positive(d, "maxColumn")} },
	},
	{
		id:       api.FormatToolSwiftFormat,
		files:    files(formatJSON, ".swift-format"),
		defaults: Settings{LineWidth: 100, IndentStyle: editorconfig.IndentSpace, IndentSize: 2},
		settings: func(d document) Settings {
			s := Settings{LineWidth: positive(d, "lineLength"), TabWidth: positive(d, "tabWidth")}
			if n := positive(d, "indentation", "spaces"); n > 0 {
				s.IndentStyle, s.IndentSize = editorconfig.IndentSpace, n
			} else if positive(d, "indentation", "tabs") > 0 {
				s.IndentStyle, s.IndentSizeTab = editorconfig.IndentTab, true
			}
			return s
		},
	},
	{
		id:       api.FormatToolDart,
		files:    []configFile{sectioned("analysis_options.yaml", formatYAML, "formatter")},
		defaults: Settings{LineWidth: 80, IndentStyle: editorconfig.IndentSpace, IndentSize: 2},
		settings: func(d document) Settings { return Settings{LineWidth: positive(d, "page_width")} },
	},
	{
		id:       api.FormatToolRubocop,
		files:    files(formatYAML, ".rubocop.yml"),
		defaults: Settings{LineWidth: 120, IndentStyle: editorconfig.IndentSpace, IndentSize: 2},
		settings: func(d document) Settings {
			s := Settings{LineWidth: positive(d, "Layout/LineLength", "Max"), IndentSize: positive(d, "Layout/IndentationWidth", "Width")}
			if enforced, ok := d.str("Layout/IndentationStyle", "EnforcedStyle"); ok {
				s.IndentStyle = styleWord(enforced)
			}
			return s
		},
	},
	{
		id:       api.FormatToolStylua,
		files:    files(formatTOML, "stylua.toml", ".stylua.toml"),
		defaults: Settings{LineWidth: 120, IndentStyle: editorconfig.IndentTab, IndentSize: 4, EndOfLine: "lf"},
		settings: func(d document) Settings {
			return Settings{
				LineWidth: positive(d, "column_width"), IndentSize: positive(d, "indent_width"),
				IndentStyle: style(d, "indent_type"), EndOfLine: eol(d, "line_endings"),
			}
		},
	},
	{
		id:       api.FormatToolOcamlformat,
		files:    files(formatKeyValue, ".ocamlformat"),
		defaults: Settings{LineWidth: 80},
		settings: func(d document) Settings { return Settings{LineWidth: positive(d, "margin")} },
	},
	{
		id:       api.FormatToolFourmolu,
		files:    files(formatYAML, "fourmolu.yaml"),
		defaults: Settings{IndentStyle: editorconfig.IndentSpace, IndentSize: 4},
		settings: func(d document) Settings {
			return Settings{LineWidth: positive(d, "column-limit"), IndentSize: positive(d, "indentation")}
		},
	},
	{
		id:       api.FormatToolJuliaFormatter,
		files:    files(formatTOML, ".JuliaFormatter.toml"),
		defaults: Settings{LineWidth: 92, IndentStyle: editorconfig.IndentSpace, IndentSize: 4},
		settings: func(d document) Settings {
			return Settings{LineWidth: positive(d, "margin"), IndentSize: positive(d, "indent")}
		},
	},
	{
		id:       api.FormatToolSqlfluff,
		files:    files(formatINI, ".sqlfluff"),
		defaults: Settings{LineWidth: 80, IndentStyle: editorconfig.IndentSpace, IndentSize: 4},
		settings: func(d document) Settings {
			s := Settings{LineWidth: positive(d, "sqlfluff", "max_line_length"), IndentSize: positive(d, "sqlfluff:indentation", "tab_space_size")}
			if unit, ok := d.str("sqlfluff:indentation", "indent_unit"); ok {
				s.IndentStyle = styleWord(unit)
			}
			return s
		},
	},
	{
		id:       api.FormatToolFoundry,
		files:    []configFile{sectioned("foundry.toml", formatTOML, "fmt")},
		defaults: Settings{LineWidth: 120, IndentStyle: editorconfig.IndentSpace, IndentSize: 4},
		settings: func(d document) Settings {
			return Settings{LineWidth: positive(d, "line_length"), IndentSize: positive(d, "tab_width")}
		},
	},
	{
		id:       api.FormatToolTaplo,
		files:    files(formatTOML, ".taplo.toml", "taplo.toml"),
		defaults: Settings{LineWidth: 80, IndentStyle: editorconfig.IndentSpace, IndentSize: 2},
		settings: func(d document) Settings {
			s := Settings{LineWidth: positive(d, "formatting", "column_width")}
			if indent, ok := d.str("formatting", "indent_string"); ok && indent != "" {
				if strings.Trim(indent, "\t") == "" {
					s.IndentStyle, s.IndentSizeTab = editorconfig.IndentTab, true
				} else if strings.Trim(indent, " ") == "" {
					s.IndentStyle, s.IndentSize = editorconfig.IndentSpace, len(indent)
				}
			}
			if crlf, ok := d.bool("formatting", "crlf"); ok {
				s.EndOfLine = "lf"
				if crlf {
					s.EndOfLine = "crlf"
				}
			}
			return s
		},
	},
}

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
