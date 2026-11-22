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
		"paintedwolf.admitted_process_capability_fields",
		"paintedwolf.command_loadable_without_schema",
		"paintedwolf.admitted_stream_edit_forms",
		"paintedwolf.admitted_archive_forms",
		"paintedwolf.archive_unzip_stream_call",
		"paintedwolf.archive_tar_list_call",
		"paintedwolf.archive_tar_extract_call",
		"paintedwolf.admitted_mutation_forms",
		"paintedwolf.admitted_secret_config_forms",
		"paintedwolf.secret_jq_edit_call",
		"paintedwolf.admitted_source_read_forms",
		"paintedwolf.admitted_range_read_forms",
		"paintedwolf.admitted_text_search_forms",
		"paintedwolf.admitted_file_read_forms",
		"paintedwolf.admitted_raw_read_forms",
		"paintedwolf.admitted_large_read_forms",
		"paintedwolf.admitted_read_addressing_forms",
		"paintedwolf.read_symbol_call",
		"paintedwolf.read_range_call",
		"paintedwolf.read_window_calls",
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
	if gotCallable["command_loader_call"] != "" {
		t.Fatalf("callable command_loader_call = %v", gotCallable["command_loader_call"])
	}
	if !slices.Equal(gotCallable["admitted_process_capability_fields"].([]string), []string{"capability_request.process_control"}) {
		t.Fatalf("admitted_process_capability_fields = %v", gotCallable["admitted_process_capability_fields"])
	}
	if !slices.Equal(gotCallable["admitted_stream_edit_forms"].([]string), []string{"sed", "awk"}) {
		t.Fatalf("admitted_stream_edit_forms = %v", gotCallable["admitted_stream_edit_forms"])
	}
	if !slices.Equal(gotCallable["admitted_secret_config_forms"].([]string), []string{"edit --path FILE --old-string OLD --new-string 'KEY=<reference>'"}) {
		t.Fatalf("admitted_secret_config_forms = %v", gotCallable["admitted_secret_config_forms"])
	}
	if gotCallable["archive_unzip_stream_call"] != "unzip -p ARCHIVE.zip MEMBER" ||
		gotCallable["archive_tar_list_call"] != "tar -t -f ARCHIVE.tar" ||
		gotCallable["archive_tar_extract_call"] != "" {
		t.Fatalf("archive calls = %v / %v / %v", gotCallable["archive_unzip_stream_call"], gotCallable["archive_tar_list_call"], gotCallable["archive_tar_extract_call"])
	}

	loadableReq := toolreach.Freeze([]string{"request_tools"}, toolsurface.Compile([]string{"request_tools"}, []string{"command"})).
		WithPrograms(func(name string) bool { return name == "external" || name == "rg" })
	gotLoadable := assemble(loadableReq)
	if gotLoadable["command_loader_call"] != "`request_tools(need=\"command\")`" ||
		gotLoadable["command_loadable_without_schema"] != true ||
		gotLoadable["read_symbol_call"] != "rg -n SYMBOL FILE" {
		t.Fatalf("loadable facts = %+v", gotLoadable)
	}

	absentReq := toolreach.Freeze([]string{"read"}, toolsurface.Compile([]string{"read"}, []string{"command"})).
		WithPrograms(func(string) bool { return true })
	gotAbsent := assemble(absentReq)
	if gotAbsent["command_loader_call"] != "" || len(gotAbsent["admitted_read_addressing_forms"].([]string)) != 0 || gotAbsent["read_symbol_call"] != "" {
		t.Fatalf("absent-loader facts = %+v", gotAbsent)
	}
}
