package gotreesitter

import "testing"

func TestWalkDFS(t *testing.T) {
	lang := queryTestLanguage()
	tree := buildSimpleTree(lang)

	var visited []string
	Walk(tree.RootNode(), func(node *Node, depth int) WalkAction {
		visited = append(visited, node.Type(lang))
		return WalkContinue
	})

	if len(visited) == 0 {
		t.Fatal("Walk visited no nodes")
	}
	if visited[0] != "program" {
		t.Errorf("first visited = %q, want %q", visited[0], "program")
	}
}

func TestWalkSkipChildren(t *testing.T) {
	lang := queryTestLanguage()
	tree := buildSimpleTree(lang)

	var visited []string
	Walk(tree.RootNode(), func(node *Node, depth int) WalkAction {
		visited = append(visited, node.Type(lang))
		if node.Type(lang) == "function_declaration" {
			return WalkSkipChildren
		}
		return WalkContinue
	})

	for _, name := range visited {
		if name == "identifier" || name == "number" {
			t.Errorf("visited %q despite WalkSkipChildren on function_declaration", name)
		}
	}
}

func TestWalkStop(t *testing.T) {
	lang := queryTestLanguage()
	tree := buildSimpleTree(lang)

	count := 0
	Walk(tree.RootNode(), func(node *Node, depth int) WalkAction {
		count++
		return WalkStop
	})

	if count != 1 {
		t.Errorf("visited %d nodes, want 1 (WalkStop after first)", count)
	}
}

func TestWalkNilNode(t *testing.T) {
	Walk(nil, func(node *Node, depth int) WalkAction {
		t.Fatal("should not be called")
		return WalkContinue
	})
}

func TestWalkDepth(t *testing.T) {
	lang := queryTestLanguage()
	tree := buildSimpleTree(lang)

	maxDepth := 0
	Walk(tree.RootNode(), func(node *Node, depth int) WalkAction {
		if depth > maxDepth {
			maxDepth = depth
		}
		return WalkContinue
	})

	if maxDepth < 2 {
		t.Errorf("maxDepth = %d, want >= 2", maxDepth)
	}
}
