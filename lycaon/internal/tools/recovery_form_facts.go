package tools

import (
	"strings"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/toolreach"
)

type programPhrase struct {
	program string
	label   string
}

func registerCommandFormRecoveryFacts(gc *oar.GuardContext, request toolreach.Request) {
	cmdCallable := request.Status(toolreach.ProgramTool) == toolreach.Callable
	put := func(bare string, compute func() any) {
		gc.RegisterProvider("paintedwolf."+bare, func(target *oar.GuardContext) error {
			if target.ObservationData == nil {
				target.ObservationData = map[string]any{}
			}
			target.ObservationData[bare] = compute()
			return nil
		})
	}
	admitted := func(items ...programPhrase) string {
		if !cmdCallable {
			return ""
		}
		var out []string
		for _, it := range items {
			if request.Program(it.program) {
				out = append(out, it.label)
			}
		}
		return joinDisjunction(out)
	}

	put("admitted_process_capability_fields", func() any {
		if !request.Known() || !request.Program("external") {
			return ""
		}
		var fields []string
		for _, field := range []string{"capability_request.process_control", "capability_request.host_execution"} {
			if request.AcceptsField(toolreach.ProgramTool, field) {
				fields = append(fields, "`"+field+"`")
			}
		}
		return joinDisjunction(fields)
	})
	put("admitted_source_read_forms", func() any {
		return admitted(programPhrase{"sed", "`sed -n`"}, programPhrase{"cat", "`cat -n`"})
	})
	put("admitted_range_read_forms", func() any {
		return admitted(programPhrase{"sed", "`sed -n 'A,Bp'`"}, programPhrase{"cat", "`cat -n`"})
	})
	put("admitted_text_search_forms", func() any {
		return admitted(programPhrase{"grep", "`grep -n`"}, programPhrase{"rg", "`rg -n`"})
	})
	put("admitted_file_read_forms", func() any {
		return admitted(programPhrase{"cat", "`cat`"}, programPhrase{"head", "`head`"}, programPhrase{"sed", "`sed -n`"})
	})
	put("admitted_raw_read_forms", func() any {
		return admitted(programPhrase{"cat", "`cat`"}, programPhrase{"sed", "`sed -n`"})
	})
	put("admitted_large_read_forms", func() any {
		return admitted(programPhrase{"head", "head"}, programPhrase{"tail", "tail"}, programPhrase{"sed", "sed"}, programPhrase{"grep", "grep"})
	})
	put("admitted_read_addressing_forms", func() any {
		if !cmdCallable {
			return ""
		}
		var parts []string
		if request.Program("rg") {
			parts = append(parts, "`rg -n` for a symbol")
		}
		if request.Program("sed") {
			parts = append(parts, "`sed -n 'A,Bp'` for a range")
		}
		switch {
		case request.Program("head") && request.Program("tail"):
			parts = append(parts, "`head`/`tail` for a window")
		case request.Program("head"):
			parts = append(parts, "`head` for a window")
		case request.Program("tail"):
			parts = append(parts, "`tail` for a window")
		}
		return strings.Join(parts, ", ")
	})
	put("admitted_stream_edit_forms", func() any {
		return admitted(programPhrase{"sed", "sed"}, programPhrase{"awk", "awk"}, programPhrase{"jq", "jq"})
	})
	put("admitted_archive_forms", func() any {
		return admitted(
			programPhrase{"unzip", "inspect a single zip member with `unzip -p`"},
			programPhrase{"tar", "extract a tar member with `tar -x -f`"},
		)
	})
	put("admitted_mutation_forms", func() any {
		return admitted(programPhrase{"edit", "`edit --path`"}, programPhrase{"replace-lines", "`replace-lines`"})
	})
	put("admitted_secret_config_forms", func() any {
		return admitted(
			programPhrase{"edit", "`edit --path FILE --old-string OLD --new-string 'KEY=<reference>'`"},
			programPhrase{"replace-lines", "`replace-lines --content`"},
		)
	})
	registerSingleProgramReachFacts(put, cmdCallable, request)
}

func registerSingleProgramReachFacts(put func(string, func() any), cmdCallable bool, request toolreach.Request) {
	put("command_jq_edit_reachable", func() any { return cmdCallable && request.Program("jq-edit") })
	put("command_git_config_reachable", func() any {
		return cmdCallable && (request.Program("git config") || request.Program("git"))
	})
	put("command_wc_reachable", func() any { return cmdCallable && request.Program("wc") })
	put("command_tee_reachable", func() any { return cmdCallable && request.Program("tee") })
	put("command_sed_reachable", func() any { return cmdCallable && request.Program("sed") })
	put("command_find_reachable", func() any { return cmdCallable && request.Program("find") })
	put("command_grep_reachable", func() any { return cmdCallable && request.Program("grep") })
	put("command_repo_map_reachable", func() any { return cmdCallable && request.Program("repo-map") })
}

func joinDisjunction(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + " or " + items[1]
	default:
		return strings.Join(items[:len(items)-1], ", ") + ", or " + items[len(items)-1]
	}
}
