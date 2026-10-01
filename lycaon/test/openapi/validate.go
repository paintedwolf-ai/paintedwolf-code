package openapi

import (
	"context"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"
)

var (
	loadOnce sync.Once
	bundled  *Validator
	loadErr  error
)

// RepoRoot walks up from cwd until docs/openapi.yaml exists.
func RepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "docs", "openapi.yaml")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("repo root not found (no docs/openapi.yaml)")
		}
		dir = parent
	}
}

func load() (*Validator, error) {
	loadOnce.Do(func() {
		root, err := RepoRoot()
		if err != nil {
			loadErr = err
			return
		}
		loader := openapi3.NewLoader()
		loader.IsExternalRefsAllowed = true
		doc, err := loader.LoadFromFile(filepath.Join(root, "docs", "openapi.yaml"))
		if err != nil {
			loadErr = err
			return
		}
		bundled, loadErr = NewValidator(doc)
	})
	return bundled, loadErr
}

// Validator checks responses against one OpenAPI document.
type Validator struct {
	doc    *openapi3.T
	router routers.Router
}

// NewValidator validates doc and builds its router.
func NewValidator(doc *openapi3.T) (*Validator, error) {
	if err := doc.Validate(context.Background()); err != nil {
		return nil, err
	}
	router, err := gorillamux.NewRouter(doc)
	if err != nil {
		return nil, err
	}
	return &Validator{doc: doc, router: router}, nil
}

// Bundled returns the validator for docs/openapi.yaml (loaded once per process).
func Bundled() (*Validator, error) {
	return load()
}

// Document returns the bundled OpenAPI document (loaded once per process).
func Document() (*openapi3.T, error) {
	v, err := load()
	if err != nil {
		return nil, err
	}
	return v.doc, nil
}

// Router returns the gorillamux router for the bundled spec.
func Router() (routers.Router, error) {
	v, err := load()
	if err != nil {
		return nil, err
	}
	return v.router, nil
}

// ValidateResponse checks status, headers, and JSON body against the bundled spec.
func ValidateResponse(ctx context.Context, method, pathTemplate string, pathParams map[string]string, status int, header http.Header, body []byte) error {
	v, err := load()
	if err != nil {
		return err
	}
	return v.ValidateResponse(ctx, method, pathTemplate, pathParams, status, header, body)
}

func resolvePath(pathTemplate string, pathParams map[string]string) string {
	out := pathTemplate
	for key, val := range pathParams {
		out = strings.ReplaceAll(out, "{"+key+"}", url.PathEscape(val))
	}
	return out
}

func (v *Validator) findRoute(method, pathTemplate string, pathParams map[string]string) (*routers.Route, map[string]string, *http.Request, error) {
	u, err := url.Parse("http://127.0.0.1:8787" + resolvePath(pathTemplate, pathParams))
	if err != nil {
		return nil, nil, nil, err
	}
	req, err := http.NewRequestWithContext(context.Background(), method, u.String(), nil)
	if err != nil {
		return nil, nil, nil, err
	}
	route, params, err := v.router.FindRoute(req)
	if err != nil {
		return nil, nil, nil, err
	}
	merged := make(map[string]string, len(pathParams)+len(params))
	for k, val := range pathParams {
		merged[k] = val
	}
	for k, val := range params {
		merged[k] = val
	}
	return route, merged, req, nil
}

// ValidateResponse checks status, headers, and body against the document. A
// body whose media type is not JSON is checked only against the declared media
// types, since the validator decodes JSON alone.
func (v *Validator) ValidateResponse(ctx context.Context, method, pathTemplate string, pathParams map[string]string, status int, header http.Header, body []byte) error {
	route, merged, req, err := v.findRoute(method, pathTemplate, pathParams) //nolint:contextcheck // routing is a pure lookup; ValidateResponse below takes ctx
	if err != nil {
		return err
	}
	respInput := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: &openapi3filter.RequestValidationInput{Request: req, PathParams: merged, Route: route},
		Status:                 status,
		Header:                 header,
		Options:                &openapi3filter.Options{IncludeResponseStatus: true},
	}
	if mediaType := responseMediaType(header); len(body) > 0 && mediaType != "application/json" {
		if err := declaresMediaType(route.Operation, status, mediaType); err != nil {
			return err
		}
		respInput.Options.ExcludeResponseBody = true
	}
	if len(body) > 0 {
		respInput.SetBodyBytes(body)
	}
	return openapi3filter.ValidateResponse(ctx, respInput)
}

func responseMediaType(header http.Header) string {
	mediaType, _, err := mime.ParseMediaType(header.Get("Content-Type"))
	if err != nil {
		return ""
	}
	return mediaType
}

func declaresMediaType(op *openapi3.Operation, status int, mediaType string) error {
	ref := op.Responses.Status(status)
	if ref == nil || ref.Value == nil {
		return fmt.Errorf("status %d is not declared", status)
	}
	for declared := range ref.Value.Content {
		if declared == mediaType {
			return nil
		}
		if prefix, ok := strings.CutSuffix(declared, "/*"); ok && strings.HasPrefix(mediaType, prefix+"/") {
			return nil
		}
	}
	return fmt.Errorf("status %d does not declare media type %q", status, mediaType)
}
