package syntax

import (
	"fmt"
	"math"
	"regexp/syntax"
	"slices"
	"unicode"

	"github.com/dlclark/regexp2/v2/internal/budget"
)

// FromValidatedSyntax lowers a host-validated regular tree without accepting a
// second textual grammar. Captures are unobservable for this boolean-only API.
func FromValidatedSyntax(source *syntax.Regexp, m *budget.Meter, inputBound int) *RegexTree {
	m.Alloc(512)
	l := lowering{m: m, inputBound: inputBound}
	root := &RegexNode{T: NtCapture, M: 0, N: -1, Children: []*RegexNode{l.lower(source)}}
	root.Children[0].Parent = root
	return &RegexTree{Root: root, Captop: 1}
}

// Host markers are private-use scalars that a host-validated tree carries for
// assertions the standard syntax cannot spell. Only FromMarkedSyntax
// interprets them; the boolean lowering keeps every scalar literal.
const (
	MarkerBackref         rune = 0x10FF00 // plus the group number, 1 through MaxMarkedGroup
	MarkerWordStart       rune = 0x10FFF0 // GNU \<
	MarkerWordEnd         rune = 0x10FFF1 // GNU \>
	MarkerNotAfterWord    rune = 0x10FFF2 // not preceded by an ASCII word scalar
	MarkerNotBeforeWord   rune = 0x10FFF3 // not followed by an ASCII word scalar
	MarkerWordBoundary    rune = 0x10FFF4 // GNU \b over the ASCII word class
	MarkerNotWordBoundary rune = 0x10FFF5 // GNU \B over the ASCII word class
	MaxMarkedGroup             = 0xEF
	markerFirst           rune = 0x10FF00
	markerLast            rune = 0x10FFFF
)

// IsMarker reports whether r is reserved for host markers.
func IsMarker(r rune) bool { return r >= markerFirst && r <= markerLast }

// FromMarkedSyntax lowers a host-validated tree with numbered captures,
// Unicode simple case folding for FoldCase literals and host markers.
// Word assertions use the fixed ASCII word class [0-9A-Za-z_].
func FromMarkedSyntax(source *syntax.Regexp, m *budget.Meter, inputBound int) *RegexTree {
	m.Alloc(512)
	l := lowering{m: m, inputBound: inputBound, marked: true}
	root := &RegexNode{T: NtCapture, M: 0, N: -1, Children: []*RegexNode{l.lower(source)}}
	root.Children[0].Parent = root
	return &RegexTree{Root: root, Captop: source.MaxCap() + 1}
}

type lowering struct {
	m          *budget.Meter
	inputBound int
	marked     bool
}

func (l lowering) lower(source *syntax.Regexp) *RegexNode {
	m, inputBound := l.m, l.inputBound
	m.Step(1)
	m.Alloc(256 + int64(len(source.Sub))*8 + int64(len(source.Rune))*8)
	node := &RegexNode{}
	switch source.Op {
	case syntax.OpNoMatch:
		node.T = NtNothing
	case syntax.OpEmptyMatch:
		node.T = NtEmpty
	case syntax.OpLiteral:
		if l.marked {
			return l.literal(source)
		}
		if source.Flags&syntax.FoldCase != 0 {
			panic(budget.Abort{fmt.Errorf("bounded typed ERE does not admit casefold flags")})
		}
		if len(source.Rune) == 1 {
			node.T = NtOne
			node.Ch = source.Rune[0]
		} else {
			node.T = NtMulti
			node.Str = append([]rune(nil), source.Rune...)
		}
	case syntax.OpCharClass:
		node.T = NtSet
		node.Set = &CharSet{}
		node.Set.ranges = make([]SingleRange, 0, len(source.Rune)/2)
		for i := 0; i < len(source.Rune); i += 2 {
			m.Step(1)
			node.Set.ranges = append(node.Set.ranges, SingleRange{First: source.Rune[i], Last: source.Rune[i+1]})
		}
	case syntax.OpAnyChar:
		node.T = NtSet
		node.Set = &CharSet{anything: true, ranges: []SingleRange{{First: 0, Last: 0x10ffff}}}
	case syntax.OpAnyCharNotNL:
		node.T = NtNotone
		node.Ch = '\n'
	case syntax.OpBeginLine:
		node.T = NtBol
	case syntax.OpEndLine:
		node.T = NtEol
	case syntax.OpBeginText:
		node.T = NtBeginning
	case syntax.OpEndText:
		node.T = NtEnd
	case syntax.OpWordBoundary:
		if l.marked {
			return l.boundary(true)
		}
		node.T = NtECMABoundary
	case syntax.OpNoWordBoundary:
		if l.marked {
			return l.boundary(false)
		}
		node.T = NtNonECMABoundary
	case syntax.OpCapture:
		node.T = NtGroup
		if l.marked {
			node.T, node.M, node.N = NtCapture, source.Cap, -1
		}
	case syntax.OpConcat:
		node.T = NtConcatenate
	case syntax.OpAlternate:
		node.T = NtAlternate
	case syntax.OpStar:
		node.T = NtLoop
		node.N = math.MaxInt32
	case syntax.OpPlus:
		node.T = NtLoop
		node.M = 1
		node.N = math.MaxInt32
	case syntax.OpQuest:
		node.T = NtLoop
		node.N = 1
	case syntax.OpRepeat:
		// Boolean matches on L input scalars need at most L+1 repetitions:
		// any longer successful sequence contains an empty iteration which can
		// be removed or duplicated without changing text/assertions. Captures and
		// backreferences are absent in this typed API. This preserves arbitrary
		// counts without introducing the VM's int32 counter as a grammar ceiling.
		node.T = NtLoop
		node.M = min(source.Min, inputBound)
		node.N = source.Max
		if node.N < 0 {
			node.N = math.MaxInt32
		} else {
			node.N = min(node.N, inputBound)
		}

	default:
		panic(budget.Abort{fmt.Errorf("unvalidated regex tree operation %d", source.Op)})
	}
	node.Children = make([]*RegexNode, len(source.Sub))
	for i, sub := range source.Sub {
		node.Children[i] = l.lower(sub)
		node.Children[i].Parent = node
	}
	return node
}

// literal lowers one marked literal: markers become assertions or
// references, FoldCase scalars become their simple-fold orbit.
func (l lowering) literal(source *syntax.Regexp) *RegexNode {
	parts := make([]*RegexNode, 0, len(source.Rune))
	for _, r := range source.Rune {
		l.m.Step(1)
		l.m.Alloc(256)
		switch {
		case IsMarker(r):
			node := l.marker(r)
			if node.T == NtRef && source.Flags&syntax.FoldCase != 0 {
				node.Options |= IgnoreCase
			}
			parts = append(parts, node)
		case source.Flags&syntax.FoldCase != 0:
			parts = append(parts, l.folded(r))
		default:
			parts = append(parts, &RegexNode{T: NtOne, Ch: r})
		}
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return l.join(NtConcatenate, parts...)
}

func (l lowering) folded(r rune) *RegexNode {
	orbit := []rune{r}
	for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
		l.m.Step(1)
		orbit = append(orbit, f)
	}
	if len(orbit) == 1 {
		return &RegexNode{T: NtOne, Ch: r}
	}
	slices.Sort(orbit)
	set := &CharSet{}
	for _, f := range orbit {
		set.ranges = append(set.ranges, SingleRange{First: f, Last: f})
	}
	return &RegexNode{T: NtSet, Set: set}
}

func asciiWord() *CharSet {
	return &CharSet{ranges: []SingleRange{{First: '0', Last: '9'}, {First: 'A', Last: 'Z'}, {First: '_', Last: '_'}, {First: 'a', Last: 'z'}}}
}

// look builds a one-scalar lookaround over the ASCII word class.
func (l lowering) look(positive, behind bool) *RegexNode {
	l.m.Alloc(512)
	t := NtNegLook
	if positive {
		t = NtPosLook
	}
	child := &RegexNode{T: NtSet, Set: asciiWord()}
	if behind {
		child.Options |= RightToLeft
	}
	return l.join(t, child)
}

func (l lowering) marker(r rune) *RegexNode {
	switch {
	case r > MarkerBackref && r <= MarkerBackref+MaxMarkedGroup:
		return &RegexNode{T: NtRef, M: int(r - MarkerBackref)}
	case r == MarkerWordBoundary:
		return l.boundary(true)
	case r == MarkerNotWordBoundary:
		return l.boundary(false)
	case r == MarkerWordStart:
		return l.join(NtConcatenate, l.look(false, true), l.look(true, false))
	case r == MarkerWordEnd:
		return l.join(NtConcatenate, l.look(true, true), l.look(false, false))
	case r == MarkerNotAfterWord:
		return l.look(false, true)
	case r == MarkerNotBeforeWord:
		return l.look(false, false)
	}
	panic(budget.Abort{fmt.Errorf("unknown host regex marker U+%06X", r)})
}

// boundary is an ASCII word boundary (or its negation) from lookarounds.
func (l lowering) boundary(at bool) *RegexNode {
	return l.join(NtAlternate,
		l.join(NtConcatenate, l.look(true, true), l.look(!at, false)),
		l.join(NtConcatenate, l.look(false, true), l.look(at, false)))
}

func (l lowering) join(t NodeType, children ...*RegexNode) *RegexNode {
	node := &RegexNode{T: t, Children: children}
	for _, child := range children {
		child.Parent = node
	}
	return node
}
