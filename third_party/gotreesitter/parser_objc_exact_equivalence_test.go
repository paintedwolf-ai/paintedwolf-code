package gotreesitter

import "testing"

func TestObjcStackEquivalenceKeepsDeepAlternativeShapes(t *testing.T) {
	left := nestedEquivalenceTestNode(12, 1)
	right := nestedEquivalenceTestNode(12, 2)

	frontierLanguage := &Language{
		Name:           "frontier",
		AliasSequences: [][]Symbol{{1}},
	}
	if !stackEntryNodesEquivalentForLanguageWithScratch(nil, frontierLanguage, left, right) {
		t.Fatal("bounded frontier did not stop before the deep leaf difference")
	}

	objcLanguage := &Language{ExactStackNodeEquivalenceCertified: true}
	if stackEntryNodesEquivalentForLanguageWithScratch(nil, objcLanguage, left, right) {
		t.Fatal("Objective-C exact equivalence merged different deep leaves")
	}
}

func nestedEquivalenceTestNode(depth int, leaf Symbol) *Node {
	node := &Node{symbol: leaf, flags: nodeFlagNamed}
	for range depth {
		node = &Node{
			symbol:   3,
			flags:    nodeFlagNamed,
			children: []*Node{node},
		}
	}
	return node
}
