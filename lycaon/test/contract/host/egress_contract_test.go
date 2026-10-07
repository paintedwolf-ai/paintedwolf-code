package contract

import (
	"fmt"
	"io/fs"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/egressclass"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// These client constructions omit request deadlines.
var unboundedClientPatterns = []string{
	"http.DefaultClient",
	"http.Get(",
	"http.Post(",
	"http.Head(",
	"http.PostForm(",
	"&http.Client{}",
}

// httpclientPkgDir owns HTTP client construction.
const httpclientPkgDir = "internal/httpclient"

func TestNoUnboundedHTTPClients(t *testing.T) {
	t.Parallel()
	root := filepath.Join(contractcheck.RepoRoot(t), "lycaon")
	internalDir := filepath.Join(root, "internal")

	var offenders []string

	err := filepath.WalkDir(internalDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if strings.HasPrefix(filepath.ToSlash(rel), httpclientPkgDir+"/") {
			return nil
		}

		content := contractcheck.ReadRepoFile(t, root, rel)
		for i, line := range strings.Split(content, "\n") {
			for _, pattern := range unboundedClientPatterns {
				if strings.Contains(line, pattern) {
					offenders = append(offenders, fmt.Sprintf("%s:%d — %s", filepath.ToSlash(rel), i+1, pattern))
				}
			}
		}
		return nil
	})
	contractcheck.FailErr(t, "walk internal for unbounded clients", err)

	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("unbounded HTTP clients in lycaon/internal (%d):\n  %s\n\n"+
			"Use httpclient.Bounded for request/response, httpclient.Streaming for LLM completions "+
			"(never a total timeout on a stream), or httpclient.Download for large fixed-host fetches.",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}

func TestEgressClassesMatchInventory(t *testing.T) {
	t.Parallel()
	want := []string{
		"agent_http_request",
		"browser_engine_download",
		"decide_model_download",
		"extension_pack_git",
		"llm_provider_request",
		"mcp_remote_http",
		"model_metadata_refresh",
		"osv_advisory_download",
		"package_identity_lookup",
		"pricing_feed_refresh",
		"provider_model_discovery",
		"update_manifest_check",
		"web_research_request",
	}

	classes := egressclass.All()
	got := make([]string, 0, len(classes))
	for _, c := range classes {
		got = append(got, string(c.ID))
	}
	sort.Strings(got)

	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("egressclass.All() ids = %v, want %v; update the outbound-class catalog before adding an id", got, want)
	}

	for _, c := range classes {
		if c.Transport == "" {
			t.Fatalf("%s has no transport", c.ID)
		}
		switch c.Transport {
		case egressclass.HTTPBounded, egressclass.HTTPStreaming, egressclass.HTTPDownload,
			egressclass.GitCLI, egressclass.LibraryDownload:
		default:
			t.Fatalf("%s has unknown transport %q", c.ID, c.Transport)
		}
		if c.EndpointSource != egressclass.EndpointBundled &&
			c.EndpointSource != egressclass.EndpointUserConfigured &&
			c.EndpointSource != egressclass.EndpointMixed {
			t.Fatalf("%s has unknown endpoint source %q", c.ID, c.EndpointSource)
		}
		if c.EndpointSource != egressclass.EndpointBundled && (len(c.FixedEndpoints) != 0 || c.EndpointTemplate != "") {
			t.Fatalf("%s is configured but declares fixed endpoints %q", c.ID, c.FixedEndpoints)
		}
	}
}

func TestEgressClassLookup(t *testing.T) {
	t.Parallel()
	for _, c := range egressclass.All() {
		got, ok := egressclass.Lookup(c.ID)
		if !ok {
			t.Fatalf("Lookup(%q) not found", c.ID)
		}
		if got.Transport != c.Transport {
			t.Fatalf("Lookup(%q).Transport = %q, want %q", c.ID, got.Transport, c.Transport)
		}
	}
	if _, ok := egressclass.Lookup("unknown"); ok {
		t.Fatal("Lookup returned a class for an id that is not in the inventory")
	}
}

func TestEgressClassInventoryCopiesNestedEndpointState(t *testing.T) {
	t.Parallel()
	first := egressclass.All()
	for index := range first {
		if len(first[index].FixedEndpoints) > 0 {
			first[index].FixedEndpoints[0] = "https://mutated.invalid"
		}
	}
	for _, class := range egressclass.All() {
		for _, endpoint := range class.FixedEndpoints {
			if endpoint == "https://mutated.invalid" {
				t.Fatal("egressclass.All leaked mutable endpoint inventory")
			}
		}
	}
}

// Each class declares a failure notice or an explicit silent disposition.
func TestEgressFailureCodesAreWellFormed(t *testing.T) {
	t.Parallel()
	var violations []string
	for _, c := range egressclass.All() {
		if c.NoticeCode == "" {
			if strings.TrimSpace(c.SilentReason) == "" {
				violations = append(violations, fmt.Sprintf(
					"egress class %s declares no NoticeCode and no SilentReason", c.ID))
			}
			continue
		}
		if strings.TrimSpace(c.SilentReason) != "" {
			violations = append(violations, fmt.Sprintf(
				"egress class %s emits %q but also declares a SilentReason", c.ID, c.NoticeCode))
		}
		for _, r := range c.NoticeCode {
			if (r < 'a' || r > 'z') && r != '_' {
				violations = append(violations, fmt.Sprintf(
					"egress class %s NoticeCode %q is not snake_case", c.ID, c.NoticeCode))
				break
			}
		}
	}
	contractcheck.FailViolations(t, "egress notice code drift", violations)
}

// Declared notice codes belong to the catalog and API error surface.
func TestEgressFailureCodesHaveNotices(t *testing.T) {
	t.Parallel()
	cfg := loadUserNoticeConfig(t)

	var violations []string
	for _, c := range egressclass.All() {
		if c.NoticeCode == "" {
			continue
		}
		entry, ok := cfg.UserNotices[c.NoticeCode]
		if !ok {
			violations = append(violations, fmt.Sprintf(
				"egress class %s NoticeCode %q has no row in host/user-notices", c.ID, c.NoticeCode))
			continue
		}
		if !entry.IsUserVisible() {
			violations = append(violations, fmt.Sprintf(
				"egress class %s NoticeCode %q is user_visible:false — the user is told nothing", c.ID, c.NoticeCode))
		}
	}
	contractcheck.FailViolations(t, "egress notice catalog drift", violations)
}

func TestEgressFailureCodesAreDistinct(t *testing.T) {
	t.Parallel()
	seen := map[string]string{}
	for _, c := range egressclass.All() {
		if c.NoticeCode == "" {
			continue
		}
		if prev, dup := seen[c.NoticeCode]; dup {
			t.Fatalf("classes %s and %s share NoticeCode %q: a user cannot tell the two failures apart",
				prev, c.ID, c.NoticeCode)
		}
		seen[c.NoticeCode] = string(c.ID)
	}
}

func TestNonHTTPOutboundAdaptersBindInventoryClasses(t *testing.T) {
	t.Parallel()
	root := filepath.Join(contractcheck.RepoRoot(t), "lycaon")

	extensions := contractcheck.ReadRepoFile(t, root, "internal/extpacks/install.go")
	if !strings.Contains(extensions,
		"RequireTransport(egressclass.ExtensionPackGit, egressclass.GitCLI)") {
		t.Fatal("extension-pack git clone is not bound to its egress class")
	}

	osv := egressclass.RequireSingleFixedEndpoint(
		egressclass.OSVAdvisoryDownload,
		egressclass.LibraryDownload,
	)
	scanner := contractcheck.ReadRepoFile(t, root, "internal/scan/drivers/library/scanners.go")
	if !strings.Contains(scanner,
		"egressclass.RequireSingleFixedEndpoint(") {
		t.Fatal("OSV library download is not configured from its egress class")
	}
	if strings.TrimSpace(osv) == "" {
		t.Fatal("OSV advisory class has no fixed endpoint")
	}

	update := egressclass.RequireTransport(egressclass.UpdateManifestCheck, egressclass.HTTPBounded)
	service := contractcheck.RustModuleSource(t, contractcheck.RepoRoot(t), "lycaon-den/src-tauri/src/update_service.rs")
	endpoint, err := url.Parse(update.EndpointTemplate)
	contractcheck.FailErr(t, "parse update endpoint template", err)
	origin := endpoint.Scheme + "://" + endpoint.Host
	if endpoint.Scheme != "https" || endpoint.Host == "" ||
		!strings.Contains(service, fmt.Sprintf("const DOWNLOAD_ORIGIN: &str = %q;", origin)) {
		t.Fatalf("update service does not bind inventory origin %q", origin)
	}
	template := strings.ReplaceAll(strings.TrimPrefix(update.EndpointTemplate, origin), "{key_generation}", "{}")
	if !strings.Contains(service, fmt.Sprintf("%q", "{DOWNLOAD_ORIGIN}"+template)) ||
		!strings.Contains(service, "embedded_key().0") {
		t.Fatalf("update service does not bind inventory template %q", update.EndpointTemplate)
	}
	// The feed is read directly, without proxies or redirects, and verified before any field is used.
	for _, needle := range []string{
		"reqwest::Client::builder()",
		".no_proxy()",
		".redirect(reqwest::redirect::Policy::none())",
		"verification::verify_feed(",
		"persistence::accept_feed_timestamp(",
	} {
		if !strings.Contains(service, needle) {
			t.Fatalf("update manifest class is missing %q", needle)
		}
	}
}

// Runtime browsing never provisions a missing browser bundle.
func TestBrowserProvisionDefaultsToNoDownload(t *testing.T) {
	t.Parallel()
	root := filepath.Join(contractcheck.RepoRoot(t), "lycaon")

	session := contractcheck.ReadRepoFile(t, root, "internal/browser/session.go")
	if strings.Contains(session, "AllowDownload") {
		t.Fatal("internal/browser/session.go sets AllowDownload: the runtime session path must leave it false " +
			"so a tool call never starts a several-hundred-megabyte download")
	}

	cli := contractcheck.ReadRepoFile(t, root, "cmd/lycaon/browser.go")
	if !strings.Contains(cli, "AllowDownload: true") {
		t.Fatal("cmd/lycaon/browser.go must set AllowDownload: true — it is the provisioning entry point " +
			"used by ./task browser:ensure and den-build-bundle.sh")
	}
}
