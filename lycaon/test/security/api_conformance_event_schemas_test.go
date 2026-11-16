package security

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

// eventSchemaBase is the $id prefix of docs/schemas/events; it and every
// relative reference under it resolve to files below the repository's docs/.
const eventSchemaBase = "https://paintedwolf.dev/"

// eventSchemas compiles one payload schema per topic on first use.
type eventSchemas struct {
	compiler *jsonschema.Compiler
	compiled map[wire.EventTopic]*jsonschema.Schema
	failed   map[wire.EventTopic]error
}

// newEventSchemas reads docs/schemas/events/<topic>.json and the OpenAPI
// component files they reference from docsDir.
func newEventSchemas(docsDir string) *eventSchemas {
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	compiler.UseLoader(docsLoader{dir: docsDir})
	return &eventSchemas{compiler: compiler, compiled: map[wire.EventTopic]*jsonschema.Schema{}, failed: map[wire.EventTopic]error{}}
}

func (e *eventSchemas) schema(topic wire.EventTopic) (*jsonschema.Schema, error) {
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

// validate checks one payload against its topic's schema.
func (e *eventSchemas) validate(topic wire.EventTopic, data json.RawMessage) error {
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

// checkEventPayloads: every published payload validates against
// docs/schemas/events/<topic>.json.
func (s *conformanceSweep) checkEventPayloads(published []wire.EventEnvelope) {
	if s.eventSchemas == nil {
		s.findings.add(ruleEventPayloadsMatchSchemas, "sweep", "event schemas", "no event schemas were loaded")
		return
	}
	for _, env := range published {
		if err := s.eventSchemas.validate(env.Topic, env.Data); err != nil {
			s.findings.add(ruleEventPayloadsMatchSchemas, string(env.Topic), "payload", "%s", schemaErrorLine(err))
		}
	}
}

// schemaErrorLine names the first leaf of a validation error.
func schemaErrorLine(err error) string {
	var invalid *jsonschema.ValidationError
	if errors.As(err, &invalid) {
		for len(invalid.Causes) > 0 {
			invalid = invalid.Causes[0]
		}
		return "/" + strings.Join(invalid.InstanceLocation, "/") + ": " + firstLine(invalid.Error())
	}
	return firstLine(err.Error())
}
