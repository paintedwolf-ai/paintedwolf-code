package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/vaultidentity"
)

func TestCustodyPolicyNamesExactGenerationsAndClosures(t *testing.T) {
	policy := custodyTrustPolicy{FormatVersion: 1, StoreDirectory: "/custom/store", Generations: []trustedGeneration{{
		InstallRoot: "/custom/store/package/lib/painted-wolf-code",
		Manifest:    "/custom/store/package/lib/painted-wolf-code/manifest.json",
		StorePaths:  []string{"/custom/store/package", "/custom/store/runtime"},
	}}}
	raw, err := json.Marshal(policy)
	testutil.FailErr(t, "encode policy", err)
	decoded, err := decodeTrustPolicy(raw)
	testutil.FailErr(t, "decode explicit generation", err)
	if len(decoded.Generations) != 1 || len(decoded.Generations[0].StorePaths) != 2 {
		t.Fatalf("policy = %+v", decoded)
	}
	for _, mutate := range []func(*custodyTrustPolicy){
		func(p *custodyTrustPolicy) { p.FormatVersion = 2 },
		func(p *custodyTrustPolicy) { p.StoreDirectory = "/custom" },
		func(p *custodyTrustPolicy) { p.Generations = nil },
		func(p *custodyTrustPolicy) { p.Generations = append(p.Generations, p.Generations[0]) },
		func(p *custodyTrustPolicy) { p.Generations[0].InstallRoot += "/.." },
		func(p *custodyTrustPolicy) { p.Generations[0].Manifest = "/etc/other-manifest" },
		func(p *custodyTrustPolicy) { p.Generations[0].StorePaths = []string{"/custom/store/runtime"} },
		func(p *custodyTrustPolicy) {
			p.Generations[0].StorePaths = append(p.Generations[0].StorePaths, "/custom/store/runtime/nested")
		},
	} {
		var changed custodyTrustPolicy
		testutil.FailErr(t, "copy policy", json.Unmarshal(raw, &changed))
		mutate(&changed)
		bad, err := json.Marshal(changed)
		testutil.FailErr(t, "encode invalid policy", err)
		if _, err := decodeTrustPolicy(bad); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	for _, bad := range []string{string(raw) + " {}", strings.Replace(string(raw), `"format_version":1`, `"format_version":1,"unknown":true`, 1)} {
		if _, err := decodeTrustPolicy([]byte(bad)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}

func TestUnavailableSelectedPolicyNeverUsesTheDefaultManifest(t *testing.T) {
	for _, content := range []string{"", `{ "format_version": 2 }`, `{
		"format_version": 1, "store_directory": "/custom/store",
		"generations": [{"install_root": "/custom/store/package/lib/painted-wolf-code",
		"manifest": "/custom/store/package/lib/painted-wolf-code/manifest.json",
		"store_paths": ["/custom/store/package"]}]}`} {
		f := newFixture(t)
		f.server.TrustPolicy = filepath.Join(t.TempDir(), "trust.json")
		if content != "" {
			testutil.FailErr(t, "write untrusted policy", os.WriteFile(f.server.TrustPolicy, []byte(content), 0o644))
		}
		verifiers, response := f.server.verifiersFor()
		if len(verifiers) != 0 || response == nil || response.Status != vaultidentity.StatusUnavailable || response.Code != "manifest_unreadable" {
			t.Fatalf("untrusted policy fell back: verifiers=%v, response=%+v", verifiers, response)
		}
	}
}
