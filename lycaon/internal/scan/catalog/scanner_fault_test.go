package catalog

import (
	"testing"
)

func TestExecutionManifestClassifiesConfigFaults(t *testing.T) {
	validFp := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	cases := []struct {
		name       string
		contract   ScannerContract
		wantReason ConfigFaultReason
	}{
		{
			name:       "invalid contract",
			contract:   ScannerContract{},
			wantReason: ConfigFaultInvalidContract,
		},
		{
			name: "missing bundled engine byte identity",
			contract: ScannerContract{
				ScannerID:             "lycaon-sast",
				Engine:                "opengrep",
				Driver:                DriverBundled,
				Scope:                 ScopeSourceHostFloor,
				DefinitionFingerprint: validFp,
				RulesSHA256:           validFp,
				ExclusionsSHA256:      validFp,
			},
			wantReason: ConfigFaultEngineIdentityMissing,
		},
		{
			name: "missing external executable",
			contract: ScannerContract{
				ScannerID:             "external-scanner",
				Engine:                "custom",
				Driver:                DriverExternal,
				Scope:                 ScopeCustom,
				DefinitionFingerprint: validFp,
				EngineExecutable:      "definitely-missing-lycaon-scanner-binary",
			},
			wantReason: ConfigFaultExecutableUnavailable,
		},
		{
			name: "incomplete host floor identity",
			contract: ScannerContract{
				ScannerID:             "lycaon-sast",
				Engine:                "opengrep",
				Driver:                DriverBundled,
				Scope:                 ScopeSourceHostFloor,
				DefinitionFingerprint: validFp,
				EngineSHA256:          validFp,
			},
			wantReason: ConfigFaultHostFloorIdentityIncomplete,
		},
		{
			name: "missing secret rules identity",
			contract: ScannerContract{
				ScannerID:             "lycaon-secrets",
				Engine:                "gitleaks",
				Driver:                DriverLibrary,
				Scope:                 ScopeSecrets,
				DefinitionFingerprint: validFp,
				EngineSHA256:          validFp,
			},
			wantReason: ConfigFaultSecretRulesIdentityMissing,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := ExecutionManifest(tc.contract)
			if err == nil {
				t.Fatal("expected configuration fault error")
			}
			fault, ok := AsConfigFault(err)
			if !ok {
				t.Fatalf("error %v was not classified as ConfigFaultError", err)
			}
			if fault.Reason != tc.wantReason {
				t.Fatalf("reason = %q, want %q (detail: %s)", fault.Reason, tc.wantReason, fault.Detail)
			}
		})
	}
}
