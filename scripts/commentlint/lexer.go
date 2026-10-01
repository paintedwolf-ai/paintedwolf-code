package main

import "strings"

type lineToken struct {
	value     string
	boundary  bool
	columnOne bool
}

type blockToken struct {
	start  string
	end    string
	nested bool
}

type quoteToken struct {
	start     string
	end       string
	escaped   bool
	multiline bool
}

type lexicalSyntax struct {
	supported bool
	lines     []lineToken
	blocks    []blockToken
	quotes    []quoteToken
}

func lexicalComments(language string, src []byte) ([]comment, bool) {
	syntax := lexicalSyntaxFor(language)
	if !syntax.supported {
		return nil, false
	}
	if len(syntax.lines) == 0 && len(syntax.blocks) == 0 {
		return nil, true
	}
	text := string(src)
	var comments []comment
	for offset, row, column := 0, 1, 1; offset < len(text); {
		if quote, ok := matchingQuote(text, offset, syntax.quotes); ok {
			next := quotedEnd(text, offset, quote)
			advancePosition(text[offset:next], &row, &column)
			offset = next
			continue
		}
		if block, ok := matchingBlock(text, offset, syntax.blocks); ok {
			end := blockEnd(text, offset, block)
			comments = append(comments, comment{
				Language: language, Kind: "block_comment", StartRow: row, StartCol: column, Text: text[offset:end],
			})
			advancePosition(text[offset:end], &row, &column)
			offset = end
			continue
		}
		if matchingLine(text, offset, column, syntax.lines) {
			end := strings.IndexByte(text[offset:], '\n')
			if end < 0 {
				end = len(text)
			} else {
				end += offset
			}
			comments = append(comments, comment{
				Language: language, Kind: "line_comment", StartRow: row, StartCol: column, Text: text[offset:end],
			})
			advancePosition(text[offset:end], &row, &column)
			offset = end
			continue
		}
		advancePosition(text[offset:offset+1], &row, &column)
		offset++
	}
	return comments, true
}

func blockEnd(text string, offset int, token blockToken) int {
	position := offset + len(token.start)
	depth := 1
	for position < len(text) {
		if token.nested && strings.HasPrefix(text[position:], token.start) {
			depth++
			position += len(token.start)
			continue
		}
		if strings.HasPrefix(text[position:], token.end) {
			depth--
			position += len(token.end)
			if depth == 0 {
				return position
			}
			continue
		}
		position++
	}
	return len(text)
}

func matchingQuote(text string, offset int, tokens []quoteToken) (quoteToken, bool) {
	for _, token := range tokens {
		if strings.HasPrefix(text[offset:], token.start) {
			return token, true
		}
	}
	return quoteToken{}, false
}

func matchingBlock(text string, offset int, tokens []blockToken) (blockToken, bool) {
	for _, token := range tokens {
		if strings.HasPrefix(text[offset:], token.start) {
			return token, true
		}
	}
	return blockToken{}, false
}

func matchingLine(text string, offset, column int, tokens []lineToken) bool {
	for _, token := range tokens {
		if !strings.HasPrefix(text[offset:], token.value) {
			continue
		}
		if token.columnOne && column != 1 {
			continue
		}
		if !token.boundary || column == 1 || offset > 0 && (text[offset-1] == ' ' || text[offset-1] == '\t') {
			return true
		}
	}
	return false
}

func quotedEnd(text string, offset int, token quoteToken) int {
	position := offset + len(token.start)
	for position < len(text) {
		if token.escaped && text[position] == '\\' {
			position += 2
			continue
		}
		if strings.HasPrefix(text[position:], token.end) {
			return position + len(token.end)
		}
		if !token.multiline && text[position] == '\n' {
			return position
		}
		position++
	}
	return len(text)
}

func advancePosition(text string, row, column *int) {
	for index := range len(text) {
		if text[index] == '\n' {
			*row++
			*column = 1
		} else {
			*column++
		}
	}
}

func lexicalSyntaxFor(language string) lexicalSyntax {
	single := []quoteToken{
		{start: `"`, end: `"`, escaped: true},
		{start: `'`, end: `'`, escaped: true},
	}
	slash := lexicalSyntax{
		supported: true,
		lines:     []lineToken{{value: "//"}},
		blocks:    []blockToken{{start: "/*", end: "*/"}},
		quotes:    single,
	}
	hash := lexicalSyntax{supported: true, lines: []lineToken{{value: "#"}}, quotes: single}
	switch language {
	case "diff":
		return lexicalSyntax{supported: true, lines: []lineToken{{value: "#", columnOne: true}}}
	case "bash", "elixir", "julia", "perl", "r", "ruby", "toml":
		return hash
	case "dockerfile", "yaml":
		return lexicalSyntax{supported: true, lines: []lineToken{{value: "#", boundary: true}}, quotes: single}
	case "python":
		return lexicalSyntax{
			supported: true,
			lines:     []lineToken{{value: "#"}},
			quotes: []quoteToken{
				{start: `"""`, end: `"""`, escaped: true, multiline: true},
				{start: `'''`, end: `'''`, escaped: true, multiline: true},
				{start: `"`, end: `"`, escaped: true},
				{start: `'`, end: `'`, escaped: true},
			},
		}
	case "css", "dart", "groovy", "java", "javascript", "solidity", "swift", "tsx", "typescript":
		return slash
	case "go", "gomod":
		slash.quotes = append([]quoteToken{{start: "`", end: "`", multiline: true}}, slash.quotes...)
		return slash
	case "rust":
		slash.blocks[0].nested = true
		return slash
	case "hcl":
		return lexicalSyntax{
			supported: true,
			lines:     []lineToken{{value: "//"}, {value: "#", boundary: true}},
			blocks:    slash.blocks,
			quotes:    single,
		}
	case "html", "markdown", "xml":
		return lexicalSyntax{supported: true, blocks: []blockToken{{start: "<!--", end: "-->"}}}
	case "lua":
		return lexicalSyntax{
			supported: true,
			lines:     []lineToken{{value: "--"}},
			blocks:    []blockToken{{start: "--[[", end: "]]"}},
			quotes:    single,
		}
	case "powershell":
		return lexicalSyntax{
			supported: true,
			lines:     []lineToken{{value: "#"}},
			blocks:    []blockToken{{start: "<#", end: "#>"}},
			quotes:    single,
		}
	case "sql":
		return lexicalSyntax{
			supported: true,
			lines:     []lineToken{{value: "--"}},
			blocks:    slash.blocks,
			quotes:    single,
		}
	case "vimdoc":
		return lexicalSyntax{supported: true, lines: []lineToken{{value: `"`}}}
	case "json":
		return lexicalSyntax{supported: true}
	default:
		return lexicalSyntax{}
	}
}
