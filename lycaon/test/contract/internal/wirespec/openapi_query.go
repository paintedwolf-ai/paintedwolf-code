package wirespec

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type openAPIQueryParam struct {
	Name       string `yaml:"name"`
	In         string `yaml:"in"`
	Deprecated bool   `yaml:"deprecated"`
	Ref        string `yaml:"$ref"`
}

type openAPIOperationParams struct {
	OperationID string              `yaml:"operationId"`
	Parameters  []openAPIQueryParam `yaml:"parameters"`
}

type openAPIPathsParamsDoc struct {
	Paths      map[string]map[string]openAPIOperationParams `yaml:"paths"`
	Components struct {
		Parameters map[string]openAPIQueryParam `yaml:"parameters"`
	} `yaml:"components"`
}

type openAPIRouteQuery struct {
	Method      string
	Path        string
	OperationID string
	QueryParams []string
}

var ForbiddenOpenAPIQueryNames = map[string]struct{}{
	"project":     {},
	"project_dir": {},
	"session":     {},
}

func LoadOpenAPIRouteQueries(repoRoot string) ([]openAPIRouteQuery, error) {
	data, err := os.ReadFile(filepath.Join(repoRoot, "docs", "openapi.yaml"))
	if err != nil {
		return nil, err
	}
	var doc openAPIPathsParamsDoc
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}

	var routes []openAPIRouteQuery
	for path, methods := range doc.Paths {
		for method, op := range methods {
			method = strings.ToUpper(method)
			if method == "PARAMETERS" || method == "SERVERS" {
				continue
			}
			var names []string
			for _, p := range op.Parameters {
				if p.In != "" && p.In != "query" {
					continue
				}
				if p.Ref != "" {
					refName := strings.TrimPrefix(p.Ref, "#/components/parameters/")
					if comp, ok := doc.Components.Parameters[refName]; ok {
						if comp.In == "query" || comp.In == "" {
							names = append(names, comp.Name)
						}
					}
					continue
				}
				if p.In == "query" {
					names = append(names, p.Name)
				}
			}
			routes = append(routes, openAPIRouteQuery{
				Method:      method,
				Path:        path,
				OperationID: op.OperationID,
				QueryParams: names,
			})
		}
	}
	return routes, nil
}
