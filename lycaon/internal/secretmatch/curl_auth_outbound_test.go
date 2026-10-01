package secretmatch

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCurlAuthHeaderOutboundRequiresCredentialShape(t *testing.T) {
	placeholder := `curl -H "Authorization: Bearer your-api-key" https://example.test/v1/search`
	upstream, err := BuildMatcher(Dir(filepath.Join(t.TempDir(), "missing")))
	testutil.FailErr(t, "build upstream matcher", err)
	if !hasRule(upstream.Screen(placeholder), "gitleaks:curl-auth-header") {
		t.Fatal("placeholder does not exercise the upstream curl auth rule")
	}

	stock := loadBundled(t)
	if hits := stock.Screen(placeholder); len(hits) != 0 {
		t.Fatalf("documentation placeholder matched outbound: %#v", hits)
	}

	realShape := `curl -H "Authorization: Bearer ` + plantGitHub + `" https://api.github.com/user`
	if hits := stock.Screen(realShape); !hasRule(hits, "gitleaks:github-pat") {
		t.Fatalf("provider-shaped token in curl header was not detected: %#v", hits)
	}
}
