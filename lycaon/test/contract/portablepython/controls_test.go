package portablepython

import "testing"

func baselineCleanCase() (CaseSpec, Observation) {
	spec := CaseSpec{
		ID:            "py06-baseline-clean",
		Version:       "1.0",
		OracleVersion: OracleSchemaVersion,
		Actor:         "agent",
		Authority:     "workspace_rw",
		Program:       "pathlib.Path('out.txt').write_text('ok\\n')",
		ExpectedRoute: "portable_python",
		ExpectedSentinels: map[string]string{
			"manifest.toml": "EDITOR_DRAFT_SENTINEL",
		},
		ExpectedFiles: map[string][]byte{
			"out.txt": []byte("ok\n"),
		},
		ExpectedOutcome: "completed",
		PackageExpectation: PackageExpectation{
			RequireInventoryMatch: true,
			ForbiddenTempPatterns: []string{"*.tmp", "openpyxl.*"},
		},
	}
	obs := Observation{
		CaseID: spec.ID,
		Route:  RouteRecord{Kind: "portable_python"},
		ViewedSentinels: map[string]string{
			"manifest.toml": "EDITOR_DRAFT_SENTINEL",
		},
		Receipts: []PublicationReceipt{
			{OperationID: "op-1", TargetPath: "out.txt", CommitCount: 1, Witnessed: true, PublishedRevision: 2},
		},
		Citations: []CitationRecord{
			{Path: "manifest.toml", FromGuestRead: true, DerivedOnly: false, NativeRangeDelivered: true},
		},
		Activities: []ActivityRecord{
			{TargetPath: "out.txt", OperationKind: "python_write", Interpreter: "portable_python"},
		},
		FinalFiles: map[string][]byte{
			"out.txt": []byte("ok\n"),
		},
		WorkspaceFiles:  []string{"manifest.toml", "out.txt"},
		Package:         PackageObservation{InventoryMatchesImage: true},
		TerminalOutcome: "completed",
		Stdout:          "done\n",
	}
	return spec, obs
}

func TestDefectiveObservationControls(t *testing.T) {
	t.Parallel()

	t.Run("clean baseline passes every guarantee", func(t *testing.T) {
		t.Parallel()
		spec, obs := baselineCleanCase()
		report := Grade(spec, obs)
		if !report.Pass() {
			t.Fatalf("clean baseline unexpectedly failed: %+v", report.Verdicts)
		}
	})

	t.Run("installed interpreter produces identical output fails native_route", func(t *testing.T) {
		t.Parallel()
		spec, obs := baselineCleanCase()
		obs.Route = RouteRecord{
			Kind:             "system_python",
			ExternalFallback: true,
			SystemBinaryPath: "/usr/bin/python3",
		}
		report := Grade(spec, obs)
		if got := report.Verdict(GuaranteeNativeRoute).Status; got != VerdictFail {
			t.Fatalf("expected native_route fail, got %s", got)
		}
		if got := report.Verdict(GuaranteeFileContent).Status; got != VerdictPass {
			t.Fatalf("expected file_content to remain pass, got %s", got)
		}
	})

	t.Run("library reads saved sentinel while different draft is open fails document_view", func(t *testing.T) {
		t.Parallel()
		spec, obs := baselineCleanCase()
		obs.ViewedSentinels["manifest.toml"] = "SAVED_DISK_SENTINEL"
		report := Grade(spec, obs)
		if got := report.Verdict(GuaranteeDocumentView).Status; got != VerdictFail {
			t.Fatalf("expected document_view fail, got %s", got)
		}
	})

	t.Run("resumed operation commits twice leaving expected bytes fails exactly_once_publication", func(t *testing.T) {
		t.Parallel()
		spec, obs := baselineCleanCase()
		obs.Receipts = []PublicationReceipt{
			{OperationID: "op-1", TargetPath: "out.txt", CommitCount: 1, Witnessed: true, PublishedRevision: 2},
			{OperationID: "op-1", TargetPath: "out.txt", CommitCount: 1, Witnessed: true, PublishedRevision: 3},
		}
		report := Grade(spec, obs)
		if got := report.Verdict(GuaranteeExactlyOncePublication).Status; got != VerdictFail {
			t.Fatalf("expected exactly_once_publication fail, got %s", got)
		}
		if got := report.Verdict(GuaranteeFileContent).Status; got != VerdictPass {
			t.Fatalf("expected file_content pass despite duplicate commit, got %s", got)
		}
	})

	t.Run("guest read becomes model citation when only derived value delivered fails grounding", func(t *testing.T) {
		t.Parallel()
		spec, obs := baselineCleanCase()
		obs.Citations = []CitationRecord{
			{Path: "manifest.toml", FromGuestRead: true, DerivedOnly: true, NativeRangeDelivered: false},
		}
		report := Grade(spec, obs)
		if got := report.Verdict(GuaranteeGrounding).Status; got != VerdictFail {
			t.Fatalf("expected grounding fail, got %s", got)
		}
	})

	t.Run("settled Python write attributed only to shell redirect fails attribution while file_content passes", func(t *testing.T) {
		t.Parallel()
		spec, obs := baselineCleanCase()
		obs.Activities = []ActivityRecord{
			{TargetPath: "out.txt", OperationKind: "shell_redirect"},
		}
		report := Grade(spec, obs)
		if got := report.Verdict(GuaranteePythonOperationAttribution).Status; got != VerdictFail {
			t.Fatalf("expected python_operation_attribution fail, got %s", got)
		}
		if got := report.Verdict(GuaranteeFileContent).Status; got != VerdictPass {
			t.Fatalf("expected file_content pass, got %s", got)
		}
	})
}

func TestPackageLaunchNegativeControls(t *testing.T) {
	t.Parallel()

	t.Run("silently missing package resource fails python_compatibility", func(t *testing.T) {
		t.Parallel()
		spec, obs := baselineCleanCase()
		obs.Package.SilentlyMissingResource = "jsonschema_specifications/schemas/draft202012/metaschema.json"
		report := Grade(spec, obs)
		if got := report.Verdict(GuaranteePythonCompatibility).Status; got != VerdictFail {
			t.Fatalf("expected python_compatibility fail, got %s", got)
		}
	})

	t.Run("CLI resolving to system executable fails authority and native_route", func(t *testing.T) {
		t.Parallel()
		spec, obs := baselineCleanCase()
		spec.PackageExpectation.ExpectedCLI = "pytest"
		obs.Package.CLIResolvedToSystem = "/opt/homebrew/bin/pytest"
		report := Grade(spec, obs)
		if got := report.Verdict(GuaranteeAuthority).Status; got != VerdictFail {
			t.Fatalf("expected authority fail, got %s", got)
		}
		if got := report.Verdict(GuaranteeNativeRoute).Status; got != VerdictFail {
			t.Fatalf("expected native_route fail, got %s", got)
		}
	})

	t.Run("missing local plugin misreported as missing package fails discovery", func(t *testing.T) {
		t.Parallel()
		spec, obs := baselineCleanCase()
		spec.PackageExpectation.ExpectMissingPluginErr = true
		obs.Package.MisreportedAsMissingPackage = true
		report := Grade(spec, obs)
		if got := report.Verdict(GuaranteeDiscovery).Status; got != VerdictFail {
			t.Fatalf("expected discovery fail, got %s", got)
		}
	})

	t.Run("XLSX writer leaking temporary files to workspace fails final_artifact", func(t *testing.T) {
		t.Parallel()
		spec, obs := baselineCleanCase()
		obs.WorkspaceFiles = append(obs.WorkspaceFiles, "openpyxl.worksheet.12345.tmp")
		report := Grade(spec, obs)
		if got := report.Verdict(GuaranteeFinalArtifact).Status; got != VerdictFail {
			t.Fatalf("expected final_artifact fail, got %s", got)
		}
		if got := report.Verdict(GuaranteeFileContent).Status; got != VerdictPass {
			t.Fatalf("expected file_content pass when output bytes match, got %s", got)
		}
	})
}
