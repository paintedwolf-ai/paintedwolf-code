package commandprogram

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// addressedDispatch is a fixture directory frame that also owns @label
// conversion: known labels convert, "gone" refuses as stale, others stay
// literal.
type addressedDispatch struct {
	*fakeDispatch
	addresses map[string]string
	asked     *[]string
}

func (d *addressedDispatch) Fork(ctx context.Context) (Dispatch, func(), error) {
	fork, release, err := d.fakeDispatch.Fork(ctx)
	return &addressedDispatch{fakeDispatch: fork.(*fakeDispatch), addresses: d.addresses, asked: d.asked}, release, err
}

func (d *addressedDispatch) Directory() string { return d.cwd }

func (d *addressedDispatch) Address(_ context.Context, label string) (string, bool, error) {
	*d.asked = append(*d.asked, label)
	if label == "gone" {
		return "", false, &codedError{"ROOT_NOT_ATTACHED"}
	}
	native, ok := d.addresses[strings.ToLower(label)]
	return native, ok, nil
}

func newAddressedFixture(t *testing.T) (*evalFixture, *[]string) {
	t.Helper()
	f := newEvalFixture(t)
	asked := &[]string{}
	f.ports.Dispatch = &addressedDispatch{fakeDispatch: &fakeDispatch{log: f.log, cwd: "/abs/api"}, asked: asked, addresses: map[string]string{
		"api": "/abs/api", "web": "/abs/web", "scratch": "/abs/scratch",
	}}
	return f, asked
}

func TestUnquotedAtLabelPrefixesExpandInEveryArgv(t *testing.T) {
	f, _ := newAddressedFixture(t)
	_, err := f.run(t, t.Context(), `echo @api/cmd "@api/cmd" '@web' \@web/x @types/node @API @web/ x=@api @scratch/t.txt @api$HOME @
d=@web/src; echo "$d"
D=@scratch env
external -- tool @web/src "@web/src" @scratch`)
	testutil.FailErr(t, "run address program", err)
	got := f.log.argv()
	want := []string{
		"echo /abs/api/cmd @api/cmd @web @web/x @types/node /abs/api /abs/web/ x=@api /abs/scratch/t.txt @api @",
		"echo /abs/web/src",
	}
	if !slices.Equal(got[:2], want) {
		t.Fatalf("argv =\n%q\nwant\n%q", got[:2], want)
	}
	if env := f.log.find(t, "env").environment; !slices.Contains(env, "D=/abs/scratch") {
		t.Fatalf("prefix assignment environment = %v", env)
	}
	if external := f.log.find(t, "external"); !slices.Equal(external.argv, []string{"external", "--", "tool", "/abs/web/src", "@web/src", "/abs/scratch"}) {
		t.Fatalf("external argv = %q", external.argv)
	}
}

func TestStaleAtLabelRefusesAtTheWordAfterEarlierChildren(t *testing.T) {
	f, asked := newAddressedFixture(t)
	outcome, err := f.run(t, t.Context(), "echo before; cat @gone/file; echo after")
	var refusal *EvaluationRefusal
	if !errors.As(err, &refusal) || refusal.Code != "ROOT_NOT_ATTACHED" || refusal.Node.Span.Line != 1 {
		t.Fatalf("stale label refusal = %v", err)
	}
	if outcome.Stop == nil || outcome.Stop.Result.StopCode != "ROOT_NOT_ATTACHED" {
		t.Fatalf("outcome stop = %+v", outcome.Stop)
	}
	if got := f.log.argv(); !slices.Equal(got, []string{"echo before"}) || !slices.Contains(*asked, "gone") {
		t.Fatalf("dispatched %q asked %q", got, *asked)
	}
}

func TestPwdVariablesFollowEachFramesDirectory(t *testing.T) {
	f, _ := newAddressedFixture(t)
	_, err := f.run(t, t.Context(), `echo "$PWD" "$OLDPWD"
cd web
echo "$PWD" "$OLDPWD"
(cd sub; echo "$PWD")
cd other | echo "$PWD"
echo "$PWD"
external -- fail 0
PWD=/typed; echo "$PWD"
cd web; echo "$PWD" "$OLDPWD"`)
	testutil.FailErr(t, "run directory program", err)
	var echoes []string
	for _, line := range f.log.argv() {
		if strings.HasPrefix(line, "echo") {
			echoes = append(echoes, line)
		}
	}
	want := []string{"echo /abs/api ", "echo /web /abs/api", "echo /sub", "echo /web", "echo /web", "echo /typed", "echo /web /web"}
	if !slices.Equal(echoes, want) {
		t.Fatalf("echoes = %q want %q", echoes, want)
	}
	env := f.log.find(t, "external").environment
	if !slices.Contains(env, "PWD=/web") || !slices.Contains(env, "OLDPWD=/abs/api") {
		t.Fatalf("external environment = %v", env)
	}
}
