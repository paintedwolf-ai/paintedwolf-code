package citationmd

import "testing"

// The first group is text the live runs put in front of users as "(,)", "(, receipt)",
// "(, landed via)" and "( and [path])".
func TestRemovedNodesLeaveNoSeparatorsOrEmptyBrackets(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, source, want string }{
		{"live empty group", "Both overlays preview clean, no conflicts ([overlay_receipt#1], [overlay_receipt#2]). Promoting both in the planned order.", "Both overlays preview clean, no conflicts. Promoting both in the planned order."},
		{"live leading separator", "Build and both package tests pass on the integrated tree ([verify: project - go build ./...], receipt [command#1]). Promoting both overlays now.", "Build and both package tests pass on the integrated tree (receipt). Promoting both overlays now."},
		{"live heading parenthetical", "**Change 1** ([task#1], landed via [overlay_receipt#3])\n- next", "**Change 1** (landed via)\n- next"},
		{"live conjunction before a kept citation", "all pass ([command#1] and [internal/limit/limit.go:14]). The rest.", "all pass ([internal/limit/limit.go:14](internal/limit/limit.go#L14)). The rest."},
		{"conjunction between removed nodes", "all pass ([command#1] and [command#2]).", "all pass."},
		{"semicolon group", "done ([task#1]; [task#2]) now", "done now"},
		{"trailing separator before the closer", "see (first, [task#1]) now", "see (first) now"},
		{"separator on both sides", "first, [task#1], second", "first, second"},
		{"separator before sentence end", "first, [task#1].", "first."},
		{"conjunction with nothing to join", "tests pass and [task#1].", "tests pass."},
		{"list item starting with a removed node", "- [task#1], then land it", "- then land it"},
		{"square brackets stay apart", "see [[task#1]] now", "see [ ] now"},
		{"an open link destination stays", "[l](https://example.test/a, [task#1])", "[l](https://example.test/a,)"},
		{"a paren after a label stays", "[l]([task#1], https://example.test/a)", "[l](, https://example.test/a)"},
		{"word left of a separator keeps it", "first [task#1], second", "first, second"},
		{"opener keeps a following word", "see ([task#1] above)", "see (above)"},
		{"a conjunction with words on both sides stays", "this and [task#1] that", "this and that"},
		{"separators elsewhere stay", "(a, b) and [task#1] (c, d).", "(a, b) and (c, d)."},
		{"empty brackets in the source stay when no node touches them", "f() and g(,) [task#1]", "f() and g(,)"},
		{"code beside a removed node stays", "`(,` [task#1] `)`", "`(,` `)`"},
		{"an escaped opener stays", `a \( [task#1])`, `a \()`},
		{"a line boundary stops the trim", "(see\n[task#1])", "(see\n)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := Project(Parse(tc.source, Options{})); got != tc.want {
				t.Fatalf("Project(%q)\n got %q\nwant %q", tc.source, got, tc.want)
			}
		})
	}
}

// A scheme the frontend cannot follow leaves its label. Live: `output:program:...`.
func TestLinksNowherePresentTheirLabel(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ source, want string }{
		{"Listed by [the output](output:program:3ae4:stdout) above.", "Listed by the output above."},
		{"Run [this](javascript:alert(1)).", "Run this."},
		{"Mail [me](mailto:a@example.test).", "Mail [me](mailto:a@example.test)."},
		{"Call [us](tel:+15550100).", "Call [us](tel:+15550100)."},
		{"Jump to [the end](#end).", "Jump to [the end](#end)."},
		{"See [it](file:///tmp/a.go).", "See [it](file:///tmp/a.go)."},
	} {
		if got := Project(Parse(tc.source, Options{})); got != tc.want {
			t.Errorf("Project(%q) = %q, want %q", tc.source, got, tc.want)
		}
	}
}
