//go:build darwin || linux

package textutil

import (
	"context"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/textsearch"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"testing"
)

type observedSedCompiler struct {
	PosixRegexEngine
	seen context.Context
}

func (c *observedSedCompiler) Compile(ctx context.Context, pattern string, extended bool) (textsearch.PosixRegex, error) {
	c.seen = ctx
	return c.PosixRegexEngine.Compile(ctx, pattern, extended)
}
func TestSedEffectModeCompilesWithInvocationContext(t *testing.T) {
	h := sedHarness(t)
	compiler := &observedSedCompiler{PosixRegexEngine: h.owners.Regex}
	h.owners.Regex = compiler
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	request := commandprogramRequest("context-mode")
	request.OperationID = "sed"
	request.Argv = commandprogram.Fields{{Value: "sed"}, {Value: "s/x/y/;w out"}}
	request.Invocation.Kind = toolcontract.ProgramChildInvocation
	request.Statement.Span = request.Invocation.Span
	mode := (dispatcher{owners: h.owners}).EffectMode(ctx, request)
	if mode != "w-command" || compiler.seen != ctx {
		t.Fatalf("mode=%q compiler context=%v, want actual invocation context", mode, compiler.seen)
	}
	cancel()
	if compiler.seen.Err() != context.Canceled {
		t.Fatal("compiler lost invocation cancellation")
	}
}
