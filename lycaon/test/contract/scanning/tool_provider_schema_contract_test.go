package contract

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/toolfixture"
)

// TestServeBootRegistryToolsHaveProviderArgsSchema checks every provider projection.
func TestServeBootRegistryToolsHaveProviderArgsSchema(t *testing.T) {
	reg := toolfixture.ContractServeBootRegistry(t)
	if err := tools.ValidateRegistryProviderArgsSchemas(reg); err != nil {
		contractcheck.FailErr(t, "validate boot registry provider args schemas", err)
	}
}

// TestServeBootToolMetadataMatchesCatalog checks the published generation.
func TestServeBootToolMetadataMatchesCatalog(t *testing.T) {
	cfg, _, err := extpacks.LoadEffectiveToolSchemas(contractcheck.StockCatalog(t))
	contractcheck.FailErr(t, "LoadEffectiveToolSchemas", err)
	reg := toolfixture.ContractServeBootRegistry(t)
	if err := tools.ValidateCatalogSchemaParity(reg, cfg); err != nil {
		contractcheck.FailErr(t, "validate catalog metadata", err)
	}
}

// TestCoordinatorTrimmedToolsKeepProviderArgsSchema guards the coordinator diet path
// that strips schema properties but must not drop ArgsSchema entirely.
func TestCoordinatorTrimmedToolsKeepProviderArgsSchema(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	cfg, err := toolschema.LoadSchemaDir(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	contractcheck.FailErr(t, "LoadSchemaDir", err)
	meta, ok := cfg.ToolMeta("read")
	if !ok {
		t.Fatal("read missing from tools/schemas")
	}
	full := tools.ToolMeta{Name: "read", Description: meta.Description, ArgsSchema: meta.ArgsSchema}
	trimmed := tools.TrimCoordinatorToolMeta(full)
	if err := tools.ValidateProviderArgsSchema(trimmed); err != nil {
		contractcheck.FailErr(t, "validate trimmed coordinator read schema", err)
	}
}

// Provider function schemas require an object root without root unions.
func TestCoordinatorTrimmedCatalogRootsAreProviderSafe(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	cfg, err := toolschema.LoadSchemaDir(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	contractcheck.FailErr(t, "LoadSchemaDir", err)

	catalogRoots := map[string][]string{}
	for name := range cfg.Tools {
		yamlMeta, ok := cfg.ToolMeta(name)
		if !ok {
			t.Fatalf("ToolMeta(%q) missing after LoadSchemaDir", name)
		}
		for _, key := range tools.FunctionParametersRootForbiddenKeys() {
			if _, has := yamlMeta.ArgsSchema[key]; has {
				catalogRoots[name] = append(catalogRoots[name], key)
			}
		}
		trimmed := tools.TrimCoordinatorToolMeta(tools.ToolMeta{
			Name:        name,
			Description: yamlMeta.Description,
			ArgsSchema:  yamlMeta.ArgsSchema,
		})
		if err := tools.ValidateProviderArgsSchema(trimmed); err != nil {
			contractcheck.FailErr(t, "validate trimmed "+name, err)
		}
		if err := tools.ValidateFunctionParametersRoot(trimmed.ArgsSchema); err != nil {
			contractcheck.FailErr(t, "trimmed "+name+" function root", err)
		}
	}

	// code_rewrite retains its catalog-level root union.
	if !contractcheck.ContainsString(catalogRoots["code_rewrite"], "anyOf") {
		t.Fatalf("catalog code_rewrite must keep root anyOf (path|paths XOR); got %v", catalogRoots["code_rewrite"])
	}

	command, ok := cfg.ToolMeta("command")
	if !ok {
		t.Fatal("command missing from tools/schemas")
	}
	trimmedCommand := tools.TrimCoordinatorToolMeta(tools.ToolMeta{
		Name: "command", Description: command.Description, ArgsSchema: command.ArgsSchema,
	})
	directIP := nestedSchema(t, trimmedCommand.ArgsSchema, "properties", "capability_request", "properties", "direct_ip")
	if _, has := directIP["oneOf"]; !has {
		t.Fatalf("trimmed command.direct_ip lost nested oneOf: %v", directIP)
	}
}

func nestedSchema(t *testing.T, schema map[string]any, keys ...string) map[string]any {
	t.Helper()
	cur := schema
	for _, key := range keys {
		next, ok := cur[key].(map[string]any)
		if !ok {
			t.Fatalf("schema[%s] = %T, want map", key, cur[key])
		}
		cur = next
	}
	return cur
}

func TestProviderCapabilityFieldsMatchExecutionContract(t *testing.T) {
	cfg, _, err := extpacks.LoadEffectiveToolSchemas(contractcheck.StockCatalog(t))
	contractcheck.FailErr(t, "load effective schemas", err)
	fields := map[string]toolcontract.Capability{
		"host_resources":   toolcontract.CapabilityHostResource,
		"socket_paths":     toolcontract.CapabilitySocket,
		"direct_ip":        toolcontract.CapabilityDirectIP,
		"local_listen":     toolcontract.CapabilityLocalListen,
		"loopback_connect": toolcontract.CapabilityLoopbackConnect,
		"write_root":       toolcontract.CapabilityWriteRoot,
		"read_path":        toolcontract.CapabilityReadPath,
	}
	for name := range cfg.Tools {
		contract, ok := toolcontract.Lookup(name)
		if !ok {
			continue
		}
		meta, _ := cfg.ToolMeta(name)
		properties, _ := meta.ArgsSchema["properties"].(map[string]any)
		request, _ := properties["capability_request"].(map[string]any)
		capabilities, _ := request["properties"].(map[string]any)
		for field, capability := range fields {
			_, offered := capabilities[field]
			if offered != contract.Supports(capability) {
				t.Errorf("%s capability_request.%s offered=%v, execution supports=%v", name, field, offered, contract.Supports(capability))
			}
		}
	}
}
