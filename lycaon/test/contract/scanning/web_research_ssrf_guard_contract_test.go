package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestPageFetchPathsUseGuardedTransport(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	files := map[string]string{
		filepath.Join(root, "lycaon", "internal", "webresearch", "fetch.go"):             "outboundhttp.Do(",
		filepath.Join(root, "lycaon", "internal", "webresearch", "direct_page_probe.go"): "politeGuardedGet(",
		filepath.Join(root, "lycaon", "internal", "webresearch", "direct_site_fetch.go"): "politeGuardedGet(",
		filepath.Join(root, "lycaon", "internal", "webresearch", "direct_polite.go"):     "guardedGetWithHopPolicy(",
	}
	for path, guardedCall := range files {
		body, err := os.ReadFile(path)
		contractcheck.FailErr(t, "read file", err)
		src := string(body)
		if !strings.Contains(src, guardedCall) {
			t.Fatalf("%s must call %s", path, guardedCall)
		}
		for _, forbid := range []string{"http.Get(", "http.DefaultClient.Do(", "http.DefaultClient.Get("} {
			if strings.Contains(src, forbid) {
				t.Fatalf("%s must not bypass SSRF with %s", path, forbid)
			}
		}
	}
	transportPath := filepath.Join(root, "lycaon", "internal", "outboundhttp", "client.go")
	transportBody, err := os.ReadFile(transportPath)
	contractcheck.FailErr(t, "read file", err)
	transport := string(transportBody)
	for _, need := range []string{"egress.ResolveIPsWithPolicy(", "egress.PinnedTransport(", "in.BeforeHop(ctx, current)"} {
		if !strings.Contains(transport, need) {
			t.Fatalf("%s must own %s", transportPath, need)
		}
	}
}
