package command

import (
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
		return d.boundChildEffects(d.run.commands[argv[0].Value], commandprogram.OperationRequest{Argv: argv})
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
}
