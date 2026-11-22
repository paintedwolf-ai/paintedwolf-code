package tools

import (
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/toolreach"
)

func registerCommandFormRecoveryFacts(gc *oar.GuardContext, request toolreach.Request) {
	cmdStatus := request.Status(toolreach.ProgramTool)
	cmdCallable := cmdStatus == toolreach.Callable
	cmdLoadable := cmdStatus == toolreach.Loadable
	_, hasSchema := request.ToolSchema(toolreach.ProgramTool)
	cmdReachable := cmdCallable || (cmdLoadable && (!hasSchema || request.AcceptsField(toolreach.ProgramTool, "command")))

	put := func(bare string, compute func() any) {
		gc.RegisterProvider("paintedwolf."+bare, func(target *oar.GuardContext) error {
			if target.ObservationData == nil {
				target.ObservationData = map[string]any{}
			}
			target.ObservationData[bare] = compute()
			return nil
		})
	}
	admittedIDs := func(invocations ...[]string) []string {
		if !cmdReachable {
			return []string{}
		}
		out := make([]string, 0, len(invocations))
		for _, words := range invocations {
			if len(words) > 0 && request.ProgramInvocation(words...) {
				out = append(out, words[0])
			}
		}
		return out
	}
	admittedCalls := func(invocations ...[]string) []string {
		if !cmdReachable {
			return []string{}
		}
		out := make([]string, 0, len(invocations))
		for _, words := range invocations {
			if len(words) > 0 && request.ProgramInvocation(words...) {
				out = append(out, toolreach.QuoteProgram(words))
			}
		}
		return out
	}
	admittedCall := func(words ...string) string {
		if !cmdReachable || len(words) == 0 || !request.ProgramInvocation(words...) {
			return ""
		}
		return toolreach.QuoteProgram(words)
	}

	put("command_loader_call", func() any {
		if !cmdLoadable || !cmdReachable {
			return ""
		}
		return toolreach.Load(toolreach.ProgramTool).Text()
	})
	put("admitted_process_capability_fields", func() any {
		if !request.Known() || !request.ProgramMode("external", "dispatch") {
			return []string{}
		}
		out := make([]string, 0, 2)
		for _, field := range []string{"capability_request.process_control", "capability_request.host_execution"} {
			if request.AcceptsField(toolreach.ProgramTool, field) {
				out = append(out, field)
			}
		}
		return out
	})
	put("command_loadable_without_schema", func() any {
		return request.Known() && cmdLoadable && request.ProgramMode("external", "dispatch") && !hasSchema
	})

	registerReadFormRecoveryFacts(put, admittedIDs, admittedCalls, admittedCall)

	put("admitted_stream_edit_forms", func() any {
		return admittedIDs(
			[]string{"sed", "s/a/b/", "FILE"},
			[]string{"awk", "{print}", "FILE"},
			[]string{"jq", ".", "FILE"},
		)
	})

	unzipStream := []string{"unzip", "-p", "ARCHIVE.zip", "MEMBER"}
	tarList := []string{"tar", "-t", "-f", "ARCHIVE.tar"}
	tarExtract := []string{"tar", "-x", "-f", "ARCHIVE.tar"}
	put("admitted_archive_forms", func() any { return admittedCalls(unzipStream, tarList, tarExtract) })
	put("archive_unzip_stream_call", func() any { return admittedCall(unzipStream...) })
	put("archive_tar_list_call", func() any { return admittedCall(tarList...) })
	put("archive_tar_extract_call", func() any { return admittedCall(tarExtract...) })

	put("admitted_mutation_forms", func() any {
		return admittedCalls(
			[]string{"edit", "--path", "FILE", "--old-string", "OLD", "--new-string", "NEW"},
			[]string{"replace-lines", "--path", "FILE", "--start", "1", "--end", "1", "--content", "NEW"},
		)
	})
	put("admitted_secret_config_forms", func() any {
		return admittedCalls(
			[]string{"edit", "--path", "FILE", "--old-string", "OLD", "--new-string", "KEY=<reference>"},
			[]string{"replace-lines", "--path", "FILE", "--start", "1", "--end", "1", "--content", "KEY=<reference>"},
		)
	})
	put("secret_jq_edit_call", func() any {
		return admittedCall("jq-edit", "--path", "FILE", "--query", ".key = $value", "--arg", "value", "<reference>")
	})

	registerSingleProgramReachFacts(put, cmdCallable, cmdReachable, request)
}

func registerReadFormRecoveryFacts(
	put func(string, func() any),
	admittedIDs func(...[]string) []string,
	admittedCalls func(...[]string) []string,
	admittedCall func(...string) string,
) {
	sedLines := []string{"sed", "-n", "1,10p", "FILE"}
	catLines := []string{"cat", "-n", "FILE"}
	catFile := []string{"cat", "FILE"}
	headFile := []string{"head", "FILE"}
	tailFile := []string{"tail", "FILE"}

	put("admitted_source_read_forms", func() any { return admittedCalls(sedLines, catLines) })
	put("admitted_range_read_forms", func() any { return admittedCalls(sedLines, catLines) })
	put("admitted_text_search_forms", func() any {
		return admittedCalls(
			[]string{"grep", "-n", "PATTERN", "FILE"},
			[]string{"rg", "-n", "PATTERN", "FILE"},
		)
	})
	put("admitted_file_read_forms", func() any { return admittedCalls(catFile, headFile, sedLines) })
	put("admitted_raw_read_forms", func() any { return admittedCalls(catFile, sedLines) })
	put("admitted_large_read_forms", func() any {
		return admittedIDs(headFile, tailFile, sedLines, []string{"grep", "PATTERN", "FILE"})
	})

	rgSymbol := []string{"rg", "-n", "SYMBOL", "FILE"}
	put("admitted_read_addressing_forms", func() any { return admittedCalls(rgSymbol, sedLines, headFile, tailFile) })
	put("read_symbol_call", func() any { return admittedCall(rgSymbol...) })
	put("read_range_call", func() any { return admittedCall(sedLines...) })
	put("read_window_calls", func() any { return admittedCalls(headFile, tailFile) })
}

func registerSingleProgramReachFacts(
	put func(string, func() any),
	cmdCallable bool,
	cmdReachable bool,
	request toolreach.Request,
) {
	put("command_jq_edit_reachable", func() any {
		return cmdReachable && request.ProgramInvocation("jq-edit", "--path", "FILE", "--query", ".")
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
