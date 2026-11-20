package llm

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Discovery strategies without a compatible /models endpoint stay documented.
func TestDiscoveryStrategyConstsInKeepSet(t *testing.T) {
	t.Parallel()
	path := filepath.Join(configlayout.FindModuleRoot(), "internal", "llm", "providerprofile", "profile.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	testutil.FailErr(t, "parse providerprofile/profile.go", err)

	consts := discoveryStrategyConsts(f)
	if len(consts) == 0 {
		t.Fatal("no DiscoveryStrategy consts found in providerprofile/profile.go")
	}
	if len(consts) != len(discoveryKeepSet) {
		t.Errorf("declared strategies = %d, documented strategies = %d", len(consts), len(discoveryKeepSet))
	}
	for name, value := range consts {
		gap, ok := discoveryKeepSet[providerprofile.DiscoveryStrategy(value)]
		if !ok {
			t.Errorf("%s (%q) missing from discoveryKeepSet — document the feature gap before minting", name, value)
			continue
		}
		if strings.TrimSpace(gap) == "" {
			t.Errorf("%s (%q) has empty keep-set gap text", name, value)
		}
	}
}

func discoveryStrategyConsts(f *ast.File) map[string]string {
	out := make(map[string]string)
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		var iotaType string
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			typeName := ""
			if vs.Type != nil {
				if ident, ok := vs.Type.(*ast.Ident); ok {
					typeName = ident.Name
					iotaType = typeName
				}
			} else if iotaType != "" {
				typeName = iotaType
			}
			if typeName != "DiscoveryStrategy" {
				if vs.Type != nil {
					iotaType = ""
				}
				continue
			}
			for i, name := range vs.Names {
				if name.Name == "_" || !strings.HasPrefix(name.Name, "Discovery") {
					continue
				}
				if len(vs.Values) <= i {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				out[name.Name] = strings.Trim(lit.Value, `"`)
			}
		}
	}
	return out
}

// discoveryKeepSet records listing capabilities required by each strategy.
// The contract guard checks strategy coverage against this table.
var discoveryKeepSet = map[providerprofile.DiscoveryStrategy]string{
	providerprofile.DiscoveryConfigured: "Composable DiscoveryProfile over OpenAI-shaped /models " +
		"(Together type=chat + token pricing, OpenRouter output modalities, " +
		"OpenAI/generic id-only marked Untyped for fail-closed merge).",

	providerprofile.DiscoveryNone:             "Explicit opt-out — no live listing.",
	providerprofile.DiscoveryNativeGeneration: "Host-admitted artifact and device context capacity.",

	providerprofile.DiscoveryOllama: "Native GET /api/tags exposes feature flags (completion vs embedding); " +
		"OpenAI-compat /v1/models is id-only and mixes embed models.",

	providerprofile.DiscoveryLMStudio: "Native GET /api/v0/models exposes type∈{llm,vlm,embeddings}; " +
		"OpenAI-compat /v1/models omits type.",

	providerprofile.DiscoveryOMLX: "Native GET /v1/models/status exposes model_type∈{llm,vlm,embedding,…}; " +
		"OpenAI-compat /v1/models is id-only and mixes non-chat engines.",

	providerprofile.DiscoveryLiteLLM: "Native GET /model/info exposes model_info.mode (chat vs embedding/rerank); " +
		"OpenAI-compat /models is id-only (404 fallback marks Untyped).",

	providerprofile.DiscoveryGemini: "Native GET {v1beta}/models exposes supportedGenerationMethods " +
		"(generateContent + createCachedContent keep-set); OpenAI-compat /models is id-only.",

	providerprofile.DiscoveryVertexExpress: "Gemini native GET {v1beta}/models validates the express API key " +
		"and exposes supportedGenerationMethods; the Vertex express transport has no list method.",

	providerprofile.DiscoveryFireworks: "Serverless catalog control plane — not the OpenAI-compat /models shape.",

	providerprofile.DiscoveryCloudflareWorkersAI: "Workers AI account catalog + neuron usage analytics; " +
		"not a plain OpenAI-compat /models list.",

	providerprofile.DiscoveryAnthropic: "Anthropic GET /v1/models on the Messages API host — not OpenAI /models.",

	providerprofile.DiscoveryAzure: "Azure Resource Manager deployment list (AAD); data-plane /models " +
		"does not expose deployment control-plane rows.",

	providerprofile.DiscoveryBedrock: "Bedrock ListInferenceProfiles (Converse-usable ids); " +
		"ListFoundationModels / OpenAI-compat shapes are not reliable Converse keep-sets.",

	providerprofile.DiscoveryVertex: "Model Garden publishers/google/models with " +
		"supportedActions.openGenerationAiStudio; OpenAI-compat /models is not the Garden catalog.",
}
