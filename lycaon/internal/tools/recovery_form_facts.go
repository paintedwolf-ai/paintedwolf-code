package tools

import (
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/toolreach"
)

func registerCommandFormRecoveryFacts(gc *oar.GuardContext, request toolreach.Request) {
	cmdStatus := request.Status(toolreach.ProgramTool)
	cmdCallable := cmdStatus == toolreach.Callable
	cmdLoadable := cmdStatus == toolreach.Loadable
	_, hasCmdSchema := request.ToolSchema(toolreach.ProgramTool)
	cmdFieldAdmitted := request.AcceptsField(toolreach.ProgramTool, "command")
	cmdLoaderOnly := cmdLoadable && !hasCmdSchema

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
		if !cmdFieldAdmitted {
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
		if !cmdFieldAdmitted {
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
		if !cmdFieldAdmitted || len(words) == 0 || !request.ProgramInvocation(words...) {
			return ""
		}
		return toolreach.QuoteProgram(words)
	}
	loaderOnly := func(invocations ...[]string) bool {
		if !cmdLoaderOnly {
			return false
		}
		for _, words := range invocations {
			if len(words) > 0 && request.ProgramInvocation(words...) {
				return true
			}
		}
		return false
	}

	put("command_loader_call", func() any {
		if !cmdLoadable {
			return ""
		}
		return toolreach.Load(toolreach.ProgramTool).Text()
	})
	put("uncompressed_reader_call", func() any {
		if request.Status("read") == toolreach.Callable && request.AcceptsField("read", "path") {
			return (toolreach.Call{Tool: "read", Partial: true}).Text()
		}
		return ""
	})
	put("admitted_process_capability_fields", func() any {
		if !request.Known() || !cmdFieldAdmitted || !request.ProgramMode("external", "dispatch") {
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
		return request.Known() && cmdLoaderOnly && request.ProgramMode("external", "dispatch")
	})

	registerReadFormRecoveryFacts(put, admittedIDs, admittedCalls, admittedCall, loaderOnly)
	registerWriteAndArchiveFormRecoveryFacts(put, admittedIDs, admittedCalls, admittedCall, loaderOnly, cmdFieldAdmitted, cmdLoaderOnly, request)
	registerSingleProgramReachFacts(put, cmdCallable, cmdFieldAdmitted, request)
}

func registerWriteAndArchiveFormRecoveryFacts(
	put func(string, func() any),
	admittedIDs func(...[]string) []string,
	admittedCalls func(...[]string) []string,
	admittedCall func(...string) string,
	loaderOnly func(...[]string) bool,
	cmdFieldAdmitted bool,
	cmdLoaderOnly bool,
	request toolreach.Request,
) {
	sedEdit := []string{"sed", "s/a/b/", "FILE"}
	awkPrint := []string{"awk", "{print}", "FILE"}
	jqIdentity := []string{"jq", ".", "FILE"}
	put("admitted_stream_edit_forms", func() any { return admittedIDs(sedEdit, awkPrint, jqIdentity) })
	put("stream_edit_forms_loadable_without_schema", func() any { return loaderOnly(sedEdit, awkPrint, jqIdentity) })

	unzipStream := []string{"unzip", "-p", "ARCHIVE.zip", "MEMBER"}
	tarList := []string{"tar", "-t", "-f", "ARCHIVE.tar"}
	tarExtract := []string{"tar", "-x", "-f", "ARCHIVE.tar"}
	put("admitted_archive_forms", func() any { return admittedCalls(unzipStream, tarList, tarExtract) })
	put("archive_unzip_stream_call", func() any { return admittedCall(unzipStream...) })
	put("archive_tar_list_call", func() any { return admittedCall(tarList...) })
	put("archive_tar_extract_call", func() any { return admittedCall(tarExtract...) })
	put("archive_forms_loadable_without_schema", func() any { return loaderOnly(unzipStream, tarList, tarExtract) })

	editMut := []string{"edit", "--path", "FILE", "--old-string", "OLD", "--new-string", "NEW"}
	replaceMut := []string{"replace-lines", "--path", "FILE", "--start", "1", "--end", "1", "--content", "NEW"}
	put("admitted_mutation_forms", func() any { return admittedCalls(editMut, replaceMut) })
	put("mutation_forms_loadable_without_schema", func() any { return loaderOnly(editMut, replaceMut) })

	editSecret := []string{"edit", "--path", "FILE", "--old-string", "OLD", "--new-string", "KEY=<reference>"}
	replaceSecret := []string{"replace-lines", "--path", "FILE", "--start", "1", "--end", "1", "--content", "KEY=<reference>"}
	jqSecret := []string{"jq-edit", "--path", "FILE", "--query", ".key = $value", "--arg", "value", "<reference>"}
	put("admitted_secret_config_forms", func() any { return admittedCalls(editSecret, replaceSecret) })
	put("secret_jq_edit_call", func() any { return admittedCall(jqSecret...) })
	put("secret_external_command_admitted", func() any {
		return cmdFieldAdmitted && request.ProgramMode("external", "dispatch")
	})
	put("secret_forms_loadable_without_schema", func() any {
		return loaderOnly(editSecret, replaceSecret, jqSecret) || (cmdLoaderOnly && request.ProgramMode("external", "dispatch"))
	})
}

func registerReadFormRecoveryFacts(
	put func(string, func() any),
	admittedIDs func(...[]string) []string,
	admittedCalls func(...[]string) []string,
	admittedCall func(...string) string,
	loaderOnly func(...[]string) bool,
) {
	sedLines := []string{"sed", "-n", "1,10p", "FILE"}
	catLines := []string{"cat", "-n", "FILE"}
	catFile := []string{"cat", "FILE"}
	headFile := []string{"head", "FILE"}
	tailFile := []string{"tail", "FILE"}
	grepLines := []string{"grep", "-n", "PATTERN", "FILE"}
	rgLines := []string{"rg", "-n", "PATTERN", "FILE"}
	grepPlain := []string{"grep", "PATTERN", "FILE"}
	rgSymbol := []string{"rg", "-n", "SYMBOL", "FILE"}

	put("admitted_source_read_forms", func() any { return admittedCalls(sedLines, catLines) })
	put("source_read_forms_loadable_without_schema", func() any { return loaderOnly(sedLines, catLines) })

	put("admitted_range_read_forms", func() any { return admittedCalls(sedLines, catLines) })
	put("range_read_forms_loadable_without_schema", func() any { return loaderOnly(sedLines, catLines) })

	put("admitted_text_search_forms", func() any { return admittedCalls(grepLines, rgLines) })
	put("text_search_forms_loadable_without_schema", func() any { return loaderOnly(grepLines, rgLines) })

	put("admitted_file_read_forms", func() any { return admittedCalls(catFile, headFile, sedLines) })
	put("file_read_forms_loadable_without_schema", func() any { return loaderOnly(catFile, headFile, sedLines) })

	put("admitted_raw_read_forms", func() any { return admittedCalls(catFile, sedLines) })
	put("raw_read_forms_loadable_without_schema", func() any { return loaderOnly(catFile, sedLines) })

	put("admitted_large_read_forms", func() any { return admittedIDs(headFile, tailFile, sedLines, grepPlain) })
	put("large_read_forms_loadable_without_schema", func() any { return loaderOnly(headFile, tailFile, sedLines, grepPlain) })

	put("admitted_read_addressing_forms", func() any { return admittedCalls(rgSymbol, sedLines, headFile, tailFile) })
	put("read_symbol_call", func() any { return admittedCall(rgSymbol...) })
	put("read_range_call", func() any { return admittedCall(sedLines...) })
	put("read_window_calls", func() any { return admittedCalls(headFile, tailFile) })
	put("read_addressing_forms_loadable_without_schema", func() any { return loaderOnly(rgSymbol, sedLines, headFile, tailFile) })
}

func registerSingleProgramReachFacts(
	put func(string, func() any),
	cmdCallable bool,
	cmdFieldAdmitted bool,
	request toolreach.Request,
) {
	callableAdmitted := cmdCallable && cmdFieldAdmitted
	put("command_jq_edit_reachable", func() any {
		return cmdFieldAdmitted && request.ProgramInvocation("jq-edit", "--path", "FILE", "--query", ".")
	})
	put("command_git_config_reachable", func() any {
		return callableAdmitted &&
			request.ProgramInvocation("git", "config", "--list") &&
			request.ProgramInvocation("git", "config", "--get", "core.bare")
	})
	put("command_wc_reachable", func() any { return callableAdmitted && request.ProgramInvocation("wc", "-l", "FILE") })
	put("command_tee_reachable", func() any { return callableAdmitted && request.ProgramInvocation("tee", "FILE") })
	put("command_sed_reachable", func() any {
		return callableAdmitted && request.ProgramInvocation("sed", "-n", "1,10p", "FILE")
	})
	put("command_find_reachable", func() any {
		return callableAdmitted && request.ProgramInvocation("find", ".", "-type", "f")
	})
	put("command_grep_reachable", func() any {
		return callableAdmitted && request.ProgramInvocation("grep", "-n", "PATTERN", "FILE")
	})
	put("command_repo_map_reachable", func() any { return callableAdmitted && request.ProgramInvocation("repo-map") })
}
