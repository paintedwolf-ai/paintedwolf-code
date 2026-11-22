package contract

import (
	"archive/tar"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func evalCredLiteralFix(t *testing.T, pipeline *oar.GuardPipeline, mode, fp string, reach toolreach.Request) string {
	t.Helper()
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

func TestShippedOARLoadableNarrowedAndLoaderOnlyWitnesses(t *testing.T) {
	_, pipeline, _, bp := effectiveOARBlockPlane(t)

	rejectFix := func(tool, code string, reach toolreach.Request) string {
		ctx := toolreach.WithRequest(t.Context(), reach)
		err := bp.RejectObservation(ctx, tool, "coordinator", nil, &toolrejection.ToolReject{
			Code: code, Data: map[string]any{},
		})
		ref, ok := guidance.RefusalFromError(err)
		if !ok || ref == nil || ref.Code() != code {
			t.Fatalf("RejectObservation(%s) = %v, want %s", code, err, code)
		}
		return ref.Copy["fix"]
	}

	loadableCmdPlan := toolsurface.Compile([]string{toolreach.LoaderTool}, []string{"command"})
	loadableBase := toolreach.Freeze([]string{toolreach.LoaderTool}, loadableCmdPlan).
		WithPrograms(func(name string) bool { return name == "external" })

	narrowedSchema := map[string]map[string]any{
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
	narrowedFix := rejectFix("process_signal", "PROCESS_UNSUPPORTED", loadableBase.WithSchemas(narrowedSchema))
	if !strings.Contains(narrowedFix, "`command(") ||
		!strings.Contains(narrowedFix, `request_tools(need="command")`) ||
		!strings.Contains(narrowedFix, "capability_request.process_control") ||
		strings.Contains(narrowedFix, "host_execution") {
		t.Fatalf("loadable narrowed schema must advertise only process_control with loader step: %q", narrowedFix)
	}

	prunedCmdSchema := map[string]map[string]any{
		"command": toolschema.PruneCommandReadOnlySchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string"},
				"capability_request": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"process_control": map[string]any{"type": "object"},
						"host_execution":  map[string]any{"type": "object"},
					},
				},
			},
		}),
	}
	prunedLoadableFix := rejectFix("process_signal", "PROCESS_UNSUPPORTED", loadableBase.WithSchemas(prunedCmdSchema))
	if strings.Contains(prunedLoadableFix, "`command(") || strings.Contains(prunedLoadableFix, "request_tools") ||
		strings.Contains(prunedLoadableFix, "capability_request") {
		t.Fatalf("loadable read-only pruned command schema must withhold command and process fields: %q", prunedLoadableFix)
	}

	loaderOnlyFix := rejectFix("process_signal", "PROCESS_UNSUPPORTED", loadableBase)
	if !strings.Contains(loaderOnlyFix, `request_tools(need="command")`) ||
		strings.Contains(loaderOnlyFix, "process_control") ||
		strings.Contains(loaderOnlyFix, "host_execution") {
		t.Fatalf("loadable command without projected schema must retain loader instruction and defer field claims: %q", loaderOnlyFix)
	}

	loadableAskPlan := toolsurface.Compile([]string{toolreach.LoaderTool, "secret_generate"}, []string{"ask_user"})
	loadableAskBase := toolreach.Freeze([]string{toolreach.LoaderTool, "secret_generate"}, loadableAskPlan)
	narrowedAskSchema := map[string]map[string]any{
		"ask_user": {
			"properties": map[string]any{
				"response_type": map[string]any{"type": "string", "enum": []string{"text", "choice"}},
			},
		},
	}
	if got := evalCredLiteralFix(t, pipeline, "agent", "fp-loadable-narrowed", loadableAskBase.WithSchemas(narrowedAskSchema)); strings.Contains(got, "ask_user") {
		t.Fatalf("loadable ask_user with narrowed non-secret enum must withhold ask_user: %q", got)
	}
	loaderOnlyCred := evalCredLiteralFix(t, pipeline, "agent", "fp-loadable-no-schema", loadableAskBase)
	if !strings.Contains(loaderOnlyCred, `request_tools(need="ask_user")`) || strings.Contains(loaderOnlyCred, `response_type="secret"`) {
		t.Fatalf("loadable ask_user without schema must retain loader instruction and defer secret enum claim: %q", loaderOnlyCred)
	}
}

func TestShippedOARMissingSchemaAndDisallowedEmptyObjectWitnesses(t *testing.T) {
	_, pipeline, _, bp := effectiveOARBlockPlane(t)

	callableMissingCmdSchema := toolreach.Offered([]string{"command"}).
		WithPrograms(func(name string) bool { return name == "external" })
	err := bp.RejectObservation(toolreach.WithRequest(t.Context(), callableMissingCmdSchema), "process_signal", "coordinator", nil, &toolrejection.ToolReject{
		Code: "PROCESS_UNSUPPORTED", Data: map[string]any{},
	})
	ref, ok := guidance.RefusalFromError(err)
	if !ok || ref == nil {
		t.Fatalf("RejectObservation PROCESS_UNSUPPORTED: %v", err)
	}
	missingCmdFix := ref.Copy["fix"]
	if strings.Contains(missingCmdFix, "`command(") || strings.Contains(missingCmdFix, "capability_request") ||
		!strings.Contains(missingCmdFix, "cannot run host process commands") {
		t.Fatalf("callable command with missing schema projection must fail closed: %q", missingCmdFix)
	}

	callableMissingAskSchema := toolreach.Offered([]string{"ask_user", "secret_generate"})
	if got := evalCredLiteralFix(t, pipeline, "agent", "fp-callable-missing-schema", callableMissingAskSchema); strings.Contains(got, "`ask_user") {
		t.Fatalf("callable ask_user with missing schema projection must fail closed: %q", got)
	}

	readOnlySchemas := map[string]map[string]any{
		"command": toolschema.PruneCommandReadOnlySchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command":            map[string]any{"type": "string"},
				"capability_request": map[string]any{"type": "object", "properties": map[string]any{"process_control": map[string]any{"type": "object"}}},
			},
		}),
	}
	emptyNestedCall := toolreach.Call{
		Tool: "command", Partial: true,
		Args: map[string]any{"command": "ps", "capability_request": map[string]any{}},
	}
	if _, ok := toolreach.Offered([]string{"command"}).WithSchemas(readOnlySchemas).Sequence(emptyNestedCall); ok {
		t.Fatal("callable Sequence admitted disallowed empty nested capability_request object")
	}
	loadablePlan := toolsurface.Compile([]string{toolreach.LoaderTool}, []string{"command"})
	if _, ok := toolreach.Freeze([]string{toolreach.LoaderTool}, loadablePlan).WithSchemas(readOnlySchemas).Sequence(emptyNestedCall); ok {
		t.Fatal("loadable Sequence admitted disallowed empty nested capability_request object")
	}
}

func TestShippedOARProductionCommandModeAndGitConfigWitnesses(t *testing.T) {
	_, _, _, bp := effectiveOARBlockPlane(t)

	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "LoadToolProfiles", err)
	readOnlyEffects, _ := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
	profiles = append(profiles, sandbox.ToolProfile{
		ID:             "git_without_config_fixture",
		DenyTools:      []string{"git config"},
		CommandEffects: readOnlyEffects,
	})
	boundary := sandbox.NewBoundary(sandbox.Config{}, profiles)
	executor := toolexecution.NewExecutor(
		toolexecution.NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(boundary), nil),
		tools.NewDefaultRegistry(),
		"implement",
	)

	productionReach := func(profileID, surfaceID string) toolreach.Request {
		filter := platform.ToolFilter{ProfileID: profileID, SurfaceID: surfaceID}
		table, err := executor.Metadata.ProgramTable(t.Context(), filter)
		contractcheck.FailErr(t, "ProgramTable", err)
		return toolreach.Offered([]string{"command"}).WithProgramTable(table)
	}

	rejectRef := func(tool, code string, reach toolreach.Request, data map[string]any) *guidance.Refusal {
		ctx := toolreach.WithRequest(t.Context(), reach)
		err := bp.RejectObservation(ctx, tool, "coordinator", nil, &toolrejection.ToolReject{
			Code: code, Data: data,
		})
		ref, ok := guidance.RefusalFromError(err)
		if !ok || ref == nil || ref.Code() != code {
			t.Fatalf("RejectObservation(%s) = %v, want %s", code, err, code)
		}
		return ref
	}

	extractData := map[string]any{"path": "bundle.7z", "format": "7z"}
	for _, tc := range []struct {
		name, profile, surface string
		wantExtract            bool
	}{
		{"worker_explore_readonly", "explore_readonly", "", false},
		{"coordinator_observe_investigate", "coordinator", "observe_investigate", false},
		{"worker_implement", "implement", "", true},
		{"coordinator_implement_investigate", "coordinator", "implement_investigate", true},
	} {
		fix := rejectRef("extract_archive", "EXTRACT_FORMAT_UNSUPPORTED", productionReach(tc.profile, tc.surface), extractData).Copy["fix"]
		if !strings.Contains(fix, "`unzip -p ARCHIVE.zip MEMBER`") || !strings.Contains(fix, "`tar -t -f ARCHIVE.tar`") {
			t.Fatalf("%s: expected admitted read archive forms unzip -p and tar -t -f, got %q", tc.name, fix)
		}
		if strings.Contains(fix, "`tar -x -f ARCHIVE.tar`") != tc.wantExtract {
			t.Fatalf("%s: tar -x -f presence = %v, want %v: %q", tc.name, !tc.wantExtract, tc.wantExtract, fix)
		}
	}

	gitData := map[string]any{"path": ".git/config", "class": "config", "detail": "denied"}
	noCfgReach := productionReach("git_without_config_fixture", "")
	if !noCfgReach.Program("git") || !noCfgReach.ProgramInvocation("git", "status") || noCfgReach.ProgramInvocation("git", "config", "--list") {
		t.Fatal("git_without_config_fixture must admit generic git/git status while denying git config")
	}
	noCfgRef := rejectRef("edit", "GIT_INTERNALS_WRITE_DENIED", noCfgReach, gitData)
	if strings.Contains(noCfgRef.Copy["fix"], "git config") || strings.Contains(noCfgRef.Copy["instead"], "git config") {
		t.Fatalf("generic git reach with git config denied leaked git config remedy: %+v", noCfgRef.Copy)
	}
	roRef := rejectRef("edit", "GIT_INTERNALS_WRITE_DENIED", productionReach("explore_readonly", ""), gitData)
	if !strings.Contains(roRef.Copy["fix"], "git config --list") || !strings.Contains(roRef.Copy["instead"], "git config --get") {
		t.Fatalf("admitted read-only git config should remain visible: %+v", roRef.Copy)
	}

	sedRule := &oar.Rule{
		ID: "SED_WITNESS",
		NextSteps: &oar.NextSteps{
			Steps: []oar.NextStep{
				{Program: []string{"sed", "p"}, Complete: true, Say: "Stream with {{ call }}.", Otherwise: "No stream."},
				{Program: []string{"sed", "w out"}, Complete: true, Say: "Write with {{ call }}.", Otherwise: "No write."},
			},
		},
	}
	for _, tc := range []struct {
		name, profile, surface string
		wantWrite              bool
	}{
		{"readonly_worker", "explore_readonly", "", false},
		{"readonly_coordinator", "coordinator", "observe_investigate", false},
		{"writable_worker", "implement", "", true},
		{"writable_coordinator", "coordinator", "implement_investigate", true},
	} {
		reach := productionReach(tc.profile, tc.surface)
		if !reach.ProgramInvocation("sed", "p") {
			t.Fatalf("%s: expected sed p admitted", tc.name)
		}
		if reach.ProgramInvocation("sed", "w out") != tc.wantWrite {
			t.Fatalf("%s: sed 'w out' admitted = %v, want %v", tc.name, !tc.wantWrite, tc.wantWrite)
		}
		if reach.ProgramMode("sed", "unknown-mode") {
			t.Fatalf("%s: unknown sed mode must fail closed", tc.name)
		}
		gc := oar.NewGuardContext()
		gc.Reach = reach
		resolved := oar.ResolveNextSteps(sedRule, gc)
		if !strings.Contains(resolved.Text, "`command(command=\"sed p\")`") {
			t.Fatalf("%s: missing sed p step: %q", tc.name, resolved.Text)
		}
		if strings.Contains(resolved.Text, "sed 'w out'") != tc.wantWrite {
			t.Fatalf("%s: sed 'w out' step presence = %v, want %v: %q", tc.name, !tc.wantWrite, tc.wantWrite, resolved.Text)
		}
	}
}

func TestShippedOARExtractFormatUnsupportedProducerToRuleWitnesses(t *testing.T) {
	_, _, _, bp := effectiveOARBlockPlane(t)
	tmpDir := t.TempDir()
	contractcheck.FailErr(t, "mkdir vendor", os.MkdirAll(filepath.Join(tmpDir, "vendor"), 0o755))
	contractcheck.FailErr(t, "write sdk.7z", os.WriteFile(filepath.Join(tmpDir, "vendor", "sdk.7z"), []byte("7z"), 0o644))

	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	body := "package sdk\n"
	contractcheck.FailErr(t, "tar header", tw.WriteHeader(&tar.Header{Name: "sdk.go", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}))
	_, writeErr := tw.Write([]byte(body))
	contractcheck.FailErr(t, "tar body", writeErr)
	contractcheck.FailErr(t, "tar close", tw.Close())
	contractcheck.FailErr(t, "write sdk.tar", os.WriteFile(filepath.Join(tmpDir, "vendor", "sdk.tar"), tarBuf.Bytes(), 0o644))

	extractor := &native.ExtractArchiveTool{Boundary: contractcheck.ProdToolBoundary(t)}
	tctx := tools.ToolContext{
		Identity: tools.InvocationIdentity{ProjectID: "project", SessionID: "session", Agent: toolprofiles.DefaultToolProfileID},
		Source: tools.InvocationSource{
			Roots:               []projectroot.RootRef{{ID: "root", Path: tmpDir, IsPrimary: true}},
			ActiveRootID:        "root",
			SourceWorkspaceKind: api.SourceWorkspaceKindProject,
		},
	}
	reachCtx := toolreach.WithRequest(t.Context(), reachWithProgramsAndSchemas([]string{"command"}, []string{"unzip", "tar"}, nil))

	// 1. Supported vendor/sdk.tar extracts without rejection.
	res, err := extractor.Extract(t.Context(), native.ExtractInput{Archive: "vendor/sdk.tar", Dest: "extracted", Overwrite: true}, tctx)
	contractcheck.FailErr(t, "extract vendor/sdk.tar", err)
	if res.Entries != 1 {
		t.Fatalf("vendor/sdk.tar entries = %d, want 1", res.Entries)
	}

	// 2. Unsupported vendor/sdk.7z producer -> EXTRACT_FORMAT_UNSUPPORTED rule render.
	_, err7z := extractor.Run(t.Context(), map[string]any{"path": "vendor/sdk.7z", "dest": "out7z"}, tctx)
	var reject7z *toolrejection.ToolReject
	if !errors.As(err7z, &reject7z) || reject7z.Code != "EXTRACT_FORMAT_UNSUPPORTED" {
		t.Fatalf("vendor/sdk.7z expected EXTRACT_FORMAT_UNSUPPORTED, got %v", err7z)
	}
	obs7z := bp.RejectObservation(reachCtx, "extract_archive", "implement", nil, reject7z)
	ref7z, ok := guidance.RefusalFromError(obs7z)
	if !ok || ref7z == nil {
		t.Fatalf("RejectObservation(sdk.7z) = %v", obs7z)
	}
	wantSuffixList := ".zip, .tar, .tar.gz, .tgz, .tar.bz2, .tbz2, .tar.xz, .txz, .tar.zst, .tzst"
	if !strings.Contains(ref7z.Copy["what"], "vendor/sdk.7z") || !strings.Contains(ref7z.Copy["what"], ".7z") ||
		!strings.Contains(ref7z.Copy["cause"], "unsupported_suffix") || !strings.Contains(ref7z.Copy["cause"], wantSuffixList) ||
		!strings.Contains(ref7z.Copy["fix"], wantSuffixList) {
		t.Fatalf("sdk.7z producer-to-rule copy = %+v", ref7z.Copy)
	}

	// 3. Invalid explicit format on valid suffix (vendor/sdk.tar with Format="7z") -> EXTRACT_FORMAT_UNSUPPORTED rule render.
	_, errBadFmt := extractor.Extract(t.Context(), native.ExtractInput{Archive: "vendor/sdk.tar", Dest: "outfmt", Format: "7z"}, tctx)
	var rejectFmt *toolrejection.ToolReject
	if !errors.As(errBadFmt, &rejectFmt) || rejectFmt.Code != "EXTRACT_FORMAT_UNSUPPORTED" {
		t.Fatalf("explicit format expected EXTRACT_FORMAT_UNSUPPORTED, got %v", errBadFmt)
	}
	obsFmt := bp.RejectObservation(reachCtx, "extract_archive", "implement", nil, rejectFmt)
	refFmt, ok := guidance.RefusalFromError(obsFmt)
	if !ok || refFmt == nil {
		t.Fatalf("RejectObservation(explicit format) = %v", obsFmt)
	}
	wantFormatList := "zip, tar, tar.gz, tar.bz2, tar.xz, tar.zst"
	if !strings.Contains(refFmt.Copy["what"], "vendor/sdk.tar") || !strings.Contains(refFmt.Copy["what"], "7z") ||
		!strings.Contains(refFmt.Copy["cause"], "invalid_format") || !strings.Contains(refFmt.Copy["cause"], wantFormatList) ||
		!strings.Contains(refFmt.Copy["fix"], wantFormatList) {
		t.Fatalf("explicit format producer-to-rule copy = %+v", refFmt.Copy)
	}

	// 4. Generic paintedwolf.allowed string fact remains intact for other rules (JQ_FORMAT_INVALID).
	jqObs := bp.RejectObservation(reachCtx, "jq", "implement", nil, &toolrejection.ToolReject{
		Code: "JQ_FORMAT_INVALID",
		Data: map[string]any{"format": "xml", "allowed": "[auto json yaml toml]"},
	})
	jqRef, ok := guidance.RefusalFromError(jqObs)
	if !ok || jqRef == nil || !strings.Contains(jqRef.Copy["what"], "[auto json yaml toml]") {
		t.Fatalf("JQ_FORMAT_INVALID generic paintedwolf.allowed broken: %v / %+v", jqObs, jqRef)
	}
}
