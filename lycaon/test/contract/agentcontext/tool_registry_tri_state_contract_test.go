package contract

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/toolfixture"
	"gopkg.in/yaml.v3"
)

// toolsSchemaExempt lists registered tools whose JSON schema is not in tools/schemas.
var toolsSchemaExempt = map[string]string{
	"command":      "host command handler is constructed by the runtime",
	"scan_list":    "native scan drill-down list",
	"scan_summary": "native scan drill-down summary",
	"scan_query":   "native scan drill-down query",
	"scan_compare": "native scan compare — schema registered at serve boot via RegisterScanTools",
}

func loadToolSchemaNames(t *testing.T, root string) map[string]struct{} {
	t.Helper()
	schemas, err := toolschema.LoadSchemaDir(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	contractcheck.FailErr(t, "load tools/schemas", err)
	out := make(map[string]struct{}, len(schemas.Tools))
	for name := range schemas.Tools {
		out[name] = struct{}{}
	}
	return out
}

func loadProfileToolNames(t *testing.T, root string) map[string]struct{} {
	t.Helper()
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "load tool-profiles", err)
	out := make(map[string]struct{})
	for _, p := range profiles {
		for name, allowed := range p.Tools {
			if allowed {
				out[name] = struct{}{}
			}
		}
		for _, name := range p.DenyTools {
			out[name] = struct{}{}
		}
	}
	return out
}

func loadNativeManifestNames(t *testing.T, root string) map[string]struct{} {
	t.Helper()
	names := loadNativeToolsManifestFlat(t)
	out := make(map[string]struct{}, len(names))
	for _, n := range names {
		out[n] = struct{}{}
	}
	return out
}

func TestToolRegistryTriStateHandlersMatchSchemas(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	reg := toolfixture.ContractServeBootRegistry(t)
	registerContractWorkerSurfaceTools(t, reg)
	registered := tools.RegisteredToolSet(reg)
	schemas := loadToolSchemaNames(t, root)

	var missingHandler []string
	for name := range schemas {
		if toolcontract.IsCommandOperation(name) {
			operation, found := reg.Operation(name)
			_, exposed := reg.Definition(name)
			if !found || operation.Handler != nil || exposed {
				t.Errorf("command-only operation %q must retain metadata without a public tool handler", name)
			}
			continue
		}
		if _, ok := registered[name]; !ok {
			if _, exempt := toolsSchemaExempt[name]; exempt {
				continue
			}
			missingHandler = append(missingHandler, name)
		}
	}
	contractcheck.FailViolations(t, "tools/schemas tools missing registered handlers", missingHandler)
}

func TestToolRegistryTriStateProfilesReferenceRegisteredTools(t *testing.T) {
	reg := toolfixture.ContractServeBootRegistry(t)
	registerContractWorkerSurfaceTools(t, reg)
	registered := tools.RegisteredToolSet(reg)
	profiles := loadProfileToolNames(t, contractcheck.RepoRoot(t))

	var violations []string
	for name := range profiles {
		if strings.Contains(name, "*") {
			continue
		}
		// mcp_* handlers register at runtime via MCP server sync (SyncTools),
		// never in the serve-boot mirror.
		if strings.HasPrefix(name, "mcp_") {
			continue
		}
		_, operation := reg.Operation(name)
		if !registered[name] && !(toolcontract.IsCommandOperation(name) && operation) {
			violations = append(violations, "tool-profiles reference "+name+" but serve registry has no tool or operation")
		}
	}
	contractcheck.FailViolations(t, "tool-profiles reference unregistered tools", violations)
}

func TestToolRegistryTriStateExemptMapsStale(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	reg := toolfixture.ContractServeBootRegistry(t)
	registerContractWorkerSurfaceTools(t, reg)
	registered := tools.RegisteredToolSet(reg)
	schemas := loadToolSchemaNames(t, root)
	profiles := loadProfileToolNames(t, root)
	native := loadNativeManifestNames(t, root)

	all := make(map[string]struct{})
	for k := range registered {
		all[k] = struct{}{}
	}
	for k := range schemas {
		all[k] = struct{}{}
	}
	for k := range profiles {
		all[k] = struct{}{}
	}
	for k := range native {
		all[k] = struct{}{}
	}

	checkStale := func(label string, exempt map[string]string) {
		var stale []string
		for code := range exempt {
			if _, ok := all[code]; !ok {
				stale = append(stale, code)
			}
		}
		if len(stale) > 0 {
			sort.Strings(stale)
			t.Fatalf("stale %s entries: %v", label, stale)
		}
	}
	checkStale("toolsSchemaExempt", toolsSchemaExempt)
}

func TestAgentToolProfilesYAMLShape(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "host", "agent-tool-profiles.yaml")
	data, err := os.ReadFile(path)
	contractcheck.FailErr(t, "read agent-tool-profiles.yaml", err)
	var doc struct {
		Profiles map[string]any `yaml:"profiles"`
	}
	contractcheck.FailErr(t, "parse agent-tool-profiles.yaml", yaml.Unmarshal(data, &doc))
	if len(doc.Profiles) == 0 {
		t.Fatal("agent-tool-profiles.yaml must declare at least coordinator profile")
	}
}
