package gotreesitter

import "testing"

func TestNormalizeRootSourceStartUsesFirstRetainedChildOwnership(t *testing.T) {
	tests := []struct {
		name       string
		source     []byte
		rootStart  uint32
		childStart uint32
		wantStart  uint32
		wantPoint  Point
	}{
		{
			name:       "unowned leading newline",
			source:     []byte("\nvalue"),
			childStart: 1,
			wantStart:  1,
			wantPoint:  Point{Row: 1},
		},
		{
			name:       "unowned BOM and newline",
			source:     []byte("\xef\xbb\xbf\nvalue"),
			childStart: 4,
			wantStart:  4,
			wantPoint:  Point{Row: 1},
		},
		{
			name:       "child owns leading recovery bytes",
			source:     []byte("\nvalue"),
			childStart: 0,
			wantStart:  0,
		},
		{
			name:       "nontrivia gap remains covered",
			source:     []byte("x value"),
			childStart: 2,
			wantStart:  0,
		},
		{
			name:       "dropped source token remains covered",
			source:     []byte("\nvalue"),
			rootStart:  3,
			childStart: 3,
			wantStart:  1,
			wantPoint:  Point{Row: 1},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			arena := newNodeArena(arenaClassFull)
			childPoint := advancePointByBytes(Point{}, test.source[:test.childStart])
			child := newLeafNodeInArena(
				arena,
				1,
				true,
				test.childStart,
				uint32(len(test.source)),
				childPoint,
				advancePointByBytes(Point{}, test.source),
			)
			root := newParentNodeInArena(arena, 2, true, []*Node{child}, nil, 0)
			root.startByte = test.rootStart
			root.startPoint = advancePointByBytes(Point{}, test.source[:test.rootStart])

			(&Parser{}).normalizeRootSourceStart(root, test.source)

			if root.startByte != test.wantStart {
				t.Fatalf("root start = %d, want %d", root.startByte, test.wantStart)
			}
			if root.startPoint != test.wantPoint {
				t.Fatalf("root point = %#v, want %#v", root.startPoint, test.wantPoint)
			}
		})
	}
}

func TestNormalizeRootSourceStartPreservesIncludedRangeSpan(t *testing.T) {
	source := []byte("\nvalue")
	arena := newNodeArena(arenaClassFull)
	child := newLeafNodeInArena(
		arena,
		1,
		true,
		1,
		uint32(len(source)),
		Point{Row: 1},
		Point{Row: 1, Column: 5},
	)
	root := newParentNodeInArena(arena, 2, true, []*Node{child}, nil, 0)
	root.startByte = 0
	root.startPoint = Point{}
	parser := &Parser{included: []Range{{StartByte: 0, EndByte: uint32(len(source))}}}

	parser.normalizeRootSourceStart(root, source)

	if root.startByte != 0 || root.startPoint != (Point{}) {
		t.Fatalf("included-range root moved to %d at %#v", root.startByte, root.startPoint)
	}
}
