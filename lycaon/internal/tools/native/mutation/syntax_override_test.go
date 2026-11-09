package mutation

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/tools"
)

func TestMutationSyntaxOverrideIsExplicitAndOperationScoped(t *testing.T) {
	const broken = "package broken\nfunc ("
	h := newHarness(t, nil)
	rejected := h.run(broken, "tee", "broken.go")
	if rejected.err == nil {
		t.Fatal("broken source was accepted without override")
	}
	accepted := h.run(broken, "tee", "--syntax-override-reason", "intentional intermediate source", "broken.go")
	if accepted.err != nil || h.read("broken.go") != broken {
		t.Fatalf("override failed: %v", accepted.err)
	}
	next := h.run(broken, "tee", "other.go")
	if next.err == nil {
		t.Fatal("override leaked to another operation")
	}
	empty := h.run(broken, "tee", "--syntax-override-reason", " ", "empty.go")
	if empty.err == nil {
		t.Fatal("empty override was accepted")
	}

}

func TestSourceEditSyntaxOverrideRecordsItsReason(t *testing.T) {
	const before = "package p\nfunc f() {}\n"
	for _, command := range []string{"edit", "replace-lines"} {
		t.Run(command, func(t *testing.T) {
			h := newHarness(t, map[string]string{"main.go": before})
			h.tc.Effects.Out = &tools.ToolInvocationOut{}
			args := []string{command, "--path", "main.go"}
			if command == "edit" {
				args = append(args, "--old-string", "func f() {}", "--new-string", "func (")
			} else {
				args = append(args, "--start", "2", "--end", "2", "--content", "func (")
			}
			args = append(args, "--syntax-override-reason", "intentional intermediate source")
			result := h.run("", args...)
			if result.err != nil || !strings.Contains(h.read("main.go"), "func (") {
				t.Fatalf("edit override: %v", result.err)
			}
			if !h.tc.Effects.Out.Facts.HasCode(tools.SyntaxCheckOverriddenCode) {
				t.Fatal("override not recorded")
			}
		})
	}
}

func TestSyntaxOverridePreservesTeeByteSemantics(t *testing.T) {
	const body = "package broken\x00"
	h := newHarness(t, nil)
	for _, override := range []bool{false, true} {
		args := []string{"tee"}
		path := "plain.bin"
		if override {
			args = append(args, "--syntax-override-reason", "intentional syntax")
			path = "override.bin"
		}
		result := h.run(body, append(args, path)...)
		if result.err != nil || h.read(path) != body {
			t.Fatalf("byte publication (override=%v): %v", override, result.err)
		}
	}
}
