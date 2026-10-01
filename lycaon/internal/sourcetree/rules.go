// Package sourcetree projects shared directory facts through one client's intent.
package sourcetree

import (
	"path"
	"strings"
)

type Address struct{ Root, Path string }
type Disclosure struct {
	Open, Recursive bool
	Explicit        bool
}

// Rules share immutable ordered exceptions across presentations.
type Rules struct{ root *ruleNode }

func ruleKey(address Address) string { return address.Root + "\x00" + address.Path }

func (r *Rules) Set(address Address, disclosure Disclosure) {
	key := ruleKey(address)
	previous := findRule(r.root, key)
	entry := ruleValue{address: address, open: disclosure.Open}
	if previous != nil {
		entry.recursive, entry.hasRecursive = previous.value.recursive, previous.value.hasRecursive
	}
	if disclosure.Recursive {
		prefix := address.Root + "\x00"
		lower, upper := prefix, prefix+"\xff"
		if address.Path != "." {
			lower = key + "/"
			upper = key + "0"
		}
		left, rest := splitRules(r.root, lower)
		_, right := splitRules(rest, upper)
		r.root = mergeRules(left, right)
		entry.recursive, entry.hasRecursive = disclosure.Open, true
	}
	left, rest := splitRules(r.root, key)
	_, right := splitRules(rest, key+"\x00")
	r.root = mergeRules(mergeRules(left, newRule(key, entry)), right)
}

func (r *Rules) At(address Address) Disclosure { return r.atDefault(address, address.Path == ".") }
func (r *Rules) atDefault(address Address, open bool) Disclosure {
	exact := findRule(r.root, ruleKey(address))
	result := Disclosure{Open: open}
	for current := address.Path; ; current = path.Dir(current) {
		node := findRule(r.root, ruleKey(Address{Root: address.Root, Path: current}))
		if node != nil && node.value.hasRecursive {
			result.Open = node.value.recursive
			result.Recursive = node.value.recursive
			break
		}
		if current == "." {
			break
		}
	}
	if exact != nil {
		result.Open = exact.value.open
		result.Explicit = true
	}
	return result
}

func under(candidate, parent string) bool {
	return candidate == parent || parent == "." || strings.HasPrefix(candidate, parent+"/")
}

// Branches seeks past each child subtree without visiting its descendants.
func (r *Rules) Branches(address Address) []string {
	prefix := address.Root + "\x00"
	rel := ""
	if address.Path != "." {
		rel = address.Path + "/"
	}
	cursor := prefix + rel
	var out []string
	seen := make(map[string]bool)
	for {
		node := lowerRule(r.root, cursor)
		if node == nil || !strings.HasPrefix(node.key, prefix+rel) {
			break
		}
		if node.value.address.Path == "." {
			cursor = node.key + "\x00"
			continue
		}
		suffix := strings.TrimPrefix(node.value.address.Path, rel)
		child, _, nested := strings.Cut(suffix, "/")
		child = rel + child
		if !seen[child] {
			out = append(out, child)
			seen[child] = true
		}
		cursor = node.key + "\x00"
		if nested {
			cursor = prefix + child + "0"
		}
	}
	return out
}

// The recursive close keeps a boundary's descendants closed too, so opening it
// later shows one level.
var derivedDisclosure = ruleValue{open: false, recursive: false, hasRecursive: true, derived: true}

// recursiveAnchors lists the addresses whose subtrees are open recursively.
func (r *Rules) recursiveAnchors() []Address {
	var anchors []Address
	visitRules(r.root, func(value ruleValue) {
		if value.hasRecursive && value.recursive {
			anchors = append(anchors, value.address)
		}
	})
	return anchors
}

// reconcileDerived makes the derived boundaries exactly wanted, leaving every
// address that carries intent alone.
func (r *Rules) reconcileDerived(wanted map[Address]bool) {
	var stale []string
	visitRules(r.root, func(value ruleValue) {
		if value.derived && !wanted[value.address] {
			stale = append(stale, ruleKey(value.address))
		}
	})
	for _, key := range stale {
		left, rest := splitRules(r.root, key)
		_, right := splitRules(rest, key+"\x00")
		r.root = mergeRules(left, right)
	}
	for address := range wanted {
		key := ruleKey(address)
		if findRule(r.root, key) != nil {
			continue
		}
		value := derivedDisclosure
		value.address = address
		left, right := splitRules(r.root, key)
		r.root = mergeRules(mergeRules(left, newRule(key, value)), right)
	}
}

// Derived lists the boundaries the host holds closed under recursive rules.
func (r *Rules) Derived() []Address {
	var out []Address
	visitRules(r.root, func(value ruleValue) {
		if value.derived {
			out = append(out, value.address)
		}
	})
	return out
}

func (r *Rules) Clone() Rules { return *r }
func (r *Rules) count() int {
	if r.root == nil {
		return 0
	}
	return r.root.count
}
func (r *Rules) derivedCount() int {
	if r.root == nil {
		return 0
	}
	return r.root.derived
}
func (r *Rules) bytes() int64 {
	if r.root == nil {
		return 0
	}
	return r.root.bytes
}
