// Copyright 2011 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package syntax

import "github.com/dlclark/regexp2/v2/internal/budget"

// Equal reports whether x and y have identical structure.
func equalMetered(x, y *Regexp, m *budget.Meter) bool {
	m.Step(1)
	if x == nil || y == nil {
		return x == y
	}
	if x.Op != y.Op {
		return false
	}
	switch x.Op {
	case OpEndText:
		// The parse flags remember whether this is \z or \Z.
		if x.Flags&WasDollar != y.Flags&WasDollar {
			return false
		}

	case OpLiteral, OpCharClass:
		return x.Flags&FoldCase == y.Flags&FoldCase && equalRunes(x.Rune, y.Rune, m)

	case OpAlternate, OpConcat:
		return equalSubs(x.Sub, y.Sub, m)

	case OpStar, OpPlus, OpQuest:
		if x.Flags&NonGreedy != y.Flags&NonGreedy || !equalMetered(x.Sub[0], y.Sub[0], m) {
			return false
		}

	case OpRepeat:
		if x.Flags&NonGreedy != y.Flags&NonGreedy || x.Min != y.Min || x.Max != y.Max || !equalMetered(x.Sub[0], y.Sub[0], m) {
			return false
		}

	case OpCapture:
		if x.Cap != y.Cap || x.Name != y.Name || !equalMetered(x.Sub[0], y.Sub[0], m) {
			return false
		}
	}
	return true
}

func equalRunes(x, y []rune, m *budget.Meter) bool {
	if len(x) != len(y) {
		return false
	}
	for i := range x {
		m.Step(1)
		if x[i] != y[i] {
			return false
		}
	}
	return true
}
func equalSubs(x, y []*Regexp, m *budget.Meter) bool {
	if len(x) != len(y) {
		return false
	}
	for i := range x {
		m.Step(1)
		if !equalMetered(x[i], y[i], m) {
			return false
		}
	}
	return true
}
