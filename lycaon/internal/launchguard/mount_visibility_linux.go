//go:build linux

package launchguard

import (
	"errors"
	"strings"
)

// A mount can hide a sibling's subtree without becoming its mountinfo parent.
// Derive original visibility before peeling any cover; revealed underlays are
// pruned but must never become new imported volumes merely because of peeling.
func hiddenMountRoots(entries []mountEntry) (map[int]bool, error) {
	byID := make(map[int]mountEntry, len(entries))
	children := make(map[int][]mountEntry, len(entries))
	for _, entry := range entries {
		byID[entry.id] = entry
		children[entry.parent] = append(children[entry.parent], entry)
	}
	type visit struct {
		entry    mountEntry
		occluded bool
	}
	var stack []visit
	for _, entry := range entries {
		if _, exists := byID[entry.parent]; !exists {
			stack = append(stack, visit{entry: entry})
		}
	}
	hidden := make(map[int]bool)
	for len(stack) > 0 {
		item := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		members := children[item.entry.id]
		aliases := &mountAliasNode{}
		for _, member := range members {
			if !aliases.insert(member.mountPoint) {
				return nil, errors.New("ambiguous sibling mount aliases")
			}
			if member.mountPoint == item.entry.mountPoint {
				hidden[item.entry.id] = true
			}
		}
		if item.occluded {
			hidden[item.entry.id] = true
		}
		for _, member := range members {
			stack = append(stack, visit{entry: member, occluded: item.occluded || aliases.strictAncestor(member.mountPoint)})
		}
	}
	return hidden, nil
}

type mountAliasNode struct {
	terminal bool
	children map[string]*mountAliasNode
}

func (root *mountAliasNode) insert(alias string) bool {
	node := root
	for component := range strings.SplitSeq(strings.TrimPrefix(alias, "/"), "/") {
		if component == "" {
			continue
		}
		if node.children == nil {
			node.children = make(map[string]*mountAliasNode)
		}
		child := node.children[component]
		if child == nil {
			child = &mountAliasNode{}
			node.children[component] = child
		}
		node = child
	}
	if node.terminal {
		return false
	}
	node.terminal = true
	return true
}

func (root *mountAliasNode) strictAncestor(alias string) bool {
	node := root
	for component := range strings.SplitSeq(strings.TrimPrefix(alias, "/"), "/") {
		if component == "" {
			continue
		}
		if node.terminal {
			return true
		}
		node = node.children[component]
		if node == nil {
			return false
		}
	}
	return false
}
