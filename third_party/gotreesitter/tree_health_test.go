package gotreesitter

import "testing"

func TestNodeHasErrorOrMissingFindsDescendantMissingNode(t *testing.T) {
	missing := &Node{}
	missing.setMissing(true)
	root := &Node{children: []*Node{missing}}

	if root.HasError() {
		t.Fatal("HasError reports a missing child")
	}
	if !root.HasErrorOrMissing() {
		t.Fatal("HasErrorOrMissing missed a descendant missing node")
	}
}

func TestNodeHasErrorOrMissingLeavesHealthyTreeClean(t *testing.T) {
	root := &Node{children: []*Node{{}, {children: []*Node{{}}}}}
	if root.HasErrorOrMissing() {
		t.Fatal("HasErrorOrMissing reports a healthy tree")
	}
}
