package repomap

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/tsparse"
	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// FileTagOutcome contains definitions or analysis failures for one source file.
type FileTagOutcome struct {
	Tags        []Tag
	Definitions []DefinitionSpan
	Language    string
	Failure     *tsparse.Failure
	Skip        SkipStats
}

func definitionTags(rel, langName string, raw []gotreesitter.Tag, src []byte) []Tag {
	out := make([]Tag, 0, len(raw))
	lines := strings.Split(string(src), "\n")
	for _, t := range raw {
		if !strings.HasPrefix(t.Kind, "definition.") {
			continue
		}
		line := int(t.NameRange.StartPoint.Row) + 1
		out = append(out, Tag{
			Kind:      strings.TrimPrefix(t.Kind, "definition."),
			Name:      t.Name,
			File:      rel,
			Line:      line,
			Language:  langName,
			Signature: signatureLine(lines, line),
			Doc:       leadingComment(lines, line),
		})
	}
	sortTags(out)
	return out
}

// Bounds on the text carried with a tag.
const (
	signatureRunes = 160
	docRunes       = 240
)

func boundRunes(text string, limit int) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) > limit {
		runes = runes[:limit]
	}
	return string(runes)
}

func signatureLine(lines []string, line int) string {
	if line < 1 || line > len(lines) {
		return ""
	}
	return boundRunes(lines[line-1], signatureRunes)
}

var commentMarkers = []string{"///", "//!", "//", "/**", "/*", "*/", "*", "#", "--", "\"\"\""}

// leadingComment joins the comment lines directly above a definition; a
// blank line ends it, and attributes between comment and definition are skipped.
func leadingComment(lines []string, line int) string {
	var parts []string
	for i := line - 2; i >= 0 && len(parts) < 6; i-- {
		text := strings.TrimSpace(lines[i])
		if text == "" {
			break
		}
		if strings.HasPrefix(text, "#[") || strings.HasPrefix(text, "@") {
			continue
		}
		marker := ""
		for _, m := range commentMarkers {
			if strings.HasPrefix(text, m) {
				marker = m
				break
			}
		}
		if marker == "" {
			break
		}
		parts = append([]string{strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(text, marker), "*/"))}, parts...)
	}
	return boundRunes(strings.Join(parts, " "), docRunes)
}

func definitionSpans(raw []gotreesitter.Tag) []DefinitionSpan {
	out := make([]DefinitionSpan, 0, len(raw))
	for _, tag := range raw {
		if !strings.HasPrefix(tag.Kind, "definition.") {
			continue
		}
		out = append(out, DefinitionSpan{
			Kind:      strings.TrimPrefix(tag.Kind, "definition."),
			Name:      tag.Name,
			StartByte: int(tag.Range.StartByte),
			EndByte:   int(tag.Range.EndByte),
			StartRow:  int(tag.Range.StartPoint.Row),
			StartCol:  int(tag.Range.StartPoint.Column),
			EndRow:    int(tag.Range.EndPoint.Row),
			EndCol:    int(tag.Range.EndPoint.Column),
		})
	}
	return out
}

// tagWithRecover returns only complete bounded parses.
func tagWithRecover(tg *gotreesitter.Tagger, parse func() (*gotreesitter.Tree, error)) (tags []gotreesitter.Tag, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			tags, err = nil, &tsparse.Failure{Reason: "panic", Cause: fmt.Errorf("tag extraction panic: %v", recovered)}
		}
	}()
	tree, err := parse()
	if err != nil {
		return nil, err
	}
	defer tree.Release()
	return tg.TagTree(tree), nil
}

func tagSourceWithGrammar(ctx context.Context, rel string, src []byte, entry grammars.LangEntry, cache *taggerCache) FileTagOutcome {
	parsed := cache.tag(ctx, entry, src)
	if parsed.noTagger {
		return FileTagOutcome{
			Language: entry.Name,
			Skip:     SkipStats{NoTagger: 1},
		}
	}
	if parsed.failure != nil {
		skip := SkipStats{ParseFailed: 1}
		if parsed.failure.Incomplete() {
			skip = SkipStats{ParseIncomplete: 1}
		}
		return FileTagOutcome{Language: entry.Name, Skip: skip, Failure: parsed.failure}
	}
	tags := definitionTags(rel, entry.Name, parsed.tags, src)
	if len(tags) == 0 {
		return FileTagOutcome{
			Language: entry.Name,
			Skip:     SkipStats{NoTags: 1},
		}
	}
	return FileTagOutcome{
		Tags:        tags,
		Definitions: definitionSpans(parsed.tags),
		Language:    entry.Name,
	}
}
