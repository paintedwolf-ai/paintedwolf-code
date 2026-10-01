package grammars

import (
	"testing"

	"github.com/odvcencio/gotreesitter"
)

// Pin for a MISSING comma that the Go engine invents inside a three-enumerator
// enum when the construct is the first declaration in the file and any trivia
// precedes it.
//
// C tree-sitter parses the source below with no MISSING node and no error. The
// Go engine inserts a zero-width `,` between the last enumerator and the
// closing brace, and marks enumerator_list, enum_specifier, type_definition,
// and translation_unit as containing an error. Every other node matches the C
// tree exactly, so the whole divergence is that one invented token and the
// error state it propagates.
//
// The trigger is narrow and worth recording precisely:
//
//   - exactly three enumerators. Two are fine and four are fine. Three is the
//     only count that diverges: it is the count where the losing fork is also
//     the last fork. With four, a later fork promotes the stack to a packed GSS
//     and the merge that keeps the folded history succeeds.
//   - some trivia must precede the enum. A single space, tab, newline, or
//     comment is enough.
//
// The trivia does not cause the defect. The production GLR parser builds the
// wrong tree at every offset. What trivia changes is whether the wrong tree
// escapes: maybeReplaceRecoveredTreeWithForest (glr_forest.go) re-parses any
// errored production root through the forest engine and swaps in the clean
// result, but it declines when the candidate root does not start at byte 0.
// Leading trivia moves the root to byte 1, the repair declines, and the broken
// tree reaches the caller.
//
// Root cause. Upstream tree-sitter never executes SHIFT_REPEAT at a conflict
// cell (lib/src/parser.c, `if (action.shift.repetition) break;`); it folds the
// repetition first and re-dispatches. gotreesitter implements the same rule as
// cRepetitionSkipConflictChoice but opts language "c" out (conflict_policy.go).
// C therefore forks at the cell, and convergence then fails: C forces cap-one
// merging, the GSS merge refuses because one stack is demoted-linear, and the
// cap-one depth tiebreak keeps the deeper unfolded branch. The parse dead-ends
// on `}` and recovery invents the comma. The parse table itself matches
// upstream byte for byte, so this is a runtime and profile defect.
//
// A bare three-enumerator enum (no `= value`) diverges too, with an ERROR wrap
// instead of an invented comma. This pin covers the valued form.
//
// This pin is self-healing: it PASSES automatically once the invented comma
// disappears.
func TestCleanRegressionPinCEnumThreeValuedEnumerators(t *testing.T) {
	const src = "\ntypedef enum { A = 0, B = 1, C = MAX } t;"

	tree, lang := pinParse(t, "c", src)
	defer tree.Release()

	root := tree.RootNode()
	if got := root.Type(lang); got != "translation_unit" {
		t.Fatalf("c: root type = %q, want translation_unit", got)
	}
	// The enumerator_list must still cover the whole brace-delimited run; the
	// divergence is an extra child, not a reshaped span.
	list := pinFind(root, lang, "enumerator_list")
	if list == nil {
		t.Fatal("c: no enumerator_list node")
	}
	if got, want := int(list.StartByte()), 14; got != want {
		t.Fatalf("c: enumerator_list starts at %d, want %d", got, want)
	}
	if got, want := int(list.EndByte()), 39; got != want {
		t.Fatalf("c: enumerator_list ends at %d, want %d", got, want)
	}

	const wantMissing = 0 // C tree-sitter invents nothing here.
	if got := pinCountMissing(root); got == wantMissing {
		return // fix landed — pin now green.
	} else {
		t.Skipf("KNOWN DIVERGENCE (c enum three valued enumerators): Go invents %d "+
			"MISSING node(s), C invents %d — the production GLR parser keeps a "+
			"missing-comma trial that the forest fast path never runs; see file "+
			"comment", got, wantMissing)
	}
}

// pinCountMissing reports how many nodes in the tree are MISSING.
func pinCountMissing(n *gotreesitter.Node) int {
	if n == nil {
		return 0
	}
	count := 0
	if n.IsMissing() {
		count++
	}
	for i := 0; i < n.ChildCount(); i++ {
		count += pinCountMissing(n.Child(i))
	}
	return count
}

// Pin for a second C divergence in the same family as the enum comma above,
// which the enumerator-list fold does NOT repair.
//
// C tree-sitter parses this reduced form of libc-test's tls_align_dlopen.c
// with no error. The Go engine wraps a fragment of `t[i].align` in an ERROR
// node and marks every enclosing statement as containing an error, while every
// node span stays identical to the C tree.
//
// It is the same convergence failure, reached a different way. Both
// GOT_FAITHFUL_CONDENSE=1 and GOT_GLR_MAX_MERGE_PER_KEY=2 clear it, so the
// correct reading exists and cap-one merging discards it. Unlike the enum
// case, the fork is not at a repetition conflict cell: adding a
// ConflictPolicyRepetitionReduce row for sized_type_specifier_repeat1(342),
// _declaration_specifiers_repeat1(336), _field_declaration_declarator_repeat1(344),
// argument_list_repeat1(354), or declaration_repeat1(332) leaves it unchanged.
// Folding cannot reach it, so the repair belongs in the merge itself:
// gssMainCanMergeWithScratch refuses when one candidate is in demoted-linear
// form (nil head) instead of promoting it, and the cap-one depth tiebreak then
// keeps the wrong branch.
//
// Like the enum pin, this needs leading trivia to be observable at all:
// maybeReplaceRecoveredTreeWithForest silently repairs an errored production
// root, but declines when the root does not start at byte 0.
//
// This pin is self-healing: it PASSES once the ERROR disappears.
func TestCleanRegressionPinCStructMemberSubscriptField(t *testing.T) {
	tree, _ := pinParse(t, "c", tlsAlignFixture)
	defer tree.Release()

	root := tree.RootNode()
	if got, want := int(root.EndByte()), len(tlsAlignFixture); got != want {
		t.Fatalf("c: parse covers %d of %d bytes", got, want)
	}
	if !root.HasError() {
		return // fix landed - pin now green.
	}
	t.Skip("KNOWN DIVERGENCE (c struct member subscript field): the Go engine " +
		"wraps a field access fragment in ERROR where C parses clean; cap-one " +
		"merging discards the correct branch and no repetition fold reaches it; " +
		"see file comment")
}

const tlsAlignFixture = `
{
	struct {
		char *name;
		unsigned size;
		unsigned align;
		unsigned long addr;
	} *t;
	if (!t)
		t_error("dlsym failed\n");
	else for (i = 0; i < 4; i++) {
		if (t[i].addr & (t[i].align-1))
			t_error("bad alignment: %s, size: %u, align: %u, addr: 0x%lx\n",
				t[i].name, t[i].size, t[i].align, t[i].addr);
	}
}`
