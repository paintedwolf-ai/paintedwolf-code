package forbiddencalls

import (
	"fmt"
	"go/ast"
	"slices"

	"github.com/lycaon/lycaon/test/contract/invariants"
	"gopkg.in/yaml.v3"
)

const signalLintConfig = "lycaon/.golangci.yml"

var processSignals = []invariants.Invariant{{
	Name: "raw-process-signals-stay-in-platform-adapters",
	Anchors: []invariants.Anchor{
		invariants.Package("internal/osprocess"),
		invariants.File(signalLintConfig),
	},
	Exempt: rawSignalAdapters,
	Check: func(r *invariants.Run) {
		checkSignalLintConfig(r)
		for _, f := range r.GoFiles(invariants.Production, "internal", "cmd") {
			f.Calls(f.AST, func(call *ast.CallExpr, pkg, name string) {
				switch {
				case pkg == "syscall" && name == "Kill",
					pkg == "golang.org/x/sys/unix" && (name == "Kill" || name == "Killpg" || name == "PidfdSendSignal"),
					pkg == "os" && name == "FindProcess":
					if !rawSignalAdapter(r, f) {
						r.ReportAt(call.Pos(), "raw %s.%s outside a process adapter; use osprocess", pkg, name)
					}
				}
			})
		}
	},
}}

var rawSignalAdapters = []invariants.Exemption{
	{Key: "internal/osprocess", Reason: "owns typed process identity and signaling"},
	{Key: "internal/hostprocess", Reason: "owns platform process handles"},
	{Key: "internal/launchguard", Reason: "owns raw launch guardian process handles"},
	{Key: "internal/confine/linuxsandbox", Reason: "owns namespace relay and target handles"},
	{Key: "internal/exec/supervisor_linux.go", Reason: "process supervisor mirrors the child's exit signal"},
	{Key: "internal/exec/supervisor_tree_linux.go", Reason: "process supervisor signals identified descendants"},
	{Key: "internal/exec/relay_linux.go", Reason: "process supervisor relays signals to its child"},
}

func rawSignalAdapter(r *invariants.Run, f *invariants.GoFile) bool {
	for _, adapter := range rawSignalAdapters {
		if invariants.Under(f, adapter.Key) {
			return r.Exempt(adapter.Key)
		}
	}
	return false
}

func checkSignalLintConfig(r *invariants.Run) {
	var config struct {
		Linters struct {
			Enable   []string
			Settings struct {
				Forbidigo struct {
					Forbid []struct{ Pattern string }
				}
			}
		}
	}
	if err := yaml.Unmarshal(r.Bytes(signalLintConfig), &config); err != nil {
		r.Fatal(fmt.Errorf("decode %s: %w", signalLintConfig, err))
	}
	if !slices.Contains(config.Linters.Enable, "forbidigo") {
		r.Report("%s must enable forbidigo", signalLintConfig)
	}
	for _, pattern := range []string{
		`^syscall\.Kill$`,
		`^(golang\.org/x/sys/)?unix\.(Kill|Killpg|PidfdSendSignal)$`,
		`^os\.FindProcess$`,
	} {
		if !slices.ContainsFunc(config.Linters.Settings.Forbidigo.Forbid, func(rule struct{ Pattern string }) bool {
			return rule.Pattern == pattern
		}) {
			r.Report("%s is missing forbidigo pattern %q", signalLintConfig, pattern)
		}
	}
}
