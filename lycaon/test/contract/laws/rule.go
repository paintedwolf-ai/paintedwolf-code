// Package laws holds the repository's source laws as rule rows evaluated over
// one fail-closed corpus per test binary. A rule fails when it finds a
// violation, when an anchor it names is missing, when an exemption it declares
// matches nothing, and when any selection it makes is empty.
package laws

import (
	"errors"
	"fmt"
	"go/token"
	"sort"
	"strings"
)

// Family is a named table of rules, reported as family/rule.
type Family struct {
	Name  string
	Rules []Rule
}

// Rule is one law. Check reports through the Run; it never touches testing.T.
type Rule struct {
	Name string
	// Anchors must resolve in the corpus, so a rename cannot silence the rule.
	Anchors []Anchor
	// Exempt entries are consumed by Run.Exempt; one never consumed fails.
	Exempt []Exemption
	Check  func(*Run)
}

// Exemption permits one key the rule would otherwise report. Reason says
// why, in the table, for review.
type Exemption struct {
	Key    string
	Reason string
}

// Result is what one evaluation found.
type Result struct {
	Violations      []string
	MissingAnchors  []string
	StaleExemptions []string
	// Err is a load failure or an empty selection; it ends the check.
	Err error
}

// Failed reports whether the rule failed for any reason.
func (r Result) Failed() bool {
	return r.Err != nil || len(r.Violations)+len(r.MissingAnchors)+len(r.StaleExemptions) > 0
}

// String renders the result for a test failure.
func (r Result) String() string {
	var b strings.Builder
	if r.Err != nil {
		fmt.Fprintf(&b, "error: %v\n", r.Err)
	}
	section := func(title string, items []string) {
		if len(items) == 0 {
			return
		}
		fmt.Fprintf(&b, "%s (%d):\n", title, len(items))
		for _, item := range items {
			b.WriteString("  - ")
			b.WriteString(strings.ReplaceAll(item, "\n", "\n    "))
			b.WriteByte('\n')
		}
	}
	section("violations", r.Violations)
	section("anchors not found", r.MissingAnchors)
	section("exemptions that match nothing", r.StaleExemptions)
	return b.String()
}

// Run is the state of one rule evaluation.
type Run struct {
	*Corpus
	rule       *Rule
	exempt     map[string]bool
	violations []string
}

type abort struct{ err error }

// Evaluate checks one rule against c.
func Evaluate(c *Corpus, rule Rule) (res Result) {
	if rule.Check == nil {
		return Result{Err: fmt.Errorf("rule %s has no check", rule.Name)}
	}
	if len(rule.Anchors) == 0 {
		return Result{Err: fmt.Errorf("rule %s names no anchor", rule.Name)}
	}
	run := &Run{Corpus: c, rule: &rule, exempt: map[string]bool{}}
	for _, ex := range rule.Exempt {
		if ex.Key == "" || strings.TrimSpace(ex.Reason) == "" {
			return Result{Err: fmt.Errorf("exemption %q needs a key and a reason", ex.Key)}
		}
		if _, dup := run.exempt[ex.Key]; dup {
			return Result{Err: fmt.Errorf("exemption %q is listed twice", ex.Key)}
		}
		run.exempt[ex.Key] = false
	}
	for _, anchor := range rule.Anchors {
		if err := anchor.resolve(c); err != nil {
			var failure abort
			if errors.As(err, &failure) {
				return Result{Err: failure.err}
			}
			res.MissingAnchors = append(res.MissingAnchors, fmt.Sprintf("%s: %v", anchor, err))
		}
	}
	if len(res.MissingAnchors) > 0 {
		return res
	}
	defer func() {
		if p := recover(); p != nil {
			failure, ok := p.(abort)
			if !ok {
				panic(p)
			}
			res.Err = failure.err
		}
	}()
	rule.Check(run)
	res.Violations = run.violations
	sort.Strings(res.Violations)
	for key, used := range run.exempt {
		if !used {
			res.StaleExemptions = append(res.StaleExemptions, key)
		}
	}
	sort.Strings(res.StaleExemptions)
	return res
}

func (a abort) Error() string { return a.err.Error() }

// Fatal ends the check with err.
func (r *Run) Fatal(err error) {
	panic(abort{err})
}

// Fatalf ends the check with a formatted error.
func (r *Run) Fatalf(format string, args ...any) {
	r.Fatal(fmt.Errorf(format, args...))
}

// Must ends the check when err is not nil.
func (r *Run) Must(err error) {
	if err != nil {
		r.Fatal(err)
	}
}

// Report records a violation.
func (r *Run) Report(format string, args ...any) {
	r.violations = append(r.violations, fmt.Sprintf(format, args...))
}

// ReportAt records a violation at a Go source position.
func (r *Run) ReportAt(pos token.Pos, format string, args ...any) {
	r.Report("%s: %s", r.Go().Position(pos), fmt.Sprintf(format, args...))
}

// Exempt reports whether key is exempt, and marks the exemption as used.
func (r *Run) Exempt(key string) bool {
	if _, ok := r.exempt[key]; !ok {
		return false
	}
	r.exempt[key] = true
	return true
}

// Expect fails the check when a selection is empty.
func (r *Run) Expect(n int, what string) {
	if n == 0 {
		r.Fatalf("empty selection: %s", what)
	}
}

// Diff reports the members missing from got and the members got has
// beyond want, each under its label.
func (r *Run) Diff(label string, want, got []string) {
	wantSet, gotSet := setOf(want), setOf(got)
	for _, item := range sortedKeys(wantSet) {
		if !gotSet[item] {
			r.Report("%s: missing %s", label, item)
		}
	}
	for _, item := range sortedKeys(gotSet) {
		if !wantSet[item] {
			r.Report("%s: unexpected %s", label, item)
		}
	}
}

func setOf(items []string) map[string]bool {
	set := make(map[string]bool, len(items))
	for _, item := range items {
		set[item] = true
	}
	return set
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
