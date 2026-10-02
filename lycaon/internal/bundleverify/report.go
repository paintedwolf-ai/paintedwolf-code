package bundleverify

import "sort"

// Severity ranks a finding.
type Severity string

const (
	SeverityError Severity = "error"
	SeverityWarn  Severity = "warn"
)

// Finding codes form the release audit contract.
const (
	CodeCredentialProtectionFailed      = "CREDENTIAL_PROTECTION_FAILED"
	CodeSidecarMissing                  = "SIDECAR_MISSING"
	CodeLogsCLIMissing                  = "LOGS_CLI_MISSING"
	CodeDecideEngineMissing             = "DECIDE_ENGINE_MISSING"
	CodeDocumentCoreMissing             = "DOCUMENT_CORE_MISSING"
	CodeDocumentCoreFailed              = "DOCUMENT_CORE_FAILED"
	CodeDecideModelMissing              = "DECIDE_MODEL_MISSING"
	CodeEngineResourceMissing           = "ENGINE_RESOURCE_MISSING"
	CodeNoticesMissing                  = "NOTICES_MISSING"
	CodeOpenGrepInvalid                 = "OPENGREP_INVALID"
	CodeOpenGrepVerificationUnavailable = "OPENGREP_VERIFICATION_UNAVAILABLE"
	CodeArchUnexpected                  = "ARCH_UNEXPECTED"
	CodeArchExtraSlice                  = "ARCH_EXTRA_SLICE"
	CodeMinOSAboveFloor                 = "MINOS_ABOVE_FLOOR"
	CodeMinOSMissing                    = "MINOS_MISSING"
	CodeNonSystemDylib                  = "NON_SYSTEM_DYLIB"
	CodeBuildPathLeak                   = "BUILD_PATH_LEAK"
	CodeSignatureAdhoc                  = "SIGNATURE_ADHOC"
	CodeSignatureBroken                 = "SIGNATURE_BROKEN"
	CodeHardenedRuntimeMissing          = "HARDENED_RUNTIME_MISSING"
	CodeNotStapled                      = "NOT_STAPLED"
	CodeGatekeeperRejected              = "GATEKEEPER_REJECTED"
	CodeEntitlementMissing              = "ENTITLEMENT_MISSING"
	CodeEntitlementUnexpected           = "ENTITLEMENT_UNEXPECTED"
	CodeEntitlementUnreadable           = "ENTITLEMENT_UNREADABLE"
	CodeToolUnavailable                 = "TOOL_UNAVAILABLE"
	// CodeGitengineBloated marks an oversized native tool tree.
	CodeGitengineBloated = "GITENGINE_BLOATED"
)

// Finding is one defect.
type Finding struct {
	Code     string            `json:"code"`
	Severity Severity          `json:"severity"`
	Path     string            `json:"path"`             // bundle-relative
	Detail   map[string]string `json:"detail,omitempty"` // e.g. {"want":"13.0","got":"15.0"}
}

// Report is the result of one audit.
type Report struct {
	App           string    `json:"app"`
	DMG           string    `json:"dmg,omitempty"`
	MachOCount    int       `json:"macho_count"`
	RequireSigned bool      `json:"require_signed"`
	Findings      []Finding `json:"findings"`
}

// Failed reports whether any finding is SeverityError.
func (r Report) Failed() bool {
	for _, f := range r.Findings {
		if f.Severity == SeverityError {
			return true
		}
	}
	return false
}

// Counts returns the number of error and warn findings.
func (r Report) Counts() (errors, warns int) {
	for _, f := range r.Findings {
		if f.Severity == SeverityError {
			errors++
			continue
		}
		warns++
	}
	return errors, warns
}

// sortFindings orders by severity, code, then path.
func sortFindings(findings []Finding) {
	severityRank := func(s Severity) int {
		if s == SeverityError {
			return 0
		}
		return 1
	}
	sort.SliceStable(findings, func(i, j int) bool {
		a, b := findings[i], findings[j]
		if ra, rb := severityRank(a.Severity), severityRank(b.Severity); ra != rb {
			return ra < rb
		}
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		return a.Path < b.Path
	})
}
