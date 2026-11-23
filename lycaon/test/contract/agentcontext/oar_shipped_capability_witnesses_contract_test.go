package contract

import (
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func reachWithProgramsAndSchemas(offered []string, allowedProgs []string, schemas map[string]map[string]any) toolreach.Request {
	req := toolreach.Freeze(offered, toolsurface.Compile(offered, nil))
	if allowedProgs != nil {
		req = req.WithPrograms(func(name string) bool {
			return slices.Contains(allowedProgs, name)
		})
	}
	if slices.Contains(offered, "command") && (schemas == nil || schemas["command"] == nil) {
		merged := map[string]map[string]any{
			"command": {"properties": map[string]any{"command": map[string]any{"type": "string"}}},
		}
		for k, v := range schemas {
			merged[k] = v
		}
		schemas = merged
	}
	if schemas != nil {
		req = req.WithSchemas(schemas)
	}
	return req
}

func TestShippedOARProjectedSchemaAndSubsetWitnesses(t *testing.T) {
	_, _, _, bp := effectiveOARBlockPlane(t)

	rejectFix := func(tool, code string, reach toolreach.Request, data map[string]any) string {
		ctx := toolreach.WithRequest(t.Context(), reach)
		err := bp.RejectObservation(ctx, tool, "coordinator", nil, &toolrejection.ToolReject{
			Code: code, Data: data,
		})
		ref, ok := guidance.RefusalFromError(err)
		if !ok || ref == nil || ref.Code() != code {
			t.Fatalf("RejectObservation(%s) = %v, want %s", code, err, code)
		}
		return ref.Copy["fix"]
	}

	t.Run("process_unsupported_projected_capability_request_subset", func(t *testing.T) {
		fullSchema := map[string]map[string]any{
			"command": {
				"properties": map[string]any{
					"command": map[string]any{"type": "string"},
					"capability_request": map[string]any{
						"properties": map[string]any{
							"process_control": map[string]any{"type": "object"},
							"host_execution":  map[string]any{"type": "object"},
						},
					},
				},
			},
		}
		partialSchema := map[string]map[string]any{
			"command": {
				"properties": map[string]any{
					"command": map[string]any{"type": "string"},
					"capability_request": map[string]any{
						"properties": map[string]any{
							"process_control": map[string]any{"type": "object"},
						},
					},
				},
			},
		}
		readOnlySchema := map[string]map[string]any{
			"command": {
				"properties": map[string]any{
					"command": map[string]any{"type": "string"},
				},
			},
		}

		bothFix := rejectFix("process_signal", "PROCESS_UNSUPPORTED",
			reachWithProgramsAndSchemas([]string{"command"}, []string{"external"}, fullSchema), map[string]any{})
		if !strings.Contains(bothFix, "`command(") ||
			!strings.Contains(bothFix, "capability_request.process_control, capability_request.host_execution") {
			t.Fatalf("full schema should show both capability_request fields: %q", bothFix)
		}

		subsetFix := rejectFix("process_signal", "PROCESS_UNSUPPORTED",
			reachWithProgramsAndSchemas([]string{"command"}, []string{"external"}, partialSchema), map[string]any{})
		if !strings.Contains(subsetFix, "`command(") ||
			!strings.Contains(subsetFix, "capability_request.process_control") ||
			strings.Contains(subsetFix, "host_execution") {
			t.Fatalf("partial schema must keep process_control visible while omitting host_execution: %q", subsetFix)
		}

		prunedFix := rejectFix("process_signal", "PROCESS_UNSUPPORTED",
			reachWithProgramsAndSchemas([]string{"command"}, []string{"external"}, readOnlySchema), map[string]any{})
		if strings.Contains(prunedFix, "`command(") || strings.Contains(prunedFix, "capability_request") ||
			!strings.Contains(prunedFix, "cannot run host process commands") {
			t.Fatalf("read-only pruned command schema must withhold capability_request: %q", prunedFix)
		}
	})

	t.Run("source_parse_and_command_program_subsets_with_command_offered_in_both", func(t *testing.T) {
		parseData := map[string]any{"path": "a.go", "parse_language": "go", "parse_failure": "timeout", "parse_timed_out": true, "parse_config": "lycaon.yaml"}
		for _, code := range []string{"SOURCE_PARSE_FAILED", "SOURCE_PARSE_INCOMPLETE"} {
			allReach := reachWithProgramsAndSchemas([]string{"command"}, []string{"sed", "cat", "grep", "rg"}, nil)
			subsetReach := reachWithProgramsAndSchemas([]string{"command"}, []string{"sed", "rg"}, nil)

			full := rejectFix("code_rewrite", code, allReach, parseData)
			if !strings.Contains(full, "sed -n 1,10p FILE, cat -n FILE") || !strings.Contains(full, "grep -n PATTERN FILE, rg -n PATTERN FILE") {
				t.Fatalf("%s with all 4 forms must list sed/cat and grep/rg: %q", code, full)
			}
			subset := rejectFix("code_rewrite", code, subsetReach, parseData)
			if !strings.Contains(subset, "sed -n 1,10p FILE") || !strings.Contains(subset, "rg -n PATTERN FILE") ||
				strings.Contains(subset, "cat -n FILE") || strings.Contains(subset, "grep -n PATTERN FILE") {
				t.Fatalf("%s subset reach must keep sed+rg visible and omit cat+grep: %q", code, subset)
			}
		}

		secretData := map[string]any{"path": "cfg.json", "detail": "outside sink"}
		secretAll := rejectFix("write", "SECRET_REFERENCE_OUTSIDE_SINK",
			reachWithProgramsAndSchemas([]string{"command"}, []string{"edit", "replace-lines", "jq-edit"}, nil), secretData)
		if !strings.Contains(secretAll, "edit --path FILE --old-string OLD --new-string 'KEY=<reference>'") ||
			!strings.Contains(secretAll, "replace-lines --path FILE --start 1 --end 1 --content 'KEY=<reference>'") ||
			!strings.Contains(secretAll, "`jq-edit --path FILE --query '.key = $value' --arg value '<reference>'`") {
			t.Fatalf("SECRET_REFERENCE_OUTSIDE_SINK full reach missing forms: %q", secretAll)
		}
		secretEditOnly := rejectFix("write", "SECRET_REFERENCE_OUTSIDE_SINK",
			reachWithProgramsAndSchemas([]string{"command"}, []string{"edit"}, nil), secretData)
		if !strings.Contains(secretEditOnly, "edit --path FILE --old-string OLD --new-string 'KEY=<reference>'") ||
			strings.Contains(secretEditOnly, "replace-lines") || strings.Contains(secretEditOnly, "jq-edit") {
			t.Fatalf("SECRET_REFERENCE_OUTSIDE_SINK edit-only subset leaked unavailable forms: %q", secretEditOnly)
		}

		extractData := map[string]any{"path": "bundle.7z", "format": "7z"}
		extractReadOnly := rejectFix("extract_archive", "EXTRACT_FORMAT_UNSUPPORTED",
			reachWithProgramsAndSchemas([]string{"command"}, []string{"unzip", "tar"}, nil), extractData)
		if !strings.Contains(extractReadOnly, "`unzip -p ARCHIVE.zip MEMBER`") || !strings.Contains(extractReadOnly, "`tar -t -f ARCHIVE.tar`") ||
			strings.Contains(extractReadOnly, "`tar -x -f ARCHIVE.tar`") {
			t.Fatalf("EXTRACT_FORMAT_UNSUPPORTED generic read reach must retain unzip -p and tar -t -f while omitting tar -x -f: %q", extractReadOnly)
		}
		extractMutating := rejectFix("extract_archive", "EXTRACT_FORMAT_UNSUPPORTED",
			toolreach.Offered([]string{"command"}).WithProgramTable(toolreach.NewProgramTable(
				map[string]bool{"unzip": true, "tar": true},
				map[string]map[string]bool{
					"unzip": {"listing-or-stream-decode": true, "create-extract-compress-decompress-to-path": true},
					"tar":   {"listing-or-stream-decode": true, "create-extract-compress-decompress-to-path": true},
				},
			)).WithSchemas(map[string]map[string]any{
				"command": {"properties": map[string]any{"command": map[string]any{"type": "string"}}},
			}), extractData)
		if !strings.Contains(extractMutating, "`unzip -p ARCHIVE.zip MEMBER`") || !strings.Contains(extractMutating, "`tar -t -f ARCHIVE.tar`") ||
			!strings.Contains(extractMutating, "`tar -x -f ARCHIVE.tar`") {
			t.Fatalf("EXTRACT_FORMAT_UNSUPPORTED full mode reach missing unzip/tar list/extract: %q", extractMutating)
		}
		extractZipOnly := rejectFix("extract_archive", "EXTRACT_FORMAT_UNSUPPORTED",
			reachWithProgramsAndSchemas([]string{"command"}, []string{"unzip"}, nil), extractData)
		if !strings.Contains(extractZipOnly, "`unzip -p ARCHIVE.zip MEMBER`") || strings.Contains(extractZipOnly, "`tar -") {
			t.Fatalf("EXTRACT_FORMAT_UNSUPPORTED unzip-only subset leaked tar: %q", extractZipOnly)
		}

		// Pack-authored purpose labels, admitted subsets, and loading states across recovery_form_facts consumers.
		addrData := map[string]any{"detail": "symbol and offset", "path": "src/main.go"}
		addrFull := rejectFix("read", "READ_ARGS_CONFLICT",
			reachWithProgramsAndSchemas([]string{"command"}, []string{"rg", "sed", "head", "tail"}, nil), addrData)
		if !strings.Contains(addrFull, "For a symbol, use `rg -n SYMBOL FILE`.") ||
			!strings.Contains(addrFull, "For a line range, use `sed -n 1,10p FILE`.") ||
			!strings.Contains(addrFull, "For a window, use head FILE, tail FILE.") {
			t.Fatalf("READ_ARGS_CONFLICT full reach render = %q", addrFull)
		}
		addrSubset := rejectFix("read", "READ_ARGS_CONFLICT",
			reachWithProgramsAndSchemas([]string{"command"}, []string{"rg", "head"}, nil), addrData)
		if !strings.Contains(addrSubset, "`rg -n SYMBOL FILE`") || !strings.Contains(addrSubset, "head FILE") ||
			strings.Contains(addrSubset, "sed -n") || strings.Contains(addrSubset, "tail FILE") {
			t.Fatalf("READ_ARGS_CONFLICT rg+head subset render = %q", addrSubset)
		}

		loadableNoSchema := toolreach.Freeze([]string{"request_tools"}, toolsurface.Compile([]string{"request_tools"}, []string{"command"})).
			WithPrograms(func(name string) bool { return name == "rg" || name == "head" || name == "awk" })
		loadableRead := loadableNoSchema.WithSchemas(map[string]map[string]any{
			"command": {"properties": map[string]any{"command": map[string]any{"type": "string"}}},
		})
		deferredWithoutLoader := toolreach.Freeze([]string{"read"}, toolsurface.Compile([]string{"read"}, []string{"command"})).
			WithPrograms(func(name string) bool { return true })
		absentCmd := reachWithProgramsAndSchemas([]string{"read"}, nil, nil)

		addrLoadable := rejectFix("read", "READ_ARGS_CONFLICT", loadableRead, addrData)
		if !strings.Contains(addrLoadable, "`request_tools(need=\"command\")`") || !strings.Contains(addrLoadable, "`rg -n SYMBOL FILE`") {
			t.Fatalf("READ_ARGS_CONFLICT loadable command with schema missing loader or rg call: %q", addrLoadable)
		}
		addrLoaderOnly := rejectFix("read", "READ_ARGS_CONFLICT", loadableNoSchema, addrData)
		if !strings.Contains(addrLoaderOnly, "`request_tools(need=\"command\")`") || strings.Contains(addrLoaderOnly, "rg -n") {
			t.Fatalf("READ_ARGS_CONFLICT loader-only missing projection must emit loader without concrete rg call: %q", addrLoaderOnly)
		}
		for _, unreachable := range []toolreach.Request{deferredWithoutLoader, absentCmd} {
			got := rejectFix("read", "READ_ARGS_CONFLICT", unreachable, addrData)
			if strings.Contains(got, "inside command") || strings.Contains(got, "request_tools") {
				t.Fatalf("READ_ARGS_CONFLICT unreachable command leaked command/loader: %q", got)
			}
		}

		largeEditData := map[string]any{"path": "big.go", "max_file_bytes": "4194304", "size": "5000000"}
		editAwkLoadable := rejectFix("edit", "EDIT_FILE_TOO_LARGE", loadableRead, largeEditData)
		if !strings.Contains(editAwkLoadable, "`request_tools(need=\"command\")`") || !strings.Contains(editAwkLoadable, "(awk)") ||
			strings.Contains(editAwkLoadable, "sed") || strings.Contains(editAwkLoadable, "jq") {
			t.Fatalf("EDIT_FILE_TOO_LARGE loadable awk-only subset render = %q", editAwkLoadable)
		}
		if got := rejectFix("edit", "EDIT_FILE_TOO_LARGE", deferredWithoutLoader, largeEditData); strings.Contains(got, "Use command") || strings.Contains(got, "request_tools") {
			t.Fatalf("EDIT_FILE_TOO_LARGE deferred without loader leaked command: %q", got)
		}
		if got := rejectFix("jq_edit", "JQ_EDIT_LOSSY", loadableRead, map[string]any{"path": "Cargo.toml", "format": "toml", "kind": "comment"}); strings.Contains(got, "request_tools") || !strings.Contains(got, "read-only profile") {
			t.Fatalf("JQ_EDIT_LOSSY loadable command without admitted mutation forms must report read-only limitation: %q", got)
		}

		dirData := map[string]any{"path": "plans"}
		dirCatAndSed := rejectFix("read", "READ_IS_DIRECTORY",
			reachWithProgramsAndSchemas([]string{"command"}, []string{"cat", "sed"}, nil), dirData)
		if !strings.Contains(dirCatAndSed, "cat -n FILE") || !strings.Contains(dirCatAndSed, "sed -n A,Bp FILE") {
			t.Fatalf("READ_IS_DIRECTORY with cat+sed must offer both: %q", dirCatAndSed)
		}
		dirCatOnly := rejectFix("read", "READ_IS_DIRECTORY",
			reachWithProgramsAndSchemas([]string{"command"}, []string{"cat"}, nil), dirData)
		if !strings.Contains(dirCatOnly, "cat -n FILE") || strings.Contains(dirCatOnly, "sed -n") {
			t.Fatalf("READ_IS_DIRECTORY with cat only must keep cat and omit sed: %q", dirCatOnly)
		}
	})
}

func TestShippedOARChangedAuthorityAndRecoveryWitnesses(t *testing.T) {
	rs, pipeline, _, bp := effectiveOARBlockPlane(t)

	rejectFix := func(tool, code string, reach toolreach.Request, data map[string]any) string {
		ctx := toolreach.WithRequest(t.Context(), reach)
		err := bp.RejectObservation(ctx, tool, "coordinator", nil, &toolrejection.ToolReject{
			Code: code, Data: data,
		})
		ref, ok := guidance.RefusalFromError(err)
		if !ok || ref == nil || ref.Code() != code {
			t.Fatalf("RejectObservation(%s) = %v, want %s", code, err, code)
		}
		return ref.Copy["fix"]
	}

	t.Run("changed_recovery_authority_and_multi_tool_selector", func(t *testing.T) {
		handleData := map[string]any{"handle": "rv-missing"}
		withRenderView := rejectFix("view_image", "RENDER_HANDLE_NOT_FOUND",
			reachWithProgramsAndSchemas([]string{"view_image", "render_view"}, nil, nil), handleData)
		if !strings.Contains(withRenderView, "Call render_view") {
			t.Fatalf("expected render_view advice when render_view is callable: %q", withRenderView)
		}
		withoutRenderView := rejectFix("view_image", "RENDER_HANDLE_NOT_FOUND",
			reachWithProgramsAndSchemas([]string{"view_image"}, nil, nil), handleData)
		if strings.Contains(withoutRenderView, "Call render_view") || !strings.Contains(withoutRenderView, "Check the handle name or inspect an existing image file path") {
			t.Fatalf("view_image caller without render_view must not be told to call render_view: %q", withoutRenderView)
		}

		measureData := map[string]any{"selector": "#bad>>"}
		initialAuthority := rejectFix("measure_page", "MEASURE_SELECTOR_INVALID",
			reachWithProgramsAndSchemas([]string{"measure_page", "capture_page"}, nil, nil), measureData)
		if !strings.Contains(initialAuthority, "retry measure_page") {
			t.Fatalf("expected measure_page retry when offered: %q", initialAuthority)
		}
		changedAuthority := rejectFix("measure_page", "MEASURE_SELECTOR_INVALID",
			reachWithProgramsAndSchemas([]string{"capture_page"}, nil, nil), measureData)
		if strings.Contains(changedAuthority, "measure_page") || !strings.Contains(changedAuthority, "Correct the selector syntax before measuring again") {
			t.Fatalf("changed recovery authority without measure_page leaked retry: %q", changedAuthority)
		}
	})

	t.Run("credential_schema_enum_worker_url_command_not_argv_and_spec_posture", func(t *testing.T) {
		secretAskSchema := map[string]map[string]any{
			"ask_user": {
				"properties": map[string]any{
					"response_type": map[string]any{"type": "string", "enum": []string{"text", "choice", "secret"}},
				},
			},
		}
		fullReach := reachWithProgramsAndSchemas([]string{"command", "ask_user", "fetch_url", "secret_generate"}, []string{"external", "git config"}, secretAskSchema)
		nonSecretAskSchema := map[string]map[string]any{
			"ask_user": {
				"properties": map[string]any{
					"response_type": map[string]any{"type": "string", "enum": []string{"text", "choice"}},
				},
			},
		}
		prunedAskReach := reachWithProgramsAndSchemas([]string{"ask_user", "secret_generate"}, nil, nonSecretAskSchema)

		evalCred := func(mode, fp string, reach toolreach.Request) string {
			ctx := curationctx.WithSession(toolreach.WithRequest(t.Context(), reach), curationctx.Session{Mode: mode})
			gc := oar.NewGuardContext()
			gc.Session.SessionID, gc.Session.SessionMode = "cred-"+fp, mode
			gc.ObservationData = map[string]any{"assignment": "API_TOKEN"}
			gc.Published = map[string]any{
				"paintedwolf.assignment": "API_TOKEN", "paintedwolf.credential_ignored": false,
				"paintedwolf.credential_harvested": false, "paintedwolf.listed": false,
				"paintedwolf.credential_length": 24, "paintedwolf.credential_distinct_words": 4,
				"paintedwolf.credential_longest_run": 24, "paintedwolf.credential_run_classes": 1,
				"paintedwolf.credential_fingerprint": fp,
			}
			tools.RegisterRecoveryFacts(ctx, gc)
			res, err := pipeline.EvaluateBlock(ctx, oar.AnchorCredentialAssignment, gc)
			contractcheck.FailErr(t, "EvaluateBlock CREDENTIAL_LITERAL", err)
			return res.Decision.Copy["fix"]
		}
		if got := evalCred("agent", "fp-full", fullReach); !strings.Contains(got, "ask_user") || !strings.Contains(got, "secret_generate") {
			t.Fatalf("expected ask_user + secret_generate on full agent reach: %q", got)
		}
		if got := evalCred("agent", "fp-enum-pruned", prunedAskReach); strings.Contains(got, "ask_user") || !strings.Contains(got, "secret_generate") {
			t.Fatalf("projected ask_user schema without 'secret' enum must withhold ask_user: %q", got)
		}
		if got := evalCred("ask", "fp-ask", fullReach); strings.Contains(got, "ask_user") {
			t.Fatalf("ask mode must withhold secret ask_user: %q", got)
		}

		loadableCmd := toolreach.Freeze([]string{"request_tools"}, toolsurface.Compile([]string{"request_tools"}, []string{"command"})).WithPrograms(func(string) bool { return true })
		noCmd := reachWithProgramsAndSchemas([]string{"ask_user"}, nil, nil)
		argvData := map[string]any{"command": "ls | head", "shell_metacharacters": true}
		if got := rejectFix("terminal_open", "COMMAND_NOT_ARGV", loadableCmd, argvData); !strings.Contains(got, `request_tools(need="command")`) {
			t.Fatalf("COMMAND_NOT_ARGV loadable command missing request_tools: %q", got)
		}
		if got := rejectFix("terminal_open", "COMMAND_NOT_ARGV", noCmd, argvData); strings.Contains(got, "request_tools") || !strings.Contains(got, "not offered on this request") {
			t.Fatalf("COMMAND_NOT_ARGV without command gave false promise: %q", got)
		}
		readOnlyCmd := reachWithProgramsAndSchemas([]string{"command"}, []string{"sed"}, nil)
		if got := rejectFix("terminal_send", "REMOTE_PACKAGE_EXECUTION_REQUIRES_FRESH_BOUNDARY", readOnlyCmd, map[string]any{"command": "npx vite"}); strings.Contains(got, "`command(") {
			t.Fatalf("REMOTE_PACKAGE_EXECUTION_REQUIRES_FRESH_BOUNDARY must not offer command when external is unreachable: %q", got)
		}
		for _, specCode := range []string{"SPEC_POSTURE_STATE_FORBIDDEN", "SPEC_POSTURE_HANDOFF_FORBIDDEN", "SPEC_POSTURE_STUB_REQUIRED", "SPEC_POSTURE_UNRESOLVED"} {
			rule, ok := rs.Get(specCode)
			if !ok || strings.Contains(rule.Copy.Fix, "command redirect") || strings.Contains(rule.Copy.Instead, "command redirect") {
				t.Fatalf("%s invalid spec-posture copy: %+v", specCode, rule)
			}
		}
		_ = api.SessionPostureSpec
	})
}
