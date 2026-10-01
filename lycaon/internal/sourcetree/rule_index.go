package sourcetree

import "hash/maphash"

var ruleSeed = maphash.MakeSeed()

type ruleValue struct {
	address                       Address
	open, recursive, hasRecursive bool
	// Derived boundaries live with their recursive anchor and stay outside published intent.
	derived bool
}

type ruleNode struct {
	key         string
	value       ruleValue
	priority    uint64
	left, right *ruleNode
	// Published intent and derived boundaries have separate subtree counts.
	count, derived int
	bytes          int64
}

func newRule(key string, value ruleValue) *ruleNode {
	return rebuildRule(&ruleNode{key: key, value: value, priority: maphash.String(ruleSeed, key)}, nil, nil)
}

func rebuildRule(node, left, right *ruleNode) *ruleNode {
	copy := *node
	copy.left, copy.right = left, right
	copy.count, copy.derived = 1, 0
	if copy.value.derived {
		copy.count, copy.derived = 0, 1
	} else if copy.value.hasRecursive && copy.value.recursive != copy.value.open {
		copy.count++
	}
	copy.bytes = int64(128 + len(copy.key) + len(copy.value.address.Root) + len(copy.value.address.Path))
	for _, child := range []*ruleNode{left, right} {
		if child != nil {
			copy.count += child.count
			copy.derived += child.derived
			copy.bytes += child.bytes
		}
	}
	return &copy
}

func splitRules(node *ruleNode, key string) (*ruleNode, *ruleNode) {
	if node == nil {
		return nil, nil
	}
	if node.key < key {
		left, right := splitRules(node.right, key)
		return rebuildRule(node, node.left, left), right
	}
	left, right := splitRules(node.left, key)
	return left, rebuildRule(node, right, node.right)
}

func mergeRules(left, right *ruleNode) *ruleNode {
	if left == nil {
		return right
	}
	if right == nil {
		return left
	}
	if left.priority < right.priority {
		return rebuildRule(left, left.left, mergeRules(left.right, right))
	}
	return rebuildRule(right, mergeRules(left, right.left), right.right)
}

func findRule(node *ruleNode, key string) *ruleNode {
	for node != nil {
		if key == node.key {
			return node
		}
		if key < node.key {
			node = node.left
		} else {
			node = node.right
		}
	}
	return nil
}

func lowerRule(node *ruleNode, key string) *ruleNode {
	var found *ruleNode
	for node != nil {
		if node.key >= key {
			found = node
			node = node.left
		} else {
			node = node.right
		}
	}
	return found
}

func visitRules(node *ruleNode, visit func(ruleValue)) {
	if node == nil {
		return
	}
	visitRules(node.left, visit)
	visit(node.value)
	visitRules(node.right, visit)
}
