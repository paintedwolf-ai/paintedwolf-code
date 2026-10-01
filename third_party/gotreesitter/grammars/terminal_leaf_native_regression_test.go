//go:build !grammar_subset

package grammars

import (
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

func TestTerminalLeafInvariantAcrossParseRoutes(t *testing.T) {
	lang := GoLanguage()
	source := []byte(`package p

type Number interface {
	~int | ~string
}

func F[T Number](value T) {
	_ = value
	_ = true
}
`)

	for _, receipt := range retiredDispatchRouteReceipts(t, lang, source) {
		sameNameTerminals, aliases := assertTerminalLeafMutationAbsent(t, receipt.name, receipt.tree.RootNode(), lang)
		if sameNameTerminals == 0 {
			t.Fatalf("%s observed no same-name terminal witness", receipt.name)
		}
		if aliases == 0 {
			t.Fatalf("%s observed no visible alias witness", receipt.name)
		}
		if receipt.name == "incremental" {
			profile := receipt.incrementalProfile
			if !profile.OldTreeReuseRoute || profile.ReusedSubtrees == 0 || profile.ReusedBytes == 0 {
				t.Fatalf("incremental route did not reuse the old tree: %+v", profile)
			}
		}
	}
}

func assertTerminalLeafMutationAbsent(t *testing.T, route string, root *gotreesitter.Node, lang *gotreesitter.Language) (int, int) {
	t.Helper()
	if root == nil || root.HasError() {
		t.Fatalf("%s returned an unclean tree: %v", route, root)
	}
	aliasTargets := terminalLeafVisibleAliasTargets(lang)
	sameNameTerminals := 0
	aliases := 0
	var walk func(*gotreesitter.Node)
	walk = func(node *gotreesitter.Node) {
		if node == nil {
			return
		}
		if terminalLeafSameNameTerminalWitness(node, lang) {
			sameNameTerminals++
		}
		if terminalLeafVisibleAliasTarget(node, lang, aliasTargets) {
			aliases++
		}
		if terminalLeafRetiredShape(node, lang, aliasTargets) {
			t.Errorf("%s retained retired terminal-leaf shape at %s", route, node.Type(lang))
		}
		for index := 0; index < node.ChildCount(); index++ {
			walk(node.Child(index))
		}
	}
	walk(root)
	return sameNameTerminals, aliases
}

func terminalLeafRetiredShape(node *gotreesitter.Node, lang *gotreesitter.Language, aliasTargets []bool) bool {
	if node == nil || node.IsExtra() || node.IsMissing() || node.HasError() || node.ChildCount() != 1 {
		return false
	}
	if !terminalLeafVisibleTerminalLike(node.Symbol(), lang, aliasTargets) || node.FieldNameForChild(0, lang) != "" {
		return false
	}
	child := node.Child(0)
	if child == nil || child.IsNamed() || child.IsExtra() || child.IsMissing() || child.HasError() {
		return false
	}
	if !terminalLeafVisibleTerminalLike(child.Symbol(), lang, aliasTargets) {
		return false
	}
	parentIsTerminal := terminalLeafVisibleTerminal(node.Symbol(), lang)
	parentIsAlias := terminalLeafVisibleAliasTarget(node, lang, aliasTargets)
	if (!parentIsTerminal || parentIsAlias) && node.Type(lang) != child.Type(lang) {
		return false
	}
	return true
}

func terminalLeafSameNameTerminalWitness(node *gotreesitter.Node, lang *gotreesitter.Language) bool {
	return node != nil && node.Type(lang) == "identifier" &&
		terminalLeafVisibleTerminal(node.Symbol(), lang)
}

func terminalLeafVisibleTerminalLike(symbol gotreesitter.Symbol, lang *gotreesitter.Language, aliasTargets []bool) bool {
	return terminalLeafVisibleTerminal(symbol, lang) ||
		(int(symbol) < len(aliasTargets) && aliasTargets[symbol])
}

func terminalLeafVisibleTerminal(symbol gotreesitter.Symbol, lang *gotreesitter.Language) bool {
	return lang != nil && uint32(symbol) < lang.TokenCount &&
		int(symbol) < len(lang.SymbolMetadata) && lang.SymbolMetadata[symbol].Visible
}

func terminalLeafVisibleAliasTarget(node *gotreesitter.Node, lang *gotreesitter.Language, aliasTargets []bool) bool {
	if node == nil || lang == nil {
		return false
	}
	symbol := node.Symbol()
	return int(symbol) < len(aliasTargets) && aliasTargets[symbol]
}

func terminalLeafVisibleAliasTargets(lang *gotreesitter.Language) []bool {
	if lang == nil {
		return nil
	}
	targets := make([]bool, len(lang.SymbolMetadata))
	for _, sequence := range lang.AliasSequences {
		for _, symbol := range sequence {
			if int(symbol) < len(targets) && lang.SymbolMetadata[symbol].Visible {
				targets[symbol] = true
			}
		}
	}
	return targets
}
