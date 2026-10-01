package wirespec

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/yaml.v3"
)

type wireSpecCache struct {
	mu sync.Mutex
	// keyed by absolute repo root
	openAPI map[string]*cachedOpenAPI
	ts      map[string]string
	goAPI   map[string]apiStructScan
}

var wireCache wireSpecCache

type cachedOpenAPI struct {
	objectSchemas map[string]openAPISchemaDoc
	propertySpecs map[string]map[string]bool
}

func cachedOpenAPIDoc(repoRoot string) (*cachedOpenAPI, error) {
	repoRoot = filepath.Clean(repoRoot)
	wireCache.mu.Lock()
	if wireCache.openAPI == nil {
		wireCache.openAPI = make(map[string]*cachedOpenAPI)
	}
	if doc, ok := wireCache.openAPI[repoRoot]; ok {
		wireCache.mu.Unlock()
		return doc, nil
	}
	wireCache.mu.Unlock()

	data, err := os.ReadFile(filepath.Join(repoRoot, "docs", "openapi.yaml"))
	if err != nil {
		return nil, err
	}
	var raw struct {
		Components struct {
			Schemas map[string]struct {
				Type       string         `yaml:"type"`
				Properties map[string]any `yaml:"properties"`
				Required   []string       `yaml:"required"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	doc := &cachedOpenAPI{
		objectSchemas: make(map[string]openAPISchemaDoc),
		propertySpecs: make(map[string]map[string]bool),
	}
	for name, schema := range raw.Components.Schemas {
		// A published object with no properties is still published.
		if schema.Type == "object" {
			doc.objectSchemas[name] = openAPISchemaDoc{
				Type:       schema.Type,
				Properties: schema.Properties,
			}
		}
		required := make(map[string]struct{}, len(schema.Required))
		for _, r := range schema.Required {
			required[r] = struct{}{}
		}
		props := make(map[string]bool, len(schema.Properties))
		for propName := range schema.Properties {
			_, req := required[propName]
			props[propName] = !req
		}
		doc.propertySpecs[name] = props
	}

	wireCache.mu.Lock()
	wireCache.openAPI[repoRoot] = doc
	wireCache.mu.Unlock()
	return doc, nil
}

func CachedOpenAPIPropertySpecs(repoRoot, schema string) (map[string]bool, error) {
	doc, err := cachedOpenAPIDoc(repoRoot)
	if err != nil {
		return nil, err
	}
	props, ok := doc.propertySpecs[schema]
	if !ok {
		return nil, fmt.Errorf("schema %q not found", schema)
	}
	return props, nil
}

func cachedTSContent(repoRoot string) (string, error) {
	repoRoot = filepath.Clean(repoRoot)
	wireCache.mu.Lock()
	if wireCache.ts == nil {
		wireCache.ts = make(map[string]string)
	}
	if content, ok := wireCache.ts[repoRoot]; ok {
		wireCache.mu.Unlock()
		return content, nil
	}
	wireCache.mu.Unlock()

	data, err := os.ReadFile(filepath.Join(repoRoot, "lycaon-den", "src", "api", "types.ts"))
	if err != nil {
		return "", err
	}
	content := string(data)

	wireCache.mu.Lock()
	wireCache.ts[repoRoot] = content
	wireCache.mu.Unlock()
	return content, nil
}

func CachedDiscoverAPIStructFields(repoRoot string) (apiStructScan, error) {
	repoRoot = filepath.Clean(repoRoot)
	wireCache.mu.Lock()
	if wireCache.goAPI == nil {
		wireCache.goAPI = make(map[string]apiStructScan)
	}
	if scan, ok := wireCache.goAPI[repoRoot]; ok {
		wireCache.mu.Unlock()
		return scan, nil
	}
	wireCache.mu.Unlock()

	scan, err := discoverAPIStructFields(repoRoot)
	if err != nil {
		return apiStructScan{}, err
	}

	wireCache.mu.Lock()
	wireCache.goAPI[repoRoot] = scan
	wireCache.mu.Unlock()
	return scan, nil
}
