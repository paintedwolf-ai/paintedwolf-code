package commandsurface

import (
	"errors"
	"strconv"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// Brace expansion applies to command words, for-loop items and array elements,
// before any other expansion. Assignment values, here-string words, parameter
// operator words, test operands, case words and arithmetic keep braces literal.

// markBraceFree records a word that keeps its braces literal.
func (c *programCompiler) markBraceFree(word *syntax.Word) {
	if word == nil || !c.reserveHeap(256, word) {
		return
	}
	if c.braceFree == nil {
		c.braceFree = make(map[*syntax.Word]bool)
	}
	c.braceFree[word] = true
}

// markFieldWords records words whose unquoted braces expand into fields.
func (c *programCompiler) markFieldWords(words []*syntax.Word) {
	if len(words) == 0 || !c.reserveHeap(int64(len(words))*256, words[0]) {
		return
	}
	if c.fieldWords == nil {
		c.fieldWords = make(map[*syntax.Word]bool)
	}
	for _, word := range words {
		c.fieldWords[word] = true
	}
}

const braceRangeForms = "{a,b}, {x..y} or {x..y..step} with both endpoints integers or both single letters"

// splitBraces splits one field word's brace expansions once and admits their
// sequences and total field count before any field is produced. It returns nil
// when the word has no brace expansion.
func (c *programCompiler) splitBraces(w *syntax.Word) *syntax.Word {
	if split, seen := c.braces[w]; seen || c.err != nil {
		return split
	}
	split, err := syntax.SplitBracesBounded(w, c.pool)
	if errors.Is(err, syntax.ErrBraceMixedRange) {
		c.unsupported(w, braceRangeForms)
		return nil
	}
	if err != nil {
		c.err = parserDiagnostic(err, len(c.source))
		return nil
	}
	if !c.reserveHeap(256, w) {
		return nil
	}
	if c.braces == nil {
		c.braces = make(map[*syntax.Word]*syntax.Word)
	}
	c.braces[w] = split
	if split != nil {
		c.limit(w, "expanded_fields", c.braceFields(w, split.Parts))
	}
	return split
}

// braceFields counts the words a split word expands to, saturating one past
// the expanded_fields bound, and counts the split nodes lowering adds.
func (c *programCompiler) braceFields(w *syntax.Word, parts []syntax.WordPart) int64 {
	ceiling := languageLimit(c.catalog, "expanded_fields") + 1
	total := int64(1)
	for _, part := range parts {
		c.braceNodes++
		brace, ok := part.(*syntax.BraceExp)
		if !ok || c.err != nil {
			continue
		}
		var count int64
		if brace.Sequence {
			if !c.reserveHeap(braceLiteralBytes(brace), w) {
				return ceiling
			}
			sequence := decodeBraceSequence(brace)
			if sequence.Step == 0 {
				c.unsupported(w, "a brace sequence step other than 0")
				return ceiling
			}
			count = int64(min(sequence.Count(), uint64(ceiling)))
		} else {
			for _, elem := range brace.Elems {
				c.braceNodes++
				count = min(count+c.braceFields(w, elem.Parts), ceiling)
			}
		}
		if total > ceiling/count {
			total = ceiling
		} else {
			total = min(total*count, ceiling)
		}
	}
	return total
}

// braceLiteralBytes bounds joining a sequence's literal elements.
func braceLiteralBytes(brace *syntax.BraceExp) int64 {
	bytes := int64(256)
	for _, elem := range brace.Elems {
		for _, part := range elem.Parts {
			if lit, ok := part.(*syntax.Lit); ok {
				bytes += 128 + 4*int64(len(lit.Value))
			}
		}
	}
	return bytes
}

// decodeBraceSequence reads a {x..y[..step]} the selected splitter admitted:
// both endpoints integers or both single letters, and an integer step.
func decodeBraceSequence(brace *syntax.BraceExp) BraceSequence {
	from, to := brace.Elems[0].Lit(), brace.Elems[1].Lit()
	sequence := BraceSequence{Step: 1}
	start, startErr := strconv.ParseInt(from, 10, 64)
	end, endErr := strconv.ParseInt(to, 10, 64)
	if startErr != nil || endErr != nil {
		sequence.Letters = true
		start, end = int64(from[0]), int64(to[0])
	} else if leadingZero(from) || leadingZero(to) {
		sequence.Width = max(len(from), len(to))
	}
	sequence.Start, sequence.End = start, end
	if len(brace.Elems) > 2 {
		step, _ := strconv.ParseInt(brace.Elems[2].Lit(), 10, 64)
		sequence.Step = uint64(step)
		if step < 0 {
			sequence.Step = -uint64(step)
		}
	}
	return sequence
}

func leadingZero(value string) bool {
	value = strings.TrimPrefix(value, "-")
	return len(value) > 1 && value[0] == '0'
}

// nonEmptyParts drops the empty literals the splitter leaves around a brace.
func nonEmptyParts(parts []syntax.WordPart) []syntax.WordPart {
	out := make([]syntax.WordPart, 0, len(parts))
	for _, part := range parts {
		if lit, ok := part.(*syntax.Lit); !ok || lit.Value != "" {
			out = append(out, part)
		}
	}
	return out
}

// brace lowers one split brace expansion.
func (c *programCompiler) brace(b *syntax.BraceExp, loc SyntaxLocation) BracePart {
	out := BracePart{SyntaxLocation: loc}
	if b.Sequence {
		sequence := decodeBraceSequence(b)
		out.Sequence = &sequence
		return out
	}
	out.Alternatives = make([]DeferredWord, 0, len(b.Elems))
	for _, elem := range b.Elems {
		out.Alternatives = append(out.Alternatives, DeferredWord{SyntaxLocation: c.location(elem), Parts: c.parts(nonEmptyParts(elem.Parts))})
	}
	return out
}
