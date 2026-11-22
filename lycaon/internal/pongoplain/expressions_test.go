package pongoplain_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/pongoplain"
)

func TestParseTemplateTreeAndExpressions(t *testing.T) {
	t.Parallel()
	tree, err := pongoplain.ParseTemplateTree(
		`{% set note = "done" %}` +
			`{% if can_git_read %}git{% elif profile_has_verify or can_command_verify %}verify{% else %}{{ "load" if can_request_git_read else note }}{% endif %}` +
			`{% for item in items|default:"" %}{{ item }}{% empty %}none{% endfor %}` +
			`{% include "partials/host.md" %}`,
	)
	if err != nil {
		t.Fatalf("ParseTemplateTree failed on valid template: %v", err)
	}
	if len(tree.Nodes) != 4 {
		t.Fatalf("len(tree.Nodes) = %d, want 4", len(tree.Nodes))
	}

	for name, bad := range map[string]string{
		"unknown executable tag": `{% custom_tag foo %}bar{% endcustom_tag %}`,
		"unclosed if":            `{% if can_git_read %}git`,
		"unexpected elif":        `{% elif can_git_read %}git{% endif %}`,
		"malformed expression":   `{% if can_git_read and %}git{% endif %}`,
		"unexpected character":   `{{ foo @ bar }}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := pongoplain.ParseTemplateTree(bad); err == nil {
				t.Fatalf("ParseTemplateTree(%q) succeeded, want fail-closed error", bad)
			}
		})
	}

	expr, err := pongoplain.ParseExpression(`"yes" if has_git and not on_worker_branch else "no"`)
	if err != nil {
		t.Fatalf("ParseExpression ternary: %v", err)
	}
	if expr.Kind != pongoplain.ExprTernary || !expr.HasLogicalOrCompare() {
		t.Fatalf("unexpected ternary AST: %+v", expr)
	}
	if got := strings.Join(expr.Identifiers(), ","); got != "has_git,on_worker_branch" {
		t.Fatalf("Identifiers() = %q, want has_git,on_worker_branch", got)
	}
}
