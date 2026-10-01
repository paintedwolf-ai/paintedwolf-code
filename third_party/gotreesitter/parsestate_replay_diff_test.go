package gotreesitter_test

import (
	"fmt"
	"sort"
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/odvcencio/gotreesitter/internal/benchfixtures"
)

// TestParseStateReplayDifferential is the go/no-go proof for Phase-3 Lane 2:
// it parses a corpus both ways -- production (which records parseState /
// preGotoState per node) and replay (which reconstructs them from the LR
// goto/shift tables + tree shape) -- and reports, per node class, how many
// nodes the replay model reproduces exactly. Any mismatch localizes a node
// class whose state is not a plain table transition of its visible symbol.
//
// This test is diagnostic-first: it never fails on mismatch, it PRINTS the
// full breakdown so the exactness of the visible-tree replay model is measured
// on real trees (it is only ~0.30% exact -- the visible tree has lost the
// hidden-node gotos and pre-alias symbols replay needs). The authoritative
// go/no-go gate runs over the FULL compact derivation instead and asserts the
// classes that must be exact: TestParseStateReplayCompactDifferential
// (parsestate_replay_compact_diff_test.go).
func TestParseStateReplayDifferential(t *testing.T) {
	for _, c := range replayCorpus(t) {
		c := c
		t.Run(c.name, func(t *testing.T) {
			parser := gotreesitter.NewParser(c.lang)
			tree, err := parser.Parse(c.source)
			if err != nil {
				t.Fatalf("parse %s: %v", c.name, err)
			}
			defer tree.Release()
			root := tree.RootNode()
			if root == nil {
				t.Fatalf("nil root for %s", c.name)
			}
			rep := gotreesitter.ReplayDiffTree(parser, root, 40)

			pct := 100.0
			if rep.TotalNodes > 0 {
				pct = 100.0 * float64(rep.TotalNodes-rep.Mismatched) / float64(rep.TotalNodes)
			}
			t.Logf("%s: nodes=%d exact=%d (%.4f%%) mismatched=%d (preGoto=%d parseState=%d) rootPreGoto rec=%d seed=%d",
				c.name, rep.TotalNodes, rep.TotalNodes-rep.Mismatched, pct,
				rep.Mismatched, rep.PreGotoMismatched, rep.ParseStateMismatch,
				rep.RootPreGotoRecorded, rep.RootPreGotoSeed)

			for _, st := range rep.SortedClassStats() {
				if st.Mismatched == 0 {
					continue
				}
				t.Logf("  class %-28s total=%-6d mismatched=%-6d (%.2f%%)",
					st.Class, st.Total, st.Mismatched,
					100.0*float64(st.Mismatched)/float64(st.Total))
			}
			for i, s := range rep.Samples {
				if i >= 12 {
					t.Logf("  ... (%d more samples)", len(rep.Samples)-12)
					break
				}
				t.Logf("  sample[%d] %s (%s) depth=%d pre rec=%d rep=%d ps rec=%d rep=%d",
					i, s.SymbolName, s.Class, s.Depth,
					s.PreGotoRec, s.PreGotoReplay, s.ParseStateRec, s.ParseStateRep)
			}
		})
	}
}

// TestParseStateReplayCorpusSummary prints one aggregate line across the whole
// corpus so the go/no-go signal is a single readable number.
func TestParseStateReplayCorpusSummary(t *testing.T) {
	var totalNodes, totalMismatch int
	classAgg := map[string]*gotreesitter.ReplayClassStat{}
	for _, c := range replayCorpus(t) {
		parser := gotreesitter.NewParser(c.lang)
		tree, err := parser.Parse(c.source)
		if err != nil {
			t.Fatalf("parse %s: %v", c.name, err)
		}
		rep := gotreesitter.ReplayDiffTree(parser, tree.RootNode(), 0)
		tree.Release()
		totalNodes += rep.TotalNodes
		totalMismatch += rep.Mismatched
		for _, st := range rep.SortedClassStats() {
			agg := classAgg[st.Class]
			if agg == nil {
				agg = &gotreesitter.ReplayClassStat{Class: st.Class}
				classAgg[st.Class] = agg
			}
			agg.Total += st.Total
			agg.Mismatched += st.Mismatched
		}
	}
	pct := 100.0
	if totalNodes > 0 {
		pct = 100.0 * float64(totalNodes-totalMismatch) / float64(totalNodes)
	}
	t.Logf("CORPUS: nodes=%d exact=%d (%.4f%%) mismatched=%d", totalNodes, totalNodes-totalMismatch, pct, totalMismatch)
	keys := make([]string, 0, len(classAgg))
	for k := range classAgg {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return classAgg[keys[i]].Mismatched > classAgg[keys[j]].Mismatched
	})
	for _, k := range keys {
		st := classAgg[k]
		if st.Total == 0 {
			continue
		}
		t.Logf("  %-30s total=%-7d mismatched=%-7d (%.4f%%)", k, st.Total, st.Mismatched,
			100.0*float64(st.Mismatched)/float64(st.Total))
	}
}

type replayCase struct {
	name   string
	lang   *gotreesitter.Language
	source []byte
}

func replayCorpus(t *testing.T) []replayCase {
	t.Helper()
	cases := []replayCase{
		{name: "js/basic", lang: grammars.JavascriptLanguage(), source: []byte(jsReplaySource)},
		{name: "css/basic", lang: grammars.CssLanguage(), source: []byte(cssReplaySource)},
		{name: "python/basic", lang: grammars.PythonLanguage(), source: []byte(pyReplaySource)},
		{name: "typescript/basic", lang: grammars.TypescriptLanguage(), source: []byte(tsReplaySource)},
		{name: "go/basic", lang: grammars.GoLanguage(), source: []byte(goReplaySource)},
	}
	// The four campaign fixtures (real Go source).
	loaded, err := benchfixtures.LoadGoFullParseFixtures()
	if err != nil {
		t.Fatalf("load fixtures: %v", err)
	}
	goLang := grammars.GoLanguage()
	for _, lf := range loaded {
		cases = append(cases, replayCase{
			name:   fmt.Sprintf("fixture/%s", lf.Fixture.ID),
			lang:   goLang,
			source: lf.Source,
		})
	}
	return cases
}

const goReplaySource = `package main

import (
	"fmt"
	"strings"
)

type Shape interface {
	Area() float64
}

type Rect struct {
	W, H float64
}

func (r Rect) Area() float64 { return r.W * r.H }

func Map[T any, U any](xs []T, f func(T) U) []U {
	out := make([]U, 0, len(xs))
	for _, x := range xs {
		out = append(out, f(x))
	}
	return out
}

func main() {
	shapes := []Shape{Rect{W: 3, H: 4}}
	for i, s := range shapes {
		fmt.Printf("%d: %.2f\n", i, s.Area())
	}
	_ = strings.ToUpper("hi")
	switch x := any(1).(type) {
	case int:
		_ = x
	default:
	}
}
`

const jsReplaySource = `const add = (a, b) => a + b;

class Point {
  constructor(x, y) {
    this.x = x;
    this.y = y;
  }
  norm() {
    return Math.sqrt(this.x ** 2 + this.y ** 2);
  }
}

async function main() {
  const p = new Point(3, 4);
  const xs = [1, 2, 3].map((n) => n * 2).filter((n) => n > 2);
  for (const x of xs) {
    console.log(x, add(x, 1));
  }
  await Promise.resolve(p.norm());
}

main();
`

const cssReplaySource = `:root {
  --main-color: #3366ff;
}

.container > .item:nth-child(2n+1) {
  color: var(--main-color);
  margin: 0 auto;
  padding: 1em 2em;
}

@media (max-width: 600px) {
  .container {
    display: flex;
    flex-direction: column;
  }
}
`

const pyReplaySource = `import os
from typing import List, Optional


class Node:
    def __init__(self, value: int, children: Optional[List["Node"]] = None):
        self.value = value
        self.children = children or []

    def total(self) -> int:
        return self.value + sum(c.total() for c in self.children)


def build(depth: int) -> Node:
    if depth == 0:
        return Node(1)
    return Node(depth, [build(depth - 1) for _ in range(2)])


if __name__ == "__main__":
    root = build(3)
    print(root.total(), os.getpid())
`

const tsReplaySource = `interface Shape {
  area(): number;
}

type Maybe<T> = T | null;

class Circle implements Shape {
  constructor(private readonly r: number) {}
  area(): number {
    return Math.PI * this.r ** 2;
  }
}

function firstArea(shapes: Shape[]): Maybe<number> {
  return shapes.length > 0 ? shapes[0].area() : null;
}

const shapes: Shape[] = [new Circle(2), new Circle(3)];
const total = shapes.reduce((acc: number, s: Shape) => acc + s.area(), 0);
console.log(firstArea(shapes), total);
`
