package portablepython

import (
	"bytes"
	"fmt"
	"path"
	"sort"
	"strings"
)

// Grade evaluates every Portable Python guarantee independently from spec and obs.
func Grade(spec CaseSpec, obs Observation) GradeReport {
	oracleVer := spec.OracleVersion
	if oracleVer == "" {
		oracleVer = OracleSchemaVersion
	}
	report := GradeReport{
		CaseID:        spec.ID,
		CaseVersion:   spec.Version,
		OracleVersion: oracleVer,
		GraderVersion: GraderVersion,
		Verdicts:      make(map[Guarantee]GuaranteeVerdict, 11),
	}
	report.Verdicts[GuaranteeNativeRoute] = gradeNativeRoute(spec, obs)
	report.Verdicts[GuaranteeDocumentView] = gradeDocumentView(spec, obs)
	report.Verdicts[GuaranteeExactlyOncePublication] = gradeExactlyOnce(obs)
	report.Verdicts[GuaranteeGrounding] = gradeGrounding(obs)
	report.Verdicts[GuaranteePythonOperationAttribution] = gradePythonAttribution(spec, obs)
	report.Verdicts[GuaranteeFileContent] = gradeFileContent(spec, obs)
	report.Verdicts[GuaranteeDiscovery] = gradeDiscovery(spec, obs)
	report.Verdicts[GuaranteePythonCompatibility] = gradePythonCompatibility(obs)
	report.Verdicts[GuaranteeAuthority] = gradeAuthority(obs)
	report.Verdicts[GuaranteeProvenance] = gradeProvenance(obs)
	report.Verdicts[GuaranteeFinalArtifact] = gradeFinalArtifact(spec, obs)
	return report
}

func gradeNativeRoute(spec CaseSpec, obs Observation) GuaranteeVerdict {
	if spec.ExpectedRoute == "" {
		return GuaranteeVerdict{Guarantee: GuaranteeNativeRoute, Status: VerdictNotApplicable}
	}
	if obs.Route.ExternalFallback || obs.Route.SystemBinaryPath != "" {
		return GuaranteeVerdict{
			Guarantee: GuaranteeNativeRoute,
			Status:    VerdictFail,
			Reason:    "unexpected external or system interpreter launch",
			Evidence:  fmt.Sprintf("kind=%s external=%v binary=%s", obs.Route.Kind, obs.Route.ExternalFallback, obs.Route.SystemBinaryPath),
		}
	}
	if obs.Package.CLIResolvedToSystem != "" {
		return GuaranteeVerdict{
			Guarantee: GuaranteeNativeRoute,
			Status:    VerdictFail,
			Reason:    "advertised CLI resolved to system executable",
			Evidence:  obs.Package.CLIResolvedToSystem,
		}
	}
	if obs.Route.Kind != spec.ExpectedRoute {
		return GuaranteeVerdict{
			Guarantee: GuaranteeNativeRoute,
			Status:    VerdictFail,
			Reason:    fmt.Sprintf("route mismatch: want %q, got %q", spec.ExpectedRoute, obs.Route.Kind),
		}
	}
	return GuaranteeVerdict{Guarantee: GuaranteeNativeRoute, Status: VerdictPass, Evidence: obs.Route.Kind}
}

func gradeDocumentView(spec CaseSpec, obs Observation) GuaranteeVerdict {
	if len(spec.ExpectedSentinels) == 0 {
		return GuaranteeVerdict{Guarantee: GuaranteeDocumentView, Status: VerdictNotApplicable}
	}
	keys := make([]string, 0, len(spec.ExpectedSentinels))
	for k := range spec.ExpectedSentinels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		want := spec.ExpectedSentinels[k]
		got, ok := obs.ViewedSentinels[k]
		if !ok || got != want {
			return GuaranteeVerdict{
				Guarantee: GuaranteeDocumentView,
				Status:    VerdictFail,
				Reason:    fmt.Sprintf("document view mismatch for %s: want sentinel %q, got %q", k, want, got),
			}
		}
	}
	return GuaranteeVerdict{Guarantee: GuaranteeDocumentView, Status: VerdictPass}
}

func gradeExactlyOnce(obs Observation) GuaranteeVerdict {
	if len(obs.Receipts) == 0 {
		return GuaranteeVerdict{Guarantee: GuaranteeExactlyOncePublication, Status: VerdictNotApplicable}
	}
	seen := make(map[string]int, len(obs.Receipts))
	for _, rc := range obs.Receipts {
		if rc.CommitCount > 1 {
			return GuaranteeVerdict{
				Guarantee: GuaranteeExactlyOncePublication,
				Status:    VerdictFail,
				Reason:    fmt.Sprintf("operation %s on %s committed %d times", rc.OperationID, rc.TargetPath, rc.CommitCount),
			}
		}
		key := rc.OperationID + "\x00" + rc.TargetPath
		seen[key]++
		if seen[key] > 1 {
			return GuaranteeVerdict{
				Guarantee: GuaranteeExactlyOncePublication,
				Status:    VerdictFail,
				Reason:    fmt.Sprintf("duplicate publication receipt for operation %s on %s", rc.OperationID, rc.TargetPath),
			}
		}
	}
	return GuaranteeVerdict{Guarantee: GuaranteeExactlyOncePublication, Status: VerdictPass}
}

func gradeGrounding(obs Observation) GuaranteeVerdict {
	if len(obs.Citations) == 0 {
		return GuaranteeVerdict{Guarantee: GuaranteeGrounding, Status: VerdictNotApplicable}
	}
	for _, c := range obs.Citations {
		if c.FromGuestRead && c.DerivedOnly && !c.NativeRangeDelivered {
			return GuaranteeVerdict{
				Guarantee: GuaranteeGrounding,
				Status:    VerdictFail,
				Reason:    fmt.Sprintf("guest read of %s promoted to source citation without native range delivery", c.Path),
			}
		}
	}
	return GuaranteeVerdict{Guarantee: GuaranteeGrounding, Status: VerdictPass}
}

func gradePythonAttribution(spec CaseSpec, obs Observation) GuaranteeVerdict {
	if len(spec.ExpectedFiles) == 0 && len(obs.Activities) == 0 {
		return GuaranteeVerdict{Guarantee: GuaranteePythonOperationAttribution, Status: VerdictNotApplicable}
	}
	if len(obs.Activities) == 0 {
		return GuaranteeVerdict{
			Guarantee: GuaranteePythonOperationAttribution,
			Status:    VerdictFail,
			Reason:    "settled workspace mutation has no recorded activity attribution",
		}
	}
	for _, act := range obs.Activities {
		if act.OperationKind == "shell_redirect" || !strings.HasPrefix(act.OperationKind, "python") {
			return GuaranteeVerdict{
				Guarantee: GuaranteePythonOperationAttribution,
				Status:    VerdictFail,
				Reason:    fmt.Sprintf("workspace change to %s attributed to %q instead of a Python operation", act.TargetPath, act.OperationKind),
			}
		}
	}
	return GuaranteeVerdict{Guarantee: GuaranteePythonOperationAttribution, Status: VerdictPass}
}

func gradeFileContent(spec CaseSpec, obs Observation) GuaranteeVerdict {
	if len(spec.ExpectedFiles) == 0 {
		return GuaranteeVerdict{Guarantee: GuaranteeFileContent, Status: VerdictNotApplicable}
	}
	keys := make([]string, 0, len(spec.ExpectedFiles))
	for k := range spec.ExpectedFiles {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		got, ok := obs.FinalFiles[k]
		if !ok {
			return GuaranteeVerdict{
				Guarantee: GuaranteeFileContent,
				Status:    VerdictFail,
				Reason:    fmt.Sprintf("missing expected file %s", k),
			}
		}
		if !bytes.Equal(got, spec.ExpectedFiles[k]) {
			return GuaranteeVerdict{
				Guarantee: GuaranteeFileContent,
				Status:    VerdictFail,
				Reason:    fmt.Sprintf("byte mismatch on %s", k),
			}
		}
	}
	return GuaranteeVerdict{Guarantee: GuaranteeFileContent, Status: VerdictPass}
}

func gradeDiscovery(spec CaseSpec, obs Observation) GuaranteeVerdict {
	if !spec.PackageExpectation.RequireInventoryMatch && !spec.PackageExpectation.ExpectMissingPluginErr && !obs.Package.MisreportedAsMissingPackage {
		return GuaranteeVerdict{Guarantee: GuaranteeDiscovery, Status: VerdictNotApplicable}
	}
	if obs.Package.MisreportedAsMissingPackage {
		return GuaranteeVerdict{
			Guarantee: GuaranteeDiscovery,
			Status:    VerdictFail,
			Reason:    "missing local plugin was misreported as a missing package",
		}
	}
	if spec.PackageExpectation.RequireInventoryMatch && !obs.Package.InventoryMatchesImage {
		return GuaranteeVerdict{
			Guarantee: GuaranteeDiscovery,
			Status:    VerdictFail,
			Reason:    "rendered package inventory does not match built image",
		}
	}
	return GuaranteeVerdict{Guarantee: GuaranteeDiscovery, Status: VerdictPass}
}

func gradePythonCompatibility(obs Observation) GuaranteeVerdict {
	if obs.Package.SilentlyMissingResource != "" {
		return GuaranteeVerdict{
			Guarantee: GuaranteePythonCompatibility,
			Status:    VerdictFail,
			Reason:    fmt.Sprintf("package resource silently missing: %s", obs.Package.SilentlyMissingResource),
		}
	}
	return GuaranteeVerdict{Guarantee: GuaranteePythonCompatibility, Status: VerdictPass}
}

func gradeAuthority(obs Observation) GuaranteeVerdict {
	if obs.Package.CLIResolvedToSystem != "" {
		return GuaranteeVerdict{
			Guarantee: GuaranteeAuthority,
			Status:    VerdictFail,
			Reason:    fmt.Sprintf("CLI escaped guest boundary to system executable %s", obs.Package.CLIResolvedToSystem),
		}
	}
	if obs.Package.AuthorityViolation != "" {
		return GuaranteeVerdict{
			Guarantee: GuaranteeAuthority,
			Status:    VerdictFail,
			Reason:    obs.Package.AuthorityViolation,
		}
	}
	return GuaranteeVerdict{Guarantee: GuaranteeAuthority, Status: VerdictPass}
}

func gradeProvenance(obs Observation) GuaranteeVerdict {
	if len(obs.Receipts) == 0 {
		return GuaranteeVerdict{Guarantee: GuaranteeProvenance, Status: VerdictNotApplicable}
	}
	for _, rc := range obs.Receipts {
		if !rc.Witnessed {
			return GuaranteeVerdict{
				Guarantee: GuaranteeProvenance,
				Status:    VerdictFail,
				Reason:    fmt.Sprintf("receipt %s for %s is not host-witnessed", rc.OperationID, rc.TargetPath),
			}
		}
	}
	return GuaranteeVerdict{Guarantee: GuaranteeProvenance, Status: VerdictPass}
}

func gradeFinalArtifact(spec CaseSpec, obs Observation) GuaranteeVerdict {
	for _, wf := range obs.WorkspaceFiles {
		base := path.Base(wf)
		for _, pat := range spec.PackageExpectation.ForbiddenTempPatterns {
			matched, err := path.Match(pat, base)
			if err == nil && matched {
				return GuaranteeVerdict{
					Guarantee: GuaranteeFinalArtifact,
					Status:    VerdictFail,
					Reason:    fmt.Sprintf("temporary file leaked to workspace: %s (matched %s)", wf, pat),
				}
			}
		}
	}
	if spec.ExpectedOutcome != "" && obs.TerminalOutcome != spec.ExpectedOutcome {
		return GuaranteeVerdict{
			Guarantee: GuaranteeFinalArtifact,
			Status:    VerdictFail,
			Reason:    fmt.Sprintf("terminal outcome mismatch: want %q, got %q", spec.ExpectedOutcome, obs.TerminalOutcome),
		}
	}
	return GuaranteeVerdict{Guarantee: GuaranteeFinalArtifact, Status: VerdictPass}
}
