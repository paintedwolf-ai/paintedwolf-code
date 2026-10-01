package check

import (
	"bytes"
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

// Contract fixtures use the bundled workflow catalog.
func CatalogRegistry(t *testing.T) *workflowdef.Registry {
	t.Helper()
	reg, err := workflowdef.RegistryFromDirs("")
	FailErr(t, "load catalog workflow registry", err)
	return reg
}

func ReadRepoFile(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	FailErr(t, "read "+rel, err)
	return string(b)
}

func IsErrorCodeShape(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
			if i == 0 {
				return false
			}
		case r == '_':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func ActivateStockCatalog(t *testing.T) {
	t.Helper()
	eff := StockCatalog(t)
	extpacks.SetActive(eff)
	t.Cleanup(extpacks.ClearActive)
	prompts.ResetPersonaContractCache()
}

func StockCatalog(t *testing.T) *extpacks.EffectiveCatalog {
	t.Helper()
	content, err := extpacks.DiscoverStockContent()
	FailErr(t, "DiscoverStockContent", err)
	return extpacks.Resolve(t.Context(), extpacks.ResolveInput{
		Packs:   content,
		Desired: extpacks.EmptyDesired(),
	})
}

func DoJSON(t *testing.T, client *http.Client, method, url string, body any, wantStatus int, dest any) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, url, r)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != wantStatus {
		b, _ := io.ReadAll(resp.Body)
		body := string(b)
		if len(body) > 512 {
			body = body[:512] + "…"
		}
		t.Fatalf("%s %s: HTTP %d want %d\nbody: %s", method, url, resp.StatusCode, wantStatus, body)
	}
	if dest == nil {
		return
	}
	if err := json.NewDecoder(resp.Body).Decode(dest); err != nil {
		t.Fatalf("%s %s: decode JSON response: %v", method, url, err)
	}
}

func AssertJSONRoundTrip(t *testing.T, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("unmarshal round-trip: %v", err)
	}
}

// ParseNonTestGoTree parses the production Go files under dir and its
// subpackages, skipping testdata.
func ParseNonTestGoTree(t *testing.T, dir string) (*token.FileSet, []*ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	var files []*ast.File
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		name := d.Name()
		if d.IsDir() {
			if name == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		files = append(files, f)
		return nil
	})
	FailErr(t, "parse "+dir, err)
	return fset, files
}

func BundledPromptEngineForRoot(t *testing.T) prompts.PromptTemplateEngine {
	t.Helper()
	return prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: filepath.Join(RepoRoot(t), "lycaon")})
}

func ProdToolBoundary(t *testing.T) *sandbox.Boundary {
	t.Helper()
	cfg, err := sandbox.LoadConfig()
	FailErr(t, "load sandbox config", err)
	profiles, err := sandbox.LoadToolProfiles()
	FailErr(t, "load tool profiles", err)
	return sandbox.NewBoundary(cfg, profiles)
}
