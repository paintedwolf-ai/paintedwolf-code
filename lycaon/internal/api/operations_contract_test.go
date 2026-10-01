package api

import (
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/testutil"
	"gopkg.in/yaml.v3"
)

func TestGeneratedOperationsAreRegisteredExactlyOnce(t *testing.T) {
	server := NewServer(requiredTestDeps(t, Dependencies{}), nil, TestAPIToken)
	type routeKey struct{ Method, Path string }
	actual := make(map[routeKey]int, len(allGeneratedOperations))
	err := chi.Walk(server.router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		actual[routeKey{Method: method, Path: route}]++
		return nil
	})
	if err != nil {
		testutil.FailErr(t, "walk routes", err)
	}
	for _, operation := range allGeneratedOperations {
		key := routeKey{Method: operation.Method, Path: operation.Path}
		if actual[key] != 1 {
			t.Errorf("%s registered %d times", operation.ID, actual[key])
		}
		delete(actual, key)
	}
	for operation, count := range actual {
		if count != 0 {
			t.Errorf("route missing from OpenAPI: %s %s", operation.Method, operation.Path)
		}
	}
}

func TestCredentialCapturePolicyCoversOpenAPI(t *testing.T) {
	bundle := loadCaptureOpenAPI(t)
	requestProtected := captureOperationSet(credentialRequestOperations)
	responseProtected := captureOperationSet(withheldCredentialResponseOperations)

	var missing []string
	for operationID, schemaName := range captureRequestSchemas(bundle) {
		if captureSchemaHasWriteOnlyField(bundle, schemaName) && !requestProtected[operationID] {
			missing = append(missing, operationID+" request "+schemaName)
		}
	}
	for operationID, schemaName := range captureResponseSchemas(bundle) {
		if captureSchemaDisclosesValue(bundle, schemaName) && !responseProtected[operationID] {
			missing = append(missing, operationID+" response "+schemaName)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("credential capture policy missing:\n%s", strings.Join(missing, "\n"))
	}
}

func TestCredentialCaptureDeclarationsHaveBodies(t *testing.T) {
	bundle := loadCaptureOpenAPI(t)
	requests := captureRequestSchemas(bundle)
	responses := captureResponseSchemas(bundle)
	for _, operation := range credentialRequestOperations {
		if _, ok := requests[operation.ID]; !ok {
			t.Errorf("request capture operation %q has no JSON request body", operation.ID)
		}
	}
	for _, operation := range withheldCredentialResponseOperations {
		if _, ok := responses[operation.ID]; !ok {
			t.Errorf("response capture operation %q has no JSON response body", operation.ID)
		}
	}
}

func loadCaptureOpenAPI(t *testing.T) map[string]any {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	data, err := os.ReadFile(filepath.Join(root, "docs", "openapi.yaml"))
	testutil.FailErr(t, "read OpenAPI bundle", err)
	var bundle map[string]any
	testutil.FailErr(t, "parse OpenAPI bundle", yaml.Unmarshal(data, &bundle))
	return bundle
}

func captureOperationSet(operations []generatedOperation) map[string]bool {
	out := make(map[string]bool, len(operations))
	for _, operation := range operations {
		out[operation.ID] = true
	}
	return out
}

func captureRequestSchemas(bundle map[string]any) map[string]string {
	out := make(map[string]string)
	for _, rawItem := range capturePaths(bundle) {
		operations, _ := rawItem.(map[string]any)
		for method, rawOperation := range operations {
			if method == "parameters" {
				continue
			}
			operation, _ := rawOperation.(map[string]any)
			id, _ := operation["operationId"].(string)
			if id == "" {
				continue
			}
			if name := captureJSONSchemaName(operation["requestBody"]); name != "" {
				out[id] = name
			}
		}
	}
	return out
}

func captureResponseSchemas(bundle map[string]any) map[string]string {
	out := make(map[string]string)
	for _, rawItem := range capturePaths(bundle) {
		operations, _ := rawItem.(map[string]any)
		for method, rawOperation := range operations {
			if method == "parameters" {
				continue
			}
			operation, _ := rawOperation.(map[string]any)
			id, _ := operation["operationId"].(string)
			responses, _ := operation["responses"].(map[string]any)
			for status, rawResponse := range responses {
				if id == "" || !strings.HasPrefix(status, "2") {
					continue
				}
				if name := captureJSONSchemaName(rawResponse); name != "" {
					out[id] = name
				}
			}
		}
	}
	return out
}

func capturePaths(bundle map[string]any) map[string]any {
	paths, _ := bundle["paths"].(map[string]any)
	return paths
}

func captureJSONSchemaName(raw any) string {
	doc, _ := raw.(map[string]any)
	content, _ := doc["content"].(map[string]any)
	media, _ := content["application/json"].(map[string]any)
	schema, _ := media["schema"].(map[string]any)
	ref, _ := schema["$ref"].(string)
	if ref == "" {
		return ""
	}
	return ref[strings.LastIndex(ref, "/")+1:]
}

func captureSchemaHasWriteOnlyField(bundle map[string]any, schemaName string) bool {
	for _, raw := range captureSchemaProperties(bundle, schemaName) {
		property, _ := raw.(map[string]any)
		if writeOnly, _ := property["writeOnly"].(bool); writeOnly {
			return true
		}
	}
	return false
}

func captureSchemaDisclosesValue(bundle map[string]any, schemaName string) bool {
	_, ok := captureSchemaProperties(bundle, schemaName)["secret_value"]
	return ok
}

func captureSchemaProperties(bundle map[string]any, schemaName string) map[string]any {
	components, _ := bundle["components"].(map[string]any)
	schemas, _ := components["schemas"].(map[string]any)
	schema, _ := schemas[schemaName].(map[string]any)
	properties, _ := schema["properties"].(map[string]any)
	return properties
}
