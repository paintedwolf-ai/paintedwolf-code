package prompts_test

import (
	"github.com/lycaon/lycaon/internal/toolcontract"

	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolschema"
)

func coordinatorSurfaceInput(t *testing.T, surfaceID string) prompts.ReaderInput {
	t.Helper()
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "LoadToolProfiles", err)
	plan, err := prompts.CompileSurfacePlan(surfaceID, 1)
	testutil.FailErr(t, "CompileSurfacePlan", err)
	return prompts.ReaderInput{
		ProfileID: prompts.CoordinatorProfileID,
		SurfaceID: surfaceID,
		Profiles:  profiles,
		Plan:      plan,
		RootCount: 1,
		HasGit:    true,
		WebSearch: true,
	}
}

func TestCoordinatorSurfaceFactsHasCommandOnInvestigate(t *testing.T) {
	reader, err := prompts.DeclaredReader(nil, toolcontract.SurfaceImplementInvestigate)
	if err != nil {
		t.Fatalf("DeclaredReader: %v", err)
	}
	vars := reader.Facts()
	if !vars["profile_has_command"].(bool) {
		t.Fatalf("profile_has_command = %v want true on implement_investigate", vars["profile_has_command"])
	}
}

func TestCoordinatorSurfaceFactsTeachesLoadedHTTPRequest(t *testing.T) {
	reader, err := prompts.DeclaredReader(nil, toolcontract.SurfaceImplementInvestigate)
	if err != nil {
		t.Fatalf("DeclaredReader: %v", err)
	}
	vars := reader.Facts()
	if vars["profile_has_fetch_url"] != false || vars["more_tools_loadable"] != true {
		t.Fatalf("investigate floor must not teach the unloaded fetch_url: has=%v loadable=%v",
			vars["profile_has_fetch_url"], vars["more_tools_loadable"])
	}
	in := coordinatorSurfaceInput(t, toolcontract.SurfaceImplementInvestigate)
	in.Loaded = map[string]bool{"fetch_url": true}
	loadedReader, err := prompts.NewReader(in)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	vars = loadedReader.Facts()
	if vars["profile_has_fetch_url"] != true {
		t.Fatalf("a loaded fetch_url must be taught: %#v", vars["profile_has_fetch_url"])
	}
	promoteReader, err := prompts.DeclaredReader(nil, "implement_overlay_promote")
	if err != nil {
		t.Fatalf("DeclaredReader: %v", err)
	}
	vars = promoteReader.Facts()
	if vars["profile_has_command"] != true {
		t.Fatalf("overlay promote carries command on the floor: has=%v", vars["profile_has_command"])
	}
}

func TestCoordinatorPromptVarsRejectUnknownSurface(t *testing.T) {
	if _, err := prompts.DeclaredReader(nil, "missing"); err == nil {
		t.Fatal("DeclaredReader accepted an unknown surface")
	}
	vars := map[string]any{}
	if err := prompts.MergeCoordinatorPromptVars("missing", prompts.ExecutionModePromptTransition{}, "", vars); err == nil {
		t.Fatal("prompt vars accepted an unknown surface")
	}
}

func TestCoordinatorSurfaceFactsListsRequestableToolsWithSummaries(t *testing.T) {
	root := configlayout.FindModuleRoot()
	schemas, err := toolschema.LoadSchemaDir(filepath.Join(root, "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	testutil.FailErr(t, "LoadSchemaDir", err)
	in := coordinatorSurfaceInput(t, toolcontract.SurfaceImplementInvestigate)
	in.Schemas = schemas
	reader, err := prompts.NewReader(in)
	testutil.FailErr(t, "NewReader", err)
	rows, err := prompts.RequestableTools(reader.Requestable(), schemas)
	testutil.FailErr(t, "RequestableTools", err)
	byName := map[string]string{}
	for _, row := range rows {
		byName[row.Name] = row.Summary
	}
	if summary, ok := byName["fetch_url"]; !ok || summary == "" {
		t.Fatalf("the investigate floor leaves fetch_url requestable with a summary: %+v", rows)
	}
	if _, ok := byName["command"]; ok {
		t.Fatalf("a floor tool is offered, not requestable: %+v", rows)
	}
	in.Loaded = map[string]bool{"fetch_url": true}
	loadedReader, err := prompts.NewReader(in)
	testutil.FailErr(t, "NewReader loaded", err)
	loadedRows, err := prompts.RequestableTools(loadedReader.Requestable(), schemas)
	testutil.FailErr(t, "RequestableTools loaded", err)
	for _, row := range loadedRows {
		if row.Name == "fetch_url" {
			t.Fatal("a loaded tool leaves the requestable roster")
		}
	}
}
