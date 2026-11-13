package command

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// A dispatcher's preflight reviews what its child will do: two-word children
// resolve like dispatch, nested dispatchers contribute their own child, and a
// child it cannot classify counts as every effect, never as a read.
func TestDispatcherPreflightClassifiesItsChild(t *testing.T) {
	catalog, err := toolcontract.ReadOnlyLanguageV1()
	testutil.FailErr(t, "catalog", err)
	d := &ProgramDispatcher{binder: commandprogram.CatalogBinder{}, run: &programRun{commands: commandIndex(catalog)}}
	effects := func(source string) toolcontract.CommandEffects {
		var argv commandprogram.Fields
		for _, word := range strings.Fields(source) {
			argv = append(argv, commandprogram.Field{Value: word})
		}
		return d.boundChildEffects(t.Context(), d.run.commands[argv[0].Value], commandprogram.OperationRequest{Argv: argv})
	}
	has := func(set toolcontract.CommandEffects, class toolcontract.CommandEffectClass) bool {
		for _, c := range set.Classes() {
			if c == class {
				return true
			}
		}
		return false
	}
	if got := effects("env FOO=1 git push origin main"); !has(got, toolcontract.CommandNetworkEgress) {
		t.Errorf("env git push = %v, want network egress", got.Classes())
	}
	if got := effects("env FOO=1 xargs rm"); !has(got, toolcontract.CommandMutation) {
		t.Errorf("env xargs rm = %v, want mutation", got.Classes())
	}
	if got := effects("xargs grep needle"); has(got, toolcontract.CommandMutation) || has(got, toolcontract.CommandNetworkEgress) {
		t.Errorf("xargs grep = %v, want a read", got.Classes())
	}
	if got := effects("find . -exec sed -i s/a/b/ {} ;"); !has(got, toolcontract.CommandMutation) {
		t.Errorf("find -exec sed -i = %v, want mutation", got.Classes())
	}
	if got := effects("find . -exec no-such-command {} ;"); !has(got, toolcontract.CommandMutation) || !has(got, toolcontract.CommandNetworkEgress) {
		t.Errorf("unclassified child = %v, want every effect", got.Classes())
	}
	if got := effects("env FOO=1 git -C sub push origin main"); !has(got, toolcontract.CommandNetworkEgress) {
		t.Errorf("env git -C push = %v, want network egress", got.Classes())
	}
	if got := effects("env FOO=1 git -C sub frobnicate"); !has(got, toolcontract.CommandMutation) || !has(got, toolcontract.CommandNetworkEgress) {
		t.Errorf("env git with an unknown subcommand = %v, want every effect", got.Classes())
	}
	// Without an owner that can name the mode, every mode counts.
	if got := effects("xargs sort"); !has(got, toolcontract.CommandMutation) {
		t.Errorf("xargs sort without a selector = %v, want its write mode counted", got.Classes())
	}
}

// A child whose owner names its mode from argv contributes that mode alone,
// and a selector that cannot say keeps the union.
func TestDispatcherPreflightUsesTheChildsSelectedMode(t *testing.T) {
	catalog, err := toolcontract.ReadOnlyLanguageV1()
	testutil.FailErr(t, "catalog", err)
	var asked []string
	selector := func(_ context.Context, argv commandprogram.Fields) (string, bool) {
		asked = append(asked, strings.Join(argv.Values(), " "))
		switch {
		case argv[0].Value == "sort" && len(argv) > 1 && argv[1].Value == "-o":
			return "-o", true
		case argv[0].Value == "sort":
			return "always", true
		}
		return "", false
	}
	d := &ProgramDispatcher{binder: commandprogram.CatalogBinder{}, run: &programRun{commands: commandIndex(catalog)},
		natives: map[string]NativeOperation{"sort": {SelectMode: selector}, "yq": {SelectMode: selector}}}
	effects := func(source string) toolcontract.CommandEffects {
		var argv commandprogram.Fields
		for _, word := range strings.Fields(source) {
			argv = append(argv, commandprogram.Field{Value: word})
		}
		return d.boundChildEffects(t.Context(), d.run.commands[argv[0].Value], commandprogram.OperationRequest{Argv: argv})
	}
	mutates := func(set toolcontract.CommandEffects) bool {
		return slices.Contains(set.Classes(), toolcontract.CommandMutation)
	}
	if got := effects("env LC_ALL=C sort file"); mutates(got) {
		t.Errorf("env sort = %v, want a read", got.Classes())
	}
	if got := effects("xargs sort -o out"); !mutates(got) {
		t.Errorf("xargs sort -o = %v, want its write", got.Classes())
	}
	if got := effects("find . -exec yq .a {} +"); !mutates(got) {
		t.Errorf("a selector that cannot say = %v, want the union", got.Classes())
	}
	if !slices.Contains(asked, "sort file") || !slices.Contains(asked, "yq .a {} +") {
		t.Errorf("selectors asked %q", asked)
	}
}
