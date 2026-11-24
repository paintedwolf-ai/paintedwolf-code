package protectedpath

import (
	"sync"
	"testing"
)

func scalarIsCredentialFile(path string) bool {
	return MatchPathGlobs(credentialFileGlobs, path) && !MatchPathGlobs(credentialTemplateGlobs, path)
}

func TestCompiledCredentialClassifierMatchesScalarReferenceAndSegmentsOnce(t *testing.T) {
	if got := staticRuleSetCompilations.Load(); got != 2 {
		t.Fatalf("static rule-set compilations = %d, want 2 (positive + template)", got)
	}
	if compiledCredentialFiles.Len() != len(credentialFileGlobs) || compiledCredentialTemplates.Len() != len(credentialTemplateGlobs) {
		t.Fatalf("compiled rule counts = (%d, %d), want (%d, %d)",
			compiledCredentialFiles.Len(), compiledCredentialTemplates.Len(),
			len(credentialFileGlobs), len(credentialTemplateGlobs))
	}

	paths := []string{
		"",
		"   ",
		".",
		"/",
		"@",
		"@/",
		"@web",
		"@web/.env",
		"@web/.env.example",
		".env",
		".env.local",
		".env.production",
		".env.example",
		".env.sample",
		".env.template",
		".env.dist",
		".env.example.local",
		".env.sample.json",
		"certs/server.pem",
		"certs/server.pem.example",
		"id_rsa",
		"keys/id_rsa_deploy",
		"keys/id_ed25519.pub",
		"keys/id_ecdsa_backup",
		".ssh/config",
		"home/.ssh/authorized_keys",
		"home/.ssh/nested/plain",
		".aws/credentials",
		"ops/.aws/nested/config",
		"dir/.aws",
		".kube/config",
		"clusters/.kube/prod/token",
		".config/gcloud/application_default_credentials.json",
		"user/.config/gcloud/configurations/config_default",
		".azure/accessTokens.json",
		".docker/config.json",
		".npmrc",
		"packages/ui/.npmrc",
		".pypirc",
		".netrc",
		"../.netrc",
		"sub/../.env",
		"/abs/path/.env.local",
		"node_modules/pkg/.env",
		"dist/bundle/.npmrc",
		"worktrees/linked-branch/.env.staging",
		"src/deep/pkg/code.go",
		"dir with spaces/.env.secret",
		"weird[name]/.npmrc",
	}

	// Each non-empty normalized path must segment exactly once per IsCredentialFile call,
	// even when matching a positive rule that also checks template exclusions.
	for _, p := range paths {
		before := candidateSegmentations.Load()
		got := IsCredentialFile(p)
		after := candidateSegmentations.Load()
		want := scalarIsCredentialFile(p)
		if got != want {
			t.Fatalf("IsCredentialFile(%q) = %v, scalar = %v", p, got, want)
		}
		_, valid := normalizeCredentialRel(p)
		wantSegs := int64(0)
		if valid {
			wantSegs = 1
		}
		if delta := after - before; delta != wantSegs {
			t.Fatalf("IsCredentialFile(%q) segmented %d times, want %d", p, delta, wantSegs)
		}
		if gotTmpl, wantTmpl := IsCredentialTemplate(p), MatchPathGlobs(credentialTemplateGlobs, p); gotTmpl != wantTmpl {
			t.Fatalf("IsCredentialTemplate(%q) = %v, scalar = %v", p, gotTmpl, wantTmpl)
		}
	}

	// Concurrent callers share the immutable compiled rule sets without recompiling.
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, p := range paths {
				if IsCredentialFile(p) != scalarIsCredentialFile(p) {
					t.Errorf("concurrent mismatch on %q", p)
				}
			}
		}()
	}
	wg.Wait()

	if got := staticRuleSetCompilations.Load(); got != 2 {
		t.Fatalf("static rule sets recompiled under concurrency: %d", got)
	}
}
