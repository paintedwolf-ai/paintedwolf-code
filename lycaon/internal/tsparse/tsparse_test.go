package tsparse

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

func pathologicalBash(lines int) []byte {
	return []byte("  log) printf '%s' x ;;\n" + strings.Repeat("  local va=\"${arr[@]:-}\"\n  if [[ -n \"${v}\" ]]; then printf '%s' \"${v}\"; fi\n", lines))
}

func TestParseOrdinarySource(t *testing.T) {
	for language, source := range map[string]string{
		"bash":  "echo hello\nls -la\n",
		"swift": "class Engine {\n    func doIt(a: Int) { log(a) }\n}\nfunc run(a: Int) { log(a) }\n",
	} {
		t.Run(language, func(t *testing.T) {
			entry := grammars.DetectLanguageByName(language)
			tree, err := Parse(context.Background(), entry.Language(), []byte(source), Analysis)
			testutil.FailErr(t, "parse ordinary source", err)
			defer tree.Release()
			if tree.ParseStoppedEarly() || tree.RootNode().HasError() {
				t.Fatalf("ordinary source: stop=%s errors=%v", tree.ParseStopReason(), tree.RootNode().HasError())
			}
		})
	}
}

func TestParseTimeoutRetainsDiagnosticsAndReleasesPartialTree(t *testing.T) {
	lang := grammars.DetectLanguageByName("bash").Language()
	src := pathologicalBash(160)
	tree, err := ParseWithin(context.Background(), lang, src, time.Microsecond)
	var failure *Failure
	if tree != nil || !errors.As(err, &failure) || failure.Reason != "timeout" {
		t.Fatalf("bounded parse: tree_present=%t error=%v", tree != nil, err)
	}
	if !failure.Incomplete() || failure.Language != "bash" || failure.SourceBytes != len(src) || failure.ParsedBytes > len(src) {
		t.Fatalf("missing parser diagnostics: %+v", failure)
	}
}

func TestParseCancellationAndDeadline(t *testing.T) {
	lang := grammars.DetectLanguageByName("bash").Language()
	for _, deadline := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		want := "canceled"
		if deadline {
			cancel()
			ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			want = "deadline"
		} else {
			cancel()
		}
		tree, err := ParseWithin(ctx, lang, pathologicalBash(160), time.Second)
		cancel()
		var failure *Failure
		if tree != nil || !errors.As(err, &failure) || failure.Reason != want || !errors.Is(err, ctx.Err()) {
			t.Fatalf("canceled parse: tree_present=%t error=%v", tree != nil, err)
		}
	}
}

func TestParseStopsWhenActiveRequestIsCanceled(t *testing.T) {
	lang := grammars.DetectLanguageByName("bash").Language()
	src := pathologicalBash(16000)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	timer := time.AfterFunc(10*time.Millisecond, cancel)
	defer timer.Stop()
	tree, err := ParseWithin(ctx, lang, src, 30*time.Second)
	var failure *Failure
	if tree != nil || !errors.As(err, &failure) || failure.Reason != "canceled" {
		t.Fatalf("active cancellation: tree_present=%t error=%v", tree != nil, err)
	}
}

func TestParseStructuralExhaustionIsNotComplete(t *testing.T) {
	t.Setenv("GOT_PARSE_MEMORY_BUDGET_MB", "1")
	gotreesitter.ResetParseEnvConfigCacheForTests()
	defer gotreesitter.ResetParseEnvConfigCacheForTests()
	lang := grammars.DetectLanguageByName("go").Language()
	src := []byte("package p\nfunc f() {\n" + strings.Repeat("var x = 1\n", 20000) + "}\n")
	tree, err := ParseWithin(context.Background(), lang, src, 30*time.Second)
	if tree != nil {
		defer tree.Release()
	}
	var failure *Failure
	if tree != nil || !errors.As(err, &failure) || failure.Reason != "memory_budget" {
		t.Fatalf("resource exhaustion: tree_present=%t error=%v", tree != nil, err)
	}
	if !failure.Incomplete() || failure.SourceBytes != len(src) || failure.Language != "go" || failure.ResourceLimit != 1<<20 || failure.ResourceUnit != "bytes" {
		t.Fatalf("resource exhaustion lost its diagnostics: %+v", failure)
	}
}

func TestParseRejectsUnboundedTimeout(t *testing.T) {
	lang := grammars.DetectLanguageByName("go").Language()
	for _, timeout := range []time.Duration{0, -time.Second} {
		tree, err := ParseWithin(context.Background(), lang, []byte("package p\n"), timeout)
		var failure *Failure
		if tree != nil || !errors.As(err, &failure) || failure.Reason != "configuration" {
			t.Fatalf("invalid timeout: tree_present=%t error=%v", tree != nil, err)
		}
	}
}

func TestParseRetainsCancellationIdentityAndCause(t *testing.T) {
	lang := grammars.DetectLanguageByName("go").Language()
	cause := errors.New("worker execution stopped")
	canceled, cancel := context.WithCancelCause(context.Background())
	cancel(cause)
	expired, expire := context.WithDeadlineCause(context.Background(), time.Now().Add(-time.Second), cause)
	defer expire()
	for _, ctx := range []context.Context{canceled, expired} {
		tree, err := ParseWithin(ctx, lang, []byte("package p\n"), time.Second)
		if tree != nil || !errors.Is(err, ctx.Err()) || !errors.Is(err, cause) {
			t.Fatalf("cancellation lost its identity or cause: tree_present=%t error=%v", tree != nil, err)
		}
	}
}
