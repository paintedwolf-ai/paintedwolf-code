package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
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
