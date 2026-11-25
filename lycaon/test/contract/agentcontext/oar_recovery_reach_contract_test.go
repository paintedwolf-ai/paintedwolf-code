package contract

import (
	"archive/tar"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
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

// A recovery call carrying an empty object the projected schema disallows is
// never sequenced, whether command is callable or loadable.
func TestRecoverySequenceRejectsDisallowedEmptyObject(t *testing.T) {
	readOnlySchemas := map[string]map[string]any{
		"command": toolschema.PruneCommandReadOnlySchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command":            map[string]any{"type": "string"},
				"capability_request": map[string]any{"type": "object", "properties": map[string]any{"process_control": map[string]any{"type": "object"}}},
			},
		}),
	}
	call := toolreach.Call{
		Tool: "command", Partial: true,
		Args: map[string]any{"command": "ps", "capability_request": map[string]any{}},
	}
	if _, ok := toolreach.Offered([]string{"command"}).WithSchemas(readOnlySchemas).Sequence(call); ok {
		t.Error("callable command sequenced a disallowed empty capability_request")
	}
	plan := toolsurface.Compile([]string{toolreach.LoaderTool}, []string{"command"})
	if _, ok := toolreach.Freeze([]string{toolreach.LoaderTool}, plan).WithSchemas(readOnlySchemas).Sequence(call); ok {
		t.Error("loadable command sequenced a disallowed empty capability_request")
	}
}

// Production program tables admit archive and stream forms by profile
// mutation capability, and a profile's denied subcommand stays denied while
// the program's other invocations remain admitted.
func TestProductionProgramTablesFollowProfileMutation(t *testing.T) {
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "LoadToolProfiles", err)
	readOnlyEffects, err := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
	contractcheck.FailErr(t, "NewCommandEffects", err)
	profiles = append(profiles, sandbox.ToolProfile{
		ID:             "git_without_config",
		DenyTools:      []string{"git config"},
		CommandEffects: readOnlyEffects,
	})
	executor := toolexecution.NewExecutor(
		toolexecution.NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(sandbox.NewBoundary(sandbox.Config{}, profiles)), nil),
		tools.NewDefaultRegistry(),
		"implement",
	)
	reach := func(profileID, surfaceID string) toolreach.Request {
		table, err := executor.Metadata.ProgramTable(t.Context(), platform.ToolFilter{ProfileID: profileID, SurfaceID: surfaceID})
		contractcheck.FailErr(t, "ProgramTable "+profileID, err)
		return toolreach.Offered([]string{"command"}).WithProgramTable(table)
	}

	for _, tc := range []struct {
		profile, surface string
		mutating         bool
	}{
		{"explore_readonly", "", false},
		{"coordinator", "observe_investigate", false},
		{"implement", "", true},
		{"coordinator", "implement_investigate", true},
	} {
		r := reach(tc.profile, tc.surface)
		if !r.ProgramInvocation("sed", "p") || !r.ProgramInvocation("tar", "-t", "-f", "a.tar") || !r.ProgramInvocation("unzip", "-p", "a.zip", "m") {
			t.Errorf("%s/%s: read forms of sed, tar or unzip not admitted", tc.profile, tc.surface)
		}
		if got := r.ProgramInvocation("sed", "w out"); got != tc.mutating {
			t.Errorf("%s/%s: sed write admitted=%t, want %t", tc.profile, tc.surface, got, tc.mutating)
		}
		if got := r.ProgramInvocation("tar", "-x", "-f", "a.tar"); got != tc.mutating {
			t.Errorf("%s/%s: tar extract admitted=%t, want %t", tc.profile, tc.surface, got, tc.mutating)
		}
		if r.ProgramMode("sed", "unknown-mode") {
			t.Errorf("%s/%s: unknown sed mode admitted", tc.profile, tc.surface)
		}
	}

	noConfig := reach("git_without_config", "")
	if !noConfig.ProgramInvocation("git", "status") || noConfig.ProgramInvocation("git", "config", "--list") {
		t.Error("denying git config must keep git status admitted and deny git config")
	}
}

// The archive extractor's rejection carries the allowed formats as facts and
// fires EXTRACT_FORMAT_UNSUPPORTED, which delivers those values.
func TestArchiveFormatRejectionCarriesProducerFacts(t *testing.T) {
	_, _, _, bp := effectiveOARBlockPlane(t)
	dir := t.TempDir()
	contractcheck.FailErr(t, "mkdir vendor", os.MkdirAll(filepath.Join(dir, "vendor"), 0o755))
	contractcheck.FailErr(t, "write sdk.7z", os.WriteFile(filepath.Join(dir, "vendor", "sdk.7z"), []byte("7z"), 0o644))
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	body := "package sdk\n"
	contractcheck.FailErr(t, "tar header", tw.WriteHeader(&tar.Header{Name: "sdk.go", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}))
	_, err := tw.Write([]byte(body))
	contractcheck.FailErr(t, "tar body", err)
	contractcheck.FailErr(t, "tar close", tw.Close())
	contractcheck.FailErr(t, "write sdk.tar", os.WriteFile(filepath.Join(dir, "vendor", "sdk.tar"), tarBuf.Bytes(), 0o644))

	extractor := &native.ExtractArchiveTool{Boundary: contractcheck.ProdToolBoundary(t)}
	tctx := tools.ToolContext{
		Identity: tools.InvocationIdentity{ProjectID: "project", SessionID: "session", Agent: toolprofiles.DefaultToolProfileID},
		Source: tools.InvocationSource{
			Roots:               []projectroot.RootRef{{ID: "root", Path: dir, IsPrimary: true}},
			ActiveRootID:        "root",
			SourceWorkspaceKind: api.SourceWorkspaceKindProject,
		},
	}
	reachCtx := toolreach.WithRequest(t.Context(), toolreach.Offered([]string{"command"}).WithPrograms(func(name string) bool {
		return name == "unzip" || name == "tar"
	}).WithSchemas(map[string]map[string]any{"command": {"properties": map[string]any{"command": map[string]any{"type": "string"}}}}))

	res, err := extractor.Extract(t.Context(), native.ExtractInput{Archive: "vendor/sdk.tar", Dest: "extracted", Overwrite: true}, tctx)
	contractcheck.FailErr(t, "extract vendor/sdk.tar", err)
	if res.Entries != 1 {
		t.Fatalf("vendor/sdk.tar entries = %d, want 1", res.Entries)
	}

	_, suffixErr := extractor.Run(t.Context(), map[string]any{"path": "vendor/sdk.7z", "dest": "out7z"}, tctx)
	_, formatErr := extractor.Extract(t.Context(), native.ExtractInput{Archive: "vendor/sdk.tar", Dest: "outfmt", Format: "7z"}, tctx)
	for _, tc := range []struct {
		err             error
		cause, rejected string
	}{
		{suffixErr, "unsupported_suffix", ".7z"},
		{formatErr, "invalid_format", "7z"},
	} {
		var reject *toolrejection.ToolReject
		if !errors.As(tc.err, &reject) || reject.Code != "EXTRACT_FORMAT_UNSUPPORTED" {
			t.Fatalf("%s: rejection = %v, want EXTRACT_FORMAT_UNSUPPORTED", tc.cause, tc.err)
		}
		if reject.Data["extract_format_cause"] != tc.cause || reject.Data["extract_rejected_value"] != tc.rejected {
			t.Errorf("%s: facts = %v", tc.cause, reject.Data)
		}
		allowed, ok := reject.Data["extract_allowed"].([]string)
		if !ok || len(allowed) == 0 {
			t.Fatalf("%s: extract_allowed = %#v", tc.cause, reject.Data["extract_allowed"])
		}
		ref, ok := guidance.RefusalFromError(bp.RejectObservation(reachCtx, "extract_archive", "implement", nil, reject))
		if !ok || ref.Code() != "EXTRACT_FORMAT_UNSUPPORTED" {
			t.Fatalf("%s: refusal = %+v", tc.cause, ref)
		}
		if missing := slices.DeleteFunc(slices.Clone(allowed), func(v string) bool { return strings.Contains(ref.Copy["fix"], v) }); len(missing) > 0 {
			t.Errorf("%s: allowed values %v not delivered in the fix", tc.cause, missing)
		}
	}
}
