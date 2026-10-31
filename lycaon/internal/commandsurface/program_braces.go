package commandsurface

import (
	"errors"
	"math"
	"strconv"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// braceContexts records which parsed words expand braces. Brace expansion
// applies to command words, for-loop items and array elements, before any
// other expansion; assignment values, here-string words, parameter operator
// words, test operands, case words and arithmetic keep braces literal. Each
// field word is split and admitted once.
type braceContexts struct {
	literal, fields map[*syntax.Word]bool
	split           map[*syntax.Word]*syntax.Word
	// nodes counts the split nodes lowering adds to the host plan.
	nodes int64
}

func (b *braceContexts) mark(word *syntax.Word, fields bool) {
	target := &b.literal
	if fields {
		target = &b.fields
	}
	if *target == nil {
		*target = make(map[*syntax.Word]bool)
	}
	(*target)[word] = true
}

func (b *braceContexts) record(word, split *syntax.Word) {
	if b.split == nil {
		b.split = make(map[*syntax.Word]*syntax.Word)
	}
	b.split[word] = split
}

// markBraces records words that expand braces into fields, or that keep them
// literal.
func (c *programCompiler) markBraces(fields bool, words ...*syntax.Word) {
	for _, word := range words {
		if word == nil || !c.reserveHeap(256, word) {
			continue
		}
		c.braces.mark(word, fields)
	}
}

const braceRangeForms = "{a,b}, {x..y} or {x..y..step} with both endpoints integers or both single letters"

// splitBraces splits one field word's brace expansions once and admits their
// sequences and total field count before any field is produced. It returns nil
// when the word has no brace expansion.
func (c *programCompiler) splitBraces(w *syntax.Word) *syntax.Word {
	if split, seen := c.braces.split[w]; seen || c.err != nil {
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
	c.braces.record(w, split)
	if split == nil || !c.reserveHeap(braceLiteralBytes(split.Parts), w) {
		return split
	}
	count, nodes, zeroStep := braceFields(split.Parts, languageLimit(c.catalog, "expanded_fields")+1)
	c.braces.nodes += nodes
	if zeroStep {
		c.unsupported(w, "a brace sequence step other than 0")
	}
	c.limit(w, "expanded_fields", count)
	return split
}

// braceFields counts the words split parts expand to, saturating at ceiling,
// and the nodes lowering adds. zeroStep reports a {x..y..0} sequence.
func braceFields(parts []syntax.WordPart, ceiling int64) (count, nodes int64, zeroStep bool) {
	count = 1
	for _, part := range parts {
		nodes++
		brace, ok := part.(*syntax.BraceExp)
		if !ok {
			continue
		}
		var alternatives int64
		if brace.Sequence {
			sequence := decodeBraceSequence(brace)
			zeroStep = zeroStep || sequence.Step == 0
			seqCount := sequence.Count()
			if seqCount >= uint64(math.MaxInt64) {
				alternatives = ceiling
			} else {
				alternatives = min(int64(seqCount), ceiling)
			}
		} else {
			for _, elem := range brace.Elems {
				words, elemNodes, elemZero := braceFields(elem.Parts, ceiling)
				alternatives = min(alternatives+words, ceiling)
				nodes += elemNodes + 1
				zeroStep = zeroStep || elemZero
			}
		}
		if count > ceiling/alternatives {
			count = ceiling
		} else {
			count = min(count*alternatives, ceiling)
		}
	}
	return count, nodes, zeroStep
}

// braceLiteralBytes bounds joining every sequence's literal elements.
func braceLiteralBytes(parts []syntax.WordPart) int64 {
	bytes := int64(256)
	for _, part := range parts {
		brace, ok := part.(*syntax.BraceExp)
		if !ok {
			continue
		}
		for _, elem := range brace.Elems {
			if !brace.Sequence {
				bytes += braceLiteralBytes(elem.Parts)
				continue
			}
			for _, elemPart := range elem.Parts {
				if lit, ok := elemPart.(*syntax.Lit); ok {
					bytes += 128 + 4*int64(len(lit.Value))
				}
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
		rawStep := strings.TrimPrefix(strings.TrimPrefix(brace.Elems[2].Lit(), "-"), "+")
		step, _ := strconv.ParseUint(rawStep, 10, 64)
		sequence.Step = step
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
