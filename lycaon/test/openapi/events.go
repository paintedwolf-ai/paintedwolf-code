package openapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

// eventSchemaBase is the $id prefix of docs/schemas/events; it and every
// relative reference under it resolve to files below the repository's docs/.
const eventSchemaBase = "https://paintedwolf.dev/"

// EventSchemas validates event payloads against
// docs/schemas/events/<topic>.json, compiling each topic once.
type EventSchemas struct {
	mu       sync.Mutex
	compiler *jsonschema.Compiler
	compiled map[wire.EventTopic]*jsonschema.Schema
	failed   map[wire.EventTopic]error
}

// NewEventSchemas reads the topic schemas and the OpenAPI component files
// they reference from docsDir.
func NewEventSchemas(docsDir string) *EventSchemas {
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	compiler.UseLoader(docsLoader{dir: docsDir})
	return &EventSchemas{compiler: compiler, compiled: map[wire.EventTopic]*jsonschema.Schema{}, failed: map[wire.EventTopic]error{}}
}

// BundledEventSchemas validates against this repository's docs.
func BundledEventSchemas() (*EventSchemas, error) {
	root, err := RepoRoot()
	if err != nil {
		return nil, err
	}
	return NewEventSchemas(filepath.Join(root, "docs")), nil
}

func (e *EventSchemas) schema(topic wire.EventTopic) (*jsonschema.Schema, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if sch, ok := e.compiled[topic]; ok {
		return sch, nil
	}
	if err, ok := e.failed[topic]; ok {
		return nil, err
	}
	sch, err := e.compiler.Compile(eventSchemaBase + "schemas/events/" + string(topic) + ".json")
	if err != nil {
		e.failed[topic] = err
		return nil, err
	}
	e.compiled[topic] = sch
	return sch, nil
}

// Validate checks one payload against its topic's schema.
func (e *EventSchemas) Validate(topic wire.EventTopic, data json.RawMessage) error {
	sch, err := e.schema(topic)
	if err != nil {
		return fmt.Errorf("no payload schema: %w", err)
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("payload is not JSON: %w", err)
	}
	return sch.Validate(value)
}

// SchemaErrorLine names the first leaf of a validation error.
func SchemaErrorLine(err error) string {
	var invalid *jsonschema.ValidationError
	if errors.As(err, &invalid) {
		for len(invalid.Causes) > 0 {
			invalid = invalid.Causes[0]
		}
		line, _, _ := strings.Cut(invalid.Error(), "\n")
		return "/" + strings.Join(invalid.InstanceLocation, "/") + ": " + line
	}
	line, _, _ := strings.Cut(err.Error(), "\n")
	return line
}

// docsLoader serves eventSchemaBase URLs from the docs tree; YAML component
// files are decoded as the JSON documents they describe.
type docsLoader struct{ dir string }

func (l docsLoader) Load(url string) (any, error) {
	rel, ok := strings.CutPrefix(url, eventSchemaBase)
	if !ok {
		return nil, fmt.Errorf("schema %s is outside %s", url, eventSchemaBase)
	}
	raw, err := os.ReadFile(filepath.Join(l.dir, filepath.FromSlash(rel)))
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(rel, ".yaml") {
		var doc any
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			return nil, err
		}
		if raw, err = json.Marshal(doc); err != nil {
			return nil, err
		}
	}
	return jsonschema.UnmarshalJSON(bytes.NewReader(raw))
}
