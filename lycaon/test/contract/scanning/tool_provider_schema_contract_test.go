package contract

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/tools/toolstest"

	"github.com/lycaon/lycaon/internal/confine"
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
	if err := toolstest.ValidateRegistryProviderArgsSchemas(reg); err != nil {
		contractcheck.FailErr(t, "validate boot registry provider args schemas", err)
	}
}

// TestServeBootToolMetadataMatchesCatalog checks the published generation.
func TestServeBootToolMetadataMatchesCatalog(t *testing.T) {
	cfg, _, err := extpacks.LoadEffectiveToolSchemas(contractcheck.StockCatalog(t))
	contractcheck.FailErr(t, "LoadEffectiveToolSchemas", err)
	reg := toolfixture.ContractServeBootRegistry(t)
	if err := toolstest.ValidateCatalogSchemaParity(reg, cfg); err != nil {
		contractcheck.FailErr(t, "validate catalog metadata", err)
	}
}

// TestCoordinatorTrimmedToolsKeepProviderArgsSchema guards the coordinator diet path
// that strips schema properties but must not drop ArgsSchema entirely.
func TestCoordinatorTrimmedToolsKeepProviderArgsSchema(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	cfg, err := toolschema.LoadSchemaDirWithOperations(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	contractcheck.FailErr(t, "LoadSchemaDir", err)
	meta, ok := cfg.ToolMeta("read")
	if !ok {
		t.Fatal("read missing from tools/schemas")
	}
	full := tools.ToolMeta{Name: "read", Description: meta.Description, ArgsSchema: meta.ArgsSchema}
	trimmed := tools.ProjectProviderToolMeta(full)
	if err := toolstest.ValidateProviderArgsSchema(trimmed); err != nil {
		contractcheck.FailErr(t, "validate trimmed coordinator read schema", err)
	}
}

// Provider function schemas require an object root without root unions.
func TestCoordinatorTrimmedCatalogRootsAreProviderSafe(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	cfg, err := toolschema.LoadSchemaDirWithOperations(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	contractcheck.FailErr(t, "LoadSchemaDir", err)

	catalogRoots := map[string][]string{}
	for name := range cfg.Tools {
		yamlMeta, ok := cfg.ToolMeta(name)
		if !ok {
			t.Fatalf("ToolMeta(%q) missing after LoadSchemaDir", name)
		}
		for _, key := range []string{"oneOf", "anyOf", "allOf", "enum", "const", "not"} {
			if _, has := yamlMeta.ArgsSchema[key]; has {
				catalogRoots[name] = append(catalogRoots[name], key)
			}
		}
		trimmed := tools.ProjectProviderToolMeta(tools.ToolMeta{
			Name:        name,
			Description: yamlMeta.Description,
			ArgsSchema:  yamlMeta.ArgsSchema,
		})
		if err := toolstest.ValidateProviderArgsSchema(trimmed); err != nil {
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
	trimmedCommand := tools.ProjectProviderToolMeta(tools.ToolMeta{
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

func TestCapabilityRequestAcceptsExactlyTheContractFields(t *testing.T) {
	cfg, _, err := extpacks.LoadEffectiveToolSchemas(contractcheck.StockCatalog(t))
	contractcheck.FailErr(t, "load effective schemas", err)
	samples := map[string]any{
		"host_resources":   []any{"docker"},
		"socket_paths":     []any{"/tmp/service.sock"},
		"process_control":  true,
		"credential_store": true,
		"host_execution":   true,
		"direct_ip":        true,
		"local_listen":     map[string]any{"ports": []any{8080}},
		"loopback_connect": map[string]any{"ports": []any{8080}},
		"write_root":       "/tmp/out",
		"read_path":        "/tmp/in",
	}
	for name := range cfg.Tools {
		contract, ok := toolcontract.Lookup(name)
		supported := contract.CapabilityRequestFields()
		if !ok || len(supported) == 0 {
			continue
		}
		meta, _ := cfg.ToolMeta(name)
		properties, _ := meta.ArgsSchema["properties"].(map[string]any)
		request, ok := properties["capability_request"].(map[string]any)
		if !ok {
			t.Errorf("%s supports %v but declares no capability_request", name, supported)
			continue
		}
		base := map[string]any{}
		if required, _ := request["required"].([]any); len(required) > 0 {
			for _, field := range required {
				base[field.(string)] = samples[field.(string)]
			}
		}
		for field, value := range samples {
			args := maps.Clone(base)
			args[field] = value
			err := tools.ValidateToolArgs(request, args)
			if slices.Contains(supported, field) && err != nil {
				t.Errorf("%s rejected supported capability_request.%s: %v", name, field, err)
			}
			if !slices.Contains(supported, field) && err == nil {
				t.Errorf("%s accepted capability_request.%s outside its execution contract", name, field)
			}
		}
		if slices.Contains(supported, "socket_paths") {
			tooMany := make([]any, confine.MaxSocketGrants+1)
			for i := range tooMany {
				tooMany[i] = fmt.Sprintf("/tmp/s%d.sock", i)
			}
			if err := tools.ValidateToolArgs(request, map[string]any{"socket_paths": tooMany}); err == nil {
				t.Errorf("%s accepted %d socket paths, confinement grants at most %d", name, len(tooMany), confine.MaxSocketGrants)
			}
			if err := tools.ValidateToolArgs(request, map[string]any{"socket_paths": tooMany[:confine.MaxSocketGrants]}); err != nil {
				t.Errorf("%s rejected %d socket paths: %v", name, confine.MaxSocketGrants, err)
			}
		}
	}
}

func TestCapabilityRequestNarrowingsHold(t *testing.T) {
	cfg, _, err := extpacks.LoadEffectiveToolSchemas(contractcheck.StockCatalog(t))
	contractcheck.FailErr(t, "load effective schemas", err)
	request := func(tool string) map[string]any {
		meta, ok := cfg.ToolMeta(tool)
		if !ok {
			t.Fatalf("%s schema missing", tool)
		}
		properties, _ := meta.ArgsSchema["properties"].(map[string]any)
		out, _ := properties["capability_request"].(map[string]any)
		return out
	}
	for _, tool := range []string{"wait", "capture_page", "measure_page", "page_open"} {
		if err := tools.ValidateToolArgs(request(tool), map[string]any{}); err == nil {
			t.Errorf("%s accepted a capability_request without loopback_connect", tool)
		}
		if err := tools.ValidateToolArgs(request(tool), map[string]any{"loopback_connect": map[string]any{"ports": []any{3000, 3000}}}); err == nil {
			t.Errorf("%s accepted a repeated loopback port", tool)
		}
	}
	for _, tool := range []string{"command", "verify", "terminal_open"} {
		if err := tools.ValidateToolArgs(request(tool), map[string]any{}); err == nil {
			t.Errorf("%s accepted an empty capability_request", tool)
		}
	}
}
