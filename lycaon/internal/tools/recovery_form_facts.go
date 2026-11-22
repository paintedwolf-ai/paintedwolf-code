package tools

import (
	"strings"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/toolreach"
)

type invocationPhrase struct {
	words []string
	label string
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
	admitted := func(items ...invocationPhrase) string {
		if !cmdCallable {
			return ""
		}
		var out []string
		for _, it := range items {
			if request.ProgramInvocation(it.words...) {
				out = append(out, it.label)
			}
		}
		return joinDisjunction(out)
	}

	put("admitted_process_capability_fields", func() any {
		if !request.Known() || !request.ProgramMode("external", "dispatch") {
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
	put("command_loadable_without_schema", func() any {
		_, hasSchema := request.ToolSchema(toolreach.ProgramTool)
		return request.Known() &&
			request.Status(toolreach.ProgramTool) == toolreach.Loadable &&
			request.ProgramMode("external", "dispatch") &&
			!hasSchema
	})
	registerReadFormRecoveryFacts(put, admitted, cmdCallable, request)
	put("admitted_stream_edit_forms", func() any {
		return admitted(
			invocationPhrase{[]string{"sed", "s/a/b/", "FILE"}, "sed"},
			invocationPhrase{[]string{"awk", "{print}", "FILE"}, "awk"},
			invocationPhrase{[]string{"jq", ".", "FILE"}, "jq"},
		)
	})
	put("admitted_archive_forms", func() any {
		return admitted(
			invocationPhrase{[]string{"unzip", "-p", "ARCHIVE.zip", "MEMBER"}, "inspect a single zip member with `unzip -p`"},
			invocationPhrase{[]string{"tar", "-t", "-f", "ARCHIVE.tar"}, "list tar members with `tar -t -f`"},
			invocationPhrase{[]string{"tar", "-x", "-f", "ARCHIVE.tar"}, "extract a tar member with `tar -x -f`"},
		)
	})
	put("admitted_mutation_forms", func() any {
		return admitted(
			invocationPhrase{[]string{"edit", "--path", "FILE", "--old-string", "OLD", "--new-string", "NEW"}, "`edit --path`"},
			invocationPhrase{[]string{"replace-lines", "--path", "FILE", "--start", "1", "--end", "1", "--content", "NEW"}, "`replace-lines`"},
		)
	})
	put("admitted_secret_config_forms", func() any {
		return admitted(
			invocationPhrase{[]string{"edit", "--path", "FILE", "--old-string", "OLD", "--new-string", "KEY=<reference>"}, "`edit --path FILE --old-string OLD --new-string 'KEY=<reference>'`"},
			invocationPhrase{[]string{"replace-lines", "--path", "FILE", "--start", "1", "--end", "1", "--content", "NEW"}, "`replace-lines --content`"},
		)
	})
	registerSingleProgramReachFacts(put, cmdCallable, request)
}

func registerReadFormRecoveryFacts(
	put func(string, func() any),
	admitted func(...invocationPhrase) string,
	cmdCallable bool,
	request toolreach.Request,
) {
	put("admitted_source_read_forms", func() any {
		return admitted(
			invocationPhrase{[]string{"sed", "-n", "1,10p", "FILE"}, "`sed -n`"},
			invocationPhrase{[]string{"cat", "-n", "FILE"}, "`cat -n`"},
		)
	})
	put("admitted_range_read_forms", func() any {
		return admitted(
			invocationPhrase{[]string{"sed", "-n", "1,10p", "FILE"}, "`sed -n 'A,Bp'`"},
			invocationPhrase{[]string{"cat", "-n", "FILE"}, "`cat -n`"},
		)
	})
	put("admitted_text_search_forms", func() any {
		return admitted(
			invocationPhrase{[]string{"grep", "-n", "PATTERN", "FILE"}, "`grep -n`"},
			invocationPhrase{[]string{"rg", "-n", "PATTERN", "FILE"}, "`rg -n`"},
		)
	})
	put("admitted_file_read_forms", func() any {
		return admitted(
			invocationPhrase{[]string{"cat", "FILE"}, "`cat`"},
			invocationPhrase{[]string{"head", "FILE"}, "`head`"},
			invocationPhrase{[]string{"sed", "-n", "1,10p", "FILE"}, "`sed -n`"},
		)
	})
	put("admitted_raw_read_forms", func() any {
		return admitted(
			invocationPhrase{[]string{"cat", "FILE"}, "`cat`"},
			invocationPhrase{[]string{"sed", "-n", "1,10p", "FILE"}, "`sed -n`"},
		)
	})
	put("admitted_large_read_forms", func() any {
		return admitted(
			invocationPhrase{[]string{"head", "FILE"}, "head"},
			invocationPhrase{[]string{"tail", "FILE"}, "tail"},
			invocationPhrase{[]string{"sed", "-n", "1,10p", "FILE"}, "sed"},
			invocationPhrase{[]string{"grep", "PATTERN", "FILE"}, "grep"},
		)
	})
	put("admitted_read_addressing_forms", func() any {
		if !cmdCallable {
			return ""
		}
		var parts []string
		if request.ProgramInvocation("rg", "-n", "SYMBOL", "FILE") {
			parts = append(parts, "`rg -n` for a symbol")
		}
		if request.ProgramInvocation("sed", "-n", "1,10p", "FILE") {
			parts = append(parts, "`sed -n 'A,Bp'` for a range")
		}
		headOK := request.ProgramInvocation("head", "FILE")
		tailOK := request.ProgramInvocation("tail", "FILE")
		switch {
		case headOK && tailOK:
			parts = append(parts, "`head`/`tail` for a window")
		case headOK:
			parts = append(parts, "`head` for a window")
		case tailOK:
			parts = append(parts, "`tail` for a window")
		}
		return strings.Join(parts, ", ")
	})
}

func registerSingleProgramReachFacts(put func(string, func() any), cmdCallable bool, request toolreach.Request) {
	put("command_jq_edit_reachable", func() any {
		return cmdCallable && request.ProgramInvocation("jq-edit", "--path", "FILE", "--query", ".")
	})
	put("command_git_config_reachable", func() any {
		return cmdCallable &&
			request.ProgramInvocation("git", "config", "--list") &&
			request.ProgramInvocation("git", "config", "--get", "core.bare")
	})
	put("command_wc_reachable", func() any { return cmdCallable && request.ProgramInvocation("wc", "-l", "FILE") })
	put("command_tee_reachable", func() any { return cmdCallable && request.ProgramInvocation("tee", "FILE") })
	put("command_sed_reachable", func() any {
		return cmdCallable && request.ProgramInvocation("sed", "-n", "1,10p", "FILE")
	})
	put("command_find_reachable", func() any {
		return cmdCallable && request.ProgramInvocation("find", ".", "-type", "f")
	})
	put("command_grep_reachable", func() any {
		return cmdCallable && request.ProgramInvocation("grep", "-n", "PATTERN", "FILE")
	})
	put("command_repo_map_reachable", func() any { return cmdCallable && request.ProgramInvocation("repo-map") })
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
