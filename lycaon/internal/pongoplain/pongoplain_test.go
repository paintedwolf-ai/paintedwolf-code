package pongoplain

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

type contextWithMethod struct{ Value string }

func (contextWithMethod) Secret() string { return "method-ran" }

func fileReachingTemplates(target string) map[string]string {
	return map[string]string{
		"ssi":     `{% ssi "` + target + `" %}`,
		"include": `{% include "` + target + `" %}`,
		"extends": `{% extends "` + target + `" %}`,
		"import":  `{% import "` + target + `" as m %}`,
	}
}

func TestCompileRefusesNondeterministicAndExpandingOperations(t *testing.T) {
	for name, source := range map[string]string{
		"body buffer":      `{% filter upper %}x{% endfilter %}`,
		"collection split": `{{ value|split:"," }}`,
		"mutable cycle":    `{% cycle "a" "b" %}`,
		"wall clock":       `{% now "2006" %}`,
		"random":           `{{ values|random }}`,
		"wide center":      `{{ value|center:1000000000 }}`,
		"macro":            `{% macro recurse %}x{% endmacro %}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Compile(source); err == nil {
				t.Fatalf("compiled forbidden source %q", source)
			}
		})
	}
}

func TestCompileAndExecuteResourceCeilings(t *testing.T) {
	t.Run("source", func(t *testing.T) {
		_, err := Compile(strings.Repeat("x", MaxSourceBytes+1))
		if !errors.Is(err, ErrSourceLimit) {
			t.Fatalf("source error = %v", err)
		}
	})

	t.Run("context collection", func(t *testing.T) {
		tpl, err := Compile(`{{ values|length }}`)
		testutil.FailErr(t, "compile", err)
		_, err = Execute(t.Context(), tpl, map[string]any{"values": make([]string, MaxCollectionItems+1)})
		if !errors.Is(err, ErrContextLimit) {
			t.Fatalf("context error = %v", err)
		}
	})

	t.Run("callable context", func(t *testing.T) {
		tpl, err := Compile(`{{ value }}`)
		testutil.FailErr(t, "compile", err)
		_, err = Execute(t.Context(), tpl, map[string]any{"value": func() string { return "called" }})
		if !errors.Is(err, ErrContextLimit) {
			t.Fatalf("callable error = %v", err)
		}
	})

	t.Run("output", func(t *testing.T) {
		tpl, err := Compile(`{% for a in values %}{% for b in values %}{{ block }}{% endfor %}{% endfor %}`)
		testutil.FailErr(t, "compile", err)
		values := make([]int, MaxCollectionItems)
		_, err = Execute(t.Context(), tpl, map[string]any{"values": values, "block": strings.Repeat("x", 32)})
		if !errors.Is(err, ErrOutputLimit) {
			t.Fatalf("output error = %v", err)
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		tpl, err := Compile(`{{ value }}`)
		testutil.FailErr(t, "compile", err)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err = Execute(ctx, tpl, map[string]any{"value": "x"})
		if !errors.Is(err, ErrExecutionLimit) || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error = %v", err)
		}
	})

	t.Run("reserved context key", func(t *testing.T) {
		tpl, err := Compile(`{{ value }}`)
		testutil.FailErr(t, "compile", err)
		_, err = Execute(t.Context(), tpl, map[string]any{"0": "injected", "value": "x"})
		if !errors.Is(err, ErrContextLimit) {
			t.Fatalf("reserved key error = %v", err)
		}
	})
}

func TestExecuteDeterministicMidRenderCancellationAndWorkBounds(t *testing.T) {
	t.Run("mid-render cancellation stops zero-output loop immediately", func(t *testing.T) {
		tpl, err := Compile(`{% for a in xs %}{% set y = a %}{% endfor %}`)
		testutil.FailErr(t, "compile", err)

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		var stepsObserved int
		_, err = ExecuteWithStepHook(ctx, tpl, map[string]any{"xs": make([]int, MaxCollectionItems)}, func(step int) {
			stepsObserved = step
			if step == 7 {
				cancel()
			}
		})
		if !errors.Is(err, ErrExecutionLimit) || !errors.Is(err, context.Canceled) {
			t.Fatalf("mid-render cancellation error = %v", err)
		}
		if stepsObserved != 7 {
			t.Fatalf("stepsObserved = %d, want immediate stop at step 7", stepsObserved)
		}
	})

	t.Run("zero-output work exhaustion hits MaxExecutionSteps", func(t *testing.T) {
		tpl, err := Compile(`{% for a in xs %}{% for b in xs %}{% set y = a %}{% if y == b %}{% endif %}{% endfor %}{% endfor %}`)
		testutil.FailErr(t, "compile", err)

		out, err := Execute(t.Context(), tpl, map[string]any{"xs": make([]int, MaxCollectionItems)})
		if !errors.Is(err, ErrExecutionLimit) {
			t.Fatalf("zero-output exhaustion out=%q err=%v", out, err)
		}
		if out != "" {
			t.Fatalf("unexpected output on abort: %q", out)
		}
	})
}

func TestComposedIncludeSharesDeterministicBudgets(t *testing.T) {
	t.Run("cross-include nested loops enforce MaxForNesting", func(t *testing.T) {
		set, err := NewSet("nested-for-include", memTemplateLoader{
			"a.md": `{% for a in xs %}{% for b in xs %}{% include "b.md" only %}{% endfor %}{% endfor %}`,
			"b.md": `{% for c in ys %}{% endfor %}`,
		}, Composed)
		testutil.FailErr(t, "NewSet", err)
		tpl, err := set.FromFile("a.md")
		testutil.FailErr(t, "FromFile", err)

		_, err = Execute(t.Context(), tpl, map[string]any{"xs": []int{1}, "ys": []int{1}})
		if !errors.Is(err, ErrExecutionLimit) {
			t.Fatalf("cross-include loop nesting error = %v", err)
		}
	})

	t.Run("cross-include with only shares cumulative step budget", func(t *testing.T) {
		set, err := NewSet("only-step-budget", memTemplateLoader{
			"a.md": `{% for a in xs %}{% include "b.md" with xs=xs only %}{% endfor %}`,
			"b.md": `{% for b in xs %}{% set y = b %}{% if y == 0 %}{% endif %}{% endfor %}`,
		}, Composed)
		testutil.FailErr(t, "NewSet", err)
		tpl, err := set.FromFile("a.md")
		testutil.FailErr(t, "FromFile", err)

		_, err = Execute(t.Context(), tpl, map[string]any{"xs": make([]int, MaxCollectionItems)})
		if !errors.Is(err, ErrExecutionLimit) {
			t.Fatalf("cross-include only step exhaustion error = %v", err)
		}
	})

	t.Run("cross-include unbuffered output enforces MaxOutputBytes", func(t *testing.T) {
		set, err := NewSet("include-output-ceiling", memTemplateLoader{
			"a.md": `{% for a in xs %}{% include "b.md" with xs=xs block=block only %}{% endfor %}`,
			"b.md": `{% for b in xs %}{{ block }}{% endfor %}`,
		}, Composed)
		testutil.FailErr(t, "NewSet", err)
		tpl, err := set.FromFile("a.md")
		testutil.FailErr(t, "FromFile", err)

		_, err = Execute(t.Context(), tpl, map[string]any{
			"xs":    make([]int, MaxCollectionItems),
			"block": strings.Repeat("x", 32),
		})
		if !errors.Is(err, ErrOutputLimit) {
			t.Fatalf("cross-include output ceiling error = %v", err)
		}
	})

	t.Run("dynamic include expression refused with ErrComposition", func(t *testing.T) {
		if _, err := Inspect(`{% include target %}`, Composed); !errors.Is(err, ErrComposition) {
			t.Fatalf("Inspect dynamic include error = %v", err)
		}
		set, err := NewSet("dynamic-include", memTemplateLoader{"a.md": `{% include target %}`}, Composed)
		testutil.FailErr(t, "NewSet", err)
		_, err = set.FromFile("a.md")
		if err == nil || !strings.Contains(err.Error(), ErrComposition.Error()) {
			t.Fatalf("FromFile dynamic include error = %v", err)
		}
	})

	t.Run("include runtime depth and file count ceilings", func(t *testing.T) {
		files := memTemplateLoader{}
		for i := range MaxCompositionDepth + 2 {
			name := filepath.ToSlash(strings.Join([]string{"d", string(rune('a' + i))}, "_") + ".md")
			next := filepath.ToSlash(strings.Join([]string{"d", string(rune('a' + i + 1))}, "_") + ".md")
			if i == MaxCompositionDepth+1 {
				files[name] = "leaf"
			} else {
				files[name] = `{% include "` + next + `" %}`
			}
		}
		depthSet, err := NewSet("depth-ceiling", files, Composed)
		testutil.FailErr(t, "NewSet depth", err)
		depthTpl, err := depthSet.FromFile("d_a.md")
		testutil.FailErr(t, "FromFile depth", err)
		if _, err := Execute(t.Context(), depthTpl, nil); !errors.Is(err, ErrComposition) {
			t.Fatalf("runtime include depth error = %v", err)
		}

		countSet, err := NewSet("count-ceiling", memTemplateLoader{
			"a.md": `{% include "b.md" %}{% for x in xs %}{% include "b.md" if_exists %}{% endfor %}{% include "missing.md" if_exists %}`,
			"b.md": `ok`,
		}, Composed)
		testutil.FailErr(t, "NewSet count", err)
		countTpl, err := countSet.FromFile("a.md")
		testutil.FailErr(t, "FromFile count", err)
		if _, err := Execute(t.Context(), countTpl, map[string]any{"xs": make([]int, MaxCompositionFiles)}); !errors.Is(err, ErrComposition) {
			t.Fatalf("runtime include count error = %v", err)
		}
	})
}

func TestCompileRefusesExcessiveLoopNesting(t *testing.T) {
	_, err := Compile(`{% for a in xs %}{% for b in xs %}{% for c in xs %}x{% endfor %}{% endfor %}{% endfor %}`)
	if !errors.Is(err, ErrExecutionLimit) {
		t.Fatalf("loop nesting error = %v", err)
	}
}

func TestCompileRefusesFileReachingTags(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "credentials")
	const secret = "SENSITIVE-VALUE-42"
	testutil.FailErr(t, "write target", os.WriteFile(target, []byte(secret), 0o600))

	for tag, body := range fileReachingTemplates(target) {
		t.Run(tag, func(t *testing.T) {
			tpl, err := Compile(body)
			if err != nil {
				if !strings.Contains(err.Error(), "not allowed") || !strings.Contains(err.Error(), tag) {
					t.Fatalf("%s: refused, but not as a banned tag: %v", tag, err)
				}
				return
			}
			out, execErr := Execute(t.Context(), tpl, nil)
			if execErr == nil && strings.Contains(out, secret) {
				t.Fatalf("%s: template read %s and rendered its contents", tag, target)
			}
			t.Fatalf("%s: parsed instead of being refused (out=%q err=%v)", tag, out, execErr)
		})
	}
}

func TestCompileRefusesFileReachingTagsInContext(t *testing.T) {
	for name, body := range map[string]string{
		"mid body":  "Fix this file.\n{% ssi \"/etc/hosts\" %}\nThanks.",
		"traversal": `{% include "../../../../etc/hosts" %}`,
		"parsed":    `{% ssi "/etc/hosts" parsed %}`,
		"nested":    `{% if 1 %}{% ssi "/etc/hosts" %}{% endif %}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Compile(body); err == nil {
				t.Fatalf("%s: parsed a file-reaching tag", name)
			}
		})
	}
}

func TestCompileRendersOrdinaryTemplates(t *testing.T) {
	tpl, err := Compile(`{% if path %}Fix {{ path }}{% for n in names %} {{ n }}{% endfor %}{% endif %}`)
	testutil.FailErr(t, "parse", err)
	out, err := Execute(t.Context(), tpl, map[string]any{"path": "src/a.go", "names": []string{"x", "y"}})
	testutil.FailErr(t, "execute", err)
	if out != "Fix src/a.go x y" {
		t.Fatalf("unexpected render: %q", out)
	}
}

func TestExecutePreservesNestedDataShapeWithoutMethods(t *testing.T) {
	tpl, err := Compile(`{{ digest }}:{% for row in rows %}{{ row.id }}={{ row.items|join:"," }}{% endfor %}`)
	testutil.FailErr(t, "compile", err)
	out, err := Execute(t.Context(), tpl, map[string]any{
		"digest": "ready",
		"rows":   []map[string]any{{"id": "gate", "items": []string{"a", "b"}}},
	})
	testutil.FailErr(t, "execute", err)
	if out != "ready:gate=a,b" {
		t.Fatalf("nested render = %q", out)
	}
}

func TestExecuteStripsStructMethods(t *testing.T) {
	tpl, err := Compile(`{{ item.Value }}|{{ item.Secret }}`)
	testutil.FailErr(t, "compile", err)
	out, err := Execute(t.Context(), tpl, map[string]any{"item": contextWithMethod{Value: "data"}})
	testutil.FailErr(t, "execute", err)
	if out != "data|" {
		t.Fatalf("method was exposed: %q", out)
	}
}

// Prompt output preserves command and code text.
func TestRenderingStaysPlaintext(t *testing.T) {
	tpl, err := Compile(`{{ cmd }}`)
	testutil.FailErr(t, "parse", err)
	out, err := Execute(t.Context(), tpl, map[string]any{"cmd": `go test -run 'A&B' ./... "x"`})
	testutil.FailErr(t, "execute", err)
	if strings.Contains(out, "&amp;") || strings.Contains(out, "&quot;") {
		t.Fatalf("output was HTML-escaped: %q", out)
	}
}

func TestStringSetLoaderResolvesNothing(t *testing.T) {
	var loader denyLoader
	if _, err := loader.Get("/etc/hosts"); err == nil {
		t.Fatal("deny loader returned a reader")
	}
}

func TestNewSetProfiles(t *testing.T) {
	t.Run("composed permits only bounded composition", func(t *testing.T) {
		set, err := NewSet("test-composed", denyLoader{}, Composed)
		testutil.FailErr(t, "new set", err)
		if _, err := set.FromString(`{% include "x" %}`); err == nil {
			t.Fatal("include resolved through a deny loader")
		} else if strings.Contains(err.Error(), "not allowed") {
			t.Fatalf("Composed banned include: %v", err)
		}
		for name, source := range map[string]string{
			"block":       `{% block body %}x{% endblock %}`,
			"extends":     `{% extends "x" %}`,
			"filter":      `{% filter upper %}x{% endfilter %}`,
			"ifchanged":   `{% ifchanged %}x{% endifchanged %}`,
			"spaceless":   `{% spaceless %}<b> x </b>{% endspaceless %}`,
			"file access": `{% ssi "/etc/hosts" %}`,
		} {
			if _, err := set.FromString(source); err == nil {
				t.Errorf("Composed permitted %s", name)
			}
		}
	})

	t.Run("unknown profile is refused", func(t *testing.T) {
		if _, err := NewSet("test-unknown", denyLoader{}, Profile(0)); err == nil {
			t.Fatal("unknown profile built a set")
		}
	})

	t.Run("a set requires a loader", func(t *testing.T) {
		if _, err := NewSet("test-nil", nil, Isolated); err == nil {
			t.Fatal("nil loader built a set")
		}
	})
}

func TestVariableReferencesWalker(t *testing.T) {
	cases := []struct {
		name   string
		source string
		want   []string
	}{
		{
			name:   "plain references",
			source: `{% if profile_command_mutation and not command_on_floor %}{{ root_count|default:fallback_count }}{% endif %}`,
			want:   []string{"profile_command_mutation", "command_on_floor", "root_count", "fallback_count"},
		},
		{
			name:   "dotted references",
			source: `{% if mints.tui or profile_can_request.verify %}{{ receipt_kind.command }}{% endif %}`,
			want:   []string{"mints.tui", "profile_can_request.verify", "receipt_kind.command"},
		},
		{
			name: "loop and with bindings excluded",
			source: `{% for k, row in agent_tools %}{{ k }} {{ row.name }} {{ forloop.Counter }}{% endfor %}` +
				`{% with alias=profile_has_command other = surface_id %}{{ alias }} {{ other.len }} {{ has_git }}{% endwith %}` +
				`{% with requestable_tools as reqs %}{{ reqs.name }}{% endwith %}`,
			want: []string{"agent_tools", "profile_has_command", "surface_id", "has_git", "requestable_tools"},
		},
		{
			name: "references inside literals and comments ignored",
			source: `prose profile_has_fake {# {{ profile_in_comment }} #} ` +
				`{% comment %}{{ profile_in_block_comment }}{% endcomment %} ` +
				`{% verbatim %}{{ profile_in_verbatim }}{% endverbatim %} ` +
				`{{ "profile_in_double_quotes" }} '{{ 'profile_in_single_quotes' }}' ` +
				`{% if mode == "profile_literal" %}{{ real_var }}{% endif %}`,
			want: []string{"mode", "real_var"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := VariableReferences(tc.source)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("VariableReferences() = %v, want %v", got, tc.want)
			}
		})
	}
}
