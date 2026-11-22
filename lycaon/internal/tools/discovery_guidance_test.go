package tools

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/toolsurface"
)

func TestDiscoveryGuidanceFollowsAvailableContinuation(t *testing.T) {
	wireFixtureGuidanceRenderer(t)
	entries := make([]turnload.DiscoveryEntry, 20)
	for i := range entries {
		entries[i] = turnload.DiscoveryEntry{Name: fmt.Sprintf("skill_%02d", i), Description: strings.Repeat("界", turnload.CardMaxRunes)}
	}
	first, err := DiscoveryPage(t.Context(), ToolContext{}, "skills", "review code", "", "ranking_unavailable", turnload.RankingDisabled, entries)
	testutil.FailErr(t, "discover first page", err)
	if first.NextNeed == "" || !strings.Contains(first.Instruction, "next_need unchanged") || !strings.Contains(first.Instruction, "same catalog") {
		t.Fatalf("first page guidance: %+v", first)
	}
	need, cursor, err := turnload.ParseDiscoveryNeed(first.NextNeed)
	testutil.FailErr(t, "parse page cursor", err)
	last, err := DiscoveryPage(t.Context(), ToolContext{}, "skills", need, cursor, "catalog", "", entries)
	testutil.FailErr(t, "discover final page", err)
	if last.NextNeed != "" || strings.Contains(last.Instruction, "next_need") || !strings.Contains(last.Instruction, "final catalog page") {
		t.Fatalf("final page guidance: %+v", last)
	}
	empty, err := DiscoveryPage(t.Context(), ToolContext{}, "skills", need, "", "catalog", "", nil)
	testutil.FailErr(t, "discover empty catalog", err)
	if empty.NextNeed != "" || strings.Contains(empty.Instruction, "Load") || !strings.Contains(empty.Instruction, "No entries") {
		t.Fatalf("empty catalog guidance: %+v", empty)
	}
}

func TestDiscoveryRenderingPreservesCancellation(t *testing.T) {
	wireFixtureGuidanceRenderer(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := DiscoveryPage(ctx, ToolContext{}, "skills", "review", "", "catalog", "", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("render cancellation = %v", err)
	}
}

func TestRecoveryFormFactsSupplyIdentifiersAndQuotedCallsAcrossLoadingStates(t *testing.T) {
	allFactRefs := []string{
		"paintedwolf.command_loader_call",
		"paintedwolf.uncompressed_reader_call",
		"paintedwolf.admitted_process_capability_fields",
		"paintedwolf.command_loadable_without_schema",
		"paintedwolf.admitted_stream_edit_forms",
		"paintedwolf.stream_edit_forms_loadable_without_schema",
		"paintedwolf.admitted_archive_forms",
		"paintedwolf.archive_unzip_stream_call",
		"paintedwolf.archive_tar_list_call",
		"paintedwolf.archive_tar_extract_call",
		"paintedwolf.archive_forms_loadable_without_schema",
		"paintedwolf.admitted_mutation_forms",
		"paintedwolf.mutation_forms_loadable_without_schema",
		"paintedwolf.admitted_secret_config_forms",
		"paintedwolf.secret_jq_edit_call",
		"paintedwolf.secret_external_command_admitted",
		"paintedwolf.secret_forms_loadable_without_schema",
		"paintedwolf.admitted_source_read_forms",
		"paintedwolf.source_read_forms_loadable_without_schema",
		"paintedwolf.admitted_range_read_forms",
		"paintedwolf.range_read_forms_loadable_without_schema",
		"paintedwolf.admitted_text_search_forms",
		"paintedwolf.text_search_forms_loadable_without_schema",
		"paintedwolf.admitted_file_read_forms",
		"paintedwolf.file_read_forms_loadable_without_schema",
		"paintedwolf.admitted_raw_read_forms",
		"paintedwolf.raw_read_forms_loadable_without_schema",
		"paintedwolf.admitted_large_read_forms",
		"paintedwolf.large_read_forms_loadable_without_schema",
		"paintedwolf.admitted_read_addressing_forms",
		"paintedwolf.read_symbol_call",
		"paintedwolf.read_range_call",
		"paintedwolf.read_window_calls",
		"paintedwolf.read_addressing_forms_loadable_without_schema",
		"paintedwolf.command_jq_edit_reachable",
		"paintedwolf.command_git_config_reachable",
		"paintedwolf.command_wc_reachable",
		"paintedwolf.command_tee_reachable",
		"paintedwolf.command_sed_reachable",
		"paintedwolf.command_find_reachable",
		"paintedwolf.command_grep_reachable",
		"paintedwolf.command_repo_map_reachable",
	}

	assemble := func(req toolreach.Request) map[string]any {
		gc := oar.NewGuardContext()
		RegisterRecoveryFacts(toolreach.WithRequest(t.Context(), req), gc)
		testutil.FailErr(t, "AssembleFacts", oar.AssembleFacts(gc, allFactRefs))
		return gc.ObservationData
	}

	callableReq := toolreach.Offered([]string{"command"}).
		WithPrograms(func(name string) bool {
			return name == "external" || name == "sed" || name == "awk" || name == "unzip" || name == "tar" ||
				name == "edit" || name == "jq-edit" || name == "rg" || name == "head" || name == "git config" ||
				name == "wc" || name == "tee" || name == "find" || name == "grep" || name == "repo-map" || name == "cat"
		}).
		WithSchemas(map[string]map[string]any{
			"command": {
				"properties": map[string]any{
					"command": map[string]any{"type": "string"},
					"capability_request": map[string]any{
						"properties": map[string]any{"process_control": map[string]any{"type": "object"}},
					},
				},
			},
		})
	gotCallable := assemble(callableReq)
	if gotCallable["command_loader_call"] != "" || gotCallable["uncompressed_reader_call"] != "" {
		t.Fatalf("callable loader/reader = %v / %v", gotCallable["command_loader_call"], gotCallable["uncompressed_reader_call"])
	}
	if !slices.Equal(gotCallable["admitted_process_capability_fields"].([]string), []string{"capability_request.process_control"}) {
		t.Fatalf("admitted_process_capability_fields = %v", gotCallable["admitted_process_capability_fields"])
	}
	if !slices.Equal(gotCallable["admitted_stream_edit_forms"].([]string), []string{"sed", "awk"}) {
		t.Fatalf("admitted_stream_edit_forms = %v", gotCallable["admitted_stream_edit_forms"])
	}
	if !slices.Equal(gotCallable["admitted_secret_config_forms"].([]string), []string{"edit --path FILE --old-string OLD --new-string 'KEY=<reference>'"}) ||
		gotCallable["secret_external_command_admitted"] != true {
		t.Fatalf("secret facts = %v / %v", gotCallable["admitted_secret_config_forms"], gotCallable["secret_external_command_admitted"])
	}
	if gotCallable["archive_unzip_stream_call"] != "unzip -p ARCHIVE.zip MEMBER" ||
		gotCallable["archive_tar_list_call"] != "tar -t -f ARCHIVE.tar" ||
		gotCallable["archive_tar_extract_call"] != "" ||
		gotCallable["archive_forms_loadable_without_schema"] != false {
		t.Fatalf("archive calls = %v / %v / %v / %v", gotCallable["archive_unzip_stream_call"], gotCallable["archive_tar_list_call"], gotCallable["archive_tar_extract_call"], gotCallable["archive_forms_loadable_without_schema"])
	}

	loadableNoSchemaReq := toolreach.Freeze([]string{"request_tools"}, toolsurface.Compile([]string{"request_tools"}, []string{"command"})).
		WithPrograms(func(name string) bool { return name == "external" || name == "rg" || name == "unzip" })
	gotLoaderOnly := assemble(loadableNoSchemaReq)
	if gotLoaderOnly["command_loader_call"] != "`request_tools(need=\"command\")`" ||
		gotLoaderOnly["command_loadable_without_schema"] != true ||
		gotLoaderOnly["archive_forms_loadable_without_schema"] != true ||
		gotLoaderOnly["read_addressing_forms_loadable_without_schema"] != true ||
		gotLoaderOnly["archive_unzip_stream_call"] != "" ||
		gotLoaderOnly["read_symbol_call"] != "" ||
		gotLoaderOnly["secret_external_command_admitted"] != false {
		t.Fatalf("loader-only facts leaked concrete argv or dropped loader: %+v", gotLoaderOnly)
	}

	loadableOmittedFieldReq := toolreach.Freeze([]string{"request_tools", "read"}, toolsurface.Compile([]string{"request_tools", "read"}, []string{"command"})).
		WithPrograms(func(name string) bool { return name == "unzip" || name == "rg" }).
		WithSchemas(map[string]map[string]any{
			"command": {"properties": map[string]any{"cwd": map[string]any{"type": "string"}}},
			"read":    {"properties": map[string]any{"path": map[string]any{"type": "string"}}},
		})
	gotOmitted := assemble(loadableOmittedFieldReq)
	if gotOmitted["command_loader_call"] != "`request_tools(need=\"command\")`" ||
		gotOmitted["archive_forms_loadable_without_schema"] != false ||
		len(gotOmitted["admitted_archive_forms"].([]string)) != 0 ||
		gotOmitted["archive_unzip_stream_call"] != "" ||
		gotOmitted["uncompressed_reader_call"] != "`read(…)`" {
		t.Fatalf("omitted-command-field facts = %+v", gotOmitted)
	}

	absentReq := toolreach.Freeze([]string{"read"}, toolsurface.Compile([]string{"read"}, []string{"command"})).
		WithPrograms(func(string) bool { return true })
	gotAbsent := assemble(absentReq)
	if gotAbsent["command_loader_call"] != "" || len(gotAbsent["admitted_read_addressing_forms"].([]string)) != 0 || gotAbsent["read_symbol_call"] != "" || gotAbsent["uncompressed_reader_call"] != "" {
		t.Fatalf("absent-loader facts = %+v", gotAbsent)
	}
}

func TestRegisterRecoveryFactsTaskAndSurfaceSnapshotHandles(t *testing.T) {
	refs := []string{"paintedwolf.task_callable", "paintedwolf.surface_snapshot_handles"}
	eval := func(req toolreach.Request, turnTools []string) map[string]any {
		gc := oar.NewGuardContext()
		gc.Session.TurnTools = turnTools
		RegisterRecoveryFacts(toolreach.WithRequest(t.Context(), req), gc)
		testutil.FailErr(t, "AssembleFacts", oar.AssembleFacts(gc, refs))
		return gc.ObservationData
	}

	none := eval(toolreach.Offered([]string{"recall"}), nil)
	if none["task_callable"] != false || none["surface_snapshot_handles"] != "" {
		t.Fatalf("none = %+v", none)
	}

	one := eval(toolreach.Offered([]string{"capture_page", "task"}), nil)
	if one["task_callable"] != true || one["surface_snapshot_handles"] != "page#" {
		t.Fatalf("one = %+v", one)
	}

	two := eval(toolreach.Offered([]string{"capture_page", "terminal_open"}), []string{"task"})
	if two["task_callable"] != true || two["surface_snapshot_handles"] != "page# or terminal_session#" {
		t.Fatalf("two = %+v", two)
	}

	three := eval(
		toolreach.Offered([]string{"capture_page", "command", "terminal_open"}).
			WithPrograms(func(name string) bool { return name == "external" }),
		nil,
	)
	if three["surface_snapshot_handles"] != "page#, tui# or terminal_session#" {
		t.Fatalf("three = %+v", three)
	}
}
