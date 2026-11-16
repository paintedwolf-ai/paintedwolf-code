package bindings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

// LoadDir loads every *.yaml / *.yml binding under dir. Duplicate keys across
// files fail closed. Filename stem must equal the document id.
func LoadDir(dir string) ([]Binding, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []Binding
	seenKeys := map[string]string{} // key -> binding id
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		path := filepath.Join(dir, name)
		// #nosec G304 -- name is a direct child returned by ReadDir.
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		stem := strings.TrimSuffix(name, filepath.Ext(name))
		b, err := LoadBytes(stem, raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if b.ID != stem {
			return nil, fmt.Errorf("%s: id %q must equal filename stem %q", name, b.ID, stem)
		}
		for _, f := range b.Fields {
			if prev, ok := seenKeys[f.Key]; ok {
				return nil, fmt.Errorf("duplicate field key %q in bindings %q and %q", f.Key, prev, b.ID)
			}
			seenKeys[f.Key] = b.ID
		}
		out = append(out, b)
	}
	return out, nil
}

// LoadBytes parses and validates one binding document. idHint is the expected
// id (usually the filename stem); when non-empty it must match the document id.
func LoadBytes(idHint string, raw []byte) (Binding, error) {
	var b Binding
	// Strict: `equals` is the one transform a field may carry, so a misspelling
	// fails here rather than compiling into a projection that does nothing.
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&b); err != nil && !errors.Is(err, io.EOF) {
		return Binding{}, fmt.Errorf("yaml: %w", err)
	}
	if strings.TrimSpace(b.ID) == "" {
		return Binding{}, fmt.Errorf("missing id")
	}
	if idHint != "" && b.ID != idHint {
		return Binding{}, fmt.Errorf("id %q != hint %q", b.ID, idHint)
	}
	if strings.TrimSpace(b.ProviderID) == "" {
		return Binding{}, fmt.Errorf("missing provider_id")
	}
	if strings.TrimSpace(b.ToolName) == "" {
		return Binding{}, fmt.Errorf("missing tool_name")
	}
	if len(b.Schema) == 0 {
		return Binding{}, fmt.Errorf("missing schema")
	}
	seen := map[string]struct{}{}
	for i := range b.Fields {
		f := &b.Fields[i]
		if err := validateFieldKey(f.Key); err != nil {
			return Binding{}, err
		}
		if err := validateFieldType(f.Type); err != nil {
			return Binding{}, fmt.Errorf("field %q: %w", f.Key, err)
		}
		if strings.TrimSpace(f.Path) == "" {
			return Binding{}, fmt.Errorf("field %q: empty path", f.Key)
		}
		if !strings.HasPrefix(f.Path, "/") {
			return Binding{}, fmt.Errorf("field %q: path must be JSON Pointer starting with /", f.Key)
		}
		if f.Equals != "" && f.Type != TypeBool {
			return Binding{}, fmt.Errorf("field %q: equals only allowed with type bool", f.Key)
		}
		if _, ok := seen[f.Key]; ok {
			return Binding{}, fmt.Errorf("duplicate field key %q within binding", f.Key)
		}
		seen[f.Key] = struct{}{}
	}
	sch, err := compileSchema(b.ID, b.Schema)
	if err != nil {
		return Binding{}, fmt.Errorf("schema: %w", err)
	}
	b.compiled = sch
	return b, nil
}

func compileSchema(id string, schema map[string]any) (*jsonschema.Schema, error) {
	schemaBytes, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	var doc any
	if err := json.Unmarshal(schemaBytes, &doc); err != nil {
		return nil, err
	}
	url := "lycaon://mcp-bindings/" + id + ".json"
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(url, doc); err != nil {
		return nil, err
	}
	return compiler.Compile(url)
}

func (b Binding) schema() *jsonschema.Schema {
	if b.compiled == nil {
		return nil
	}
	sch, _ := b.compiled.(*jsonschema.Schema)
	return sch
}

func validateInstance(sch *jsonschema.Schema, obj map[string]any) error {
	if sch == nil {
		return fmt.Errorf("nil schema")
	}
	raw, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	return sch.Validate(inst)
}
