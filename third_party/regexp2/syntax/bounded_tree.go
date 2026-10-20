package syntax

import (
	"fmt"
	"math"
	"regexp/syntax"

	"github.com/dlclark/regexp2/v2/internal/budget"
)

// FromValidatedSyntax lowers a host-validated regular tree without accepting a
// second textual grammar. Captures are unobservable for this boolean-only API.
func FromValidatedSyntax(source *syntax.Regexp, m *budget.Meter, inputBound int) *RegexTree {
	m.Alloc(512)
	root := &RegexNode{T: NtCapture, M: 0, N: -1, Children: []*RegexNode{lowerValidated(source, m, inputBound)}}
	root.Children[0].Parent = root
	return &RegexTree{Root: root, Captop: 1}
}
func lowerValidated(source *syntax.Regexp, m *budget.Meter, inputBound int) *RegexNode {
	m.Step(1)
	m.Alloc(256 + int64(len(source.Sub))*8 + int64(len(source.Rune))*8)
	node := &RegexNode{}
	switch source.Op {
	case syntax.OpNoMatch:
		node.T = NtNothing
	case syntax.OpEmptyMatch:
		node.T = NtEmpty
	case syntax.OpLiteral:
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
		node.T = NtECMABoundary
	case syntax.OpNoWordBoundary:
		node.T = NtNonECMABoundary
	case syntax.OpCapture:
		node.T = NtGroup
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
		node.Children[i] = lowerValidated(sub, m, inputBound)
		node.Children[i].Parent = node
	}
	return node
}
