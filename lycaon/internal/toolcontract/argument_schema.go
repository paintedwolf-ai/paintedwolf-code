package toolcontract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

var argumentSchemas sync.Map

func compileArgumentSchema(schemaBytes []byte) (*jsonschema.Schema, error) {
	key := string(schemaBytes)
	if cached, ok := argumentSchemas.Load(key); ok {
		return cached.(*jsonschema.Schema), nil
	}
	var doc any
	if err := json.Unmarshal(schemaBytes, &doc); err != nil {
		return nil, fmt.Errorf("parse schema: %w", err)
	}
	const schemaURL = "lycaon://tool-args/schema.json"
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(schemaURL, doc); err != nil {
		return nil, fmt.Errorf("compile schema: %w", err)
	}
	schema, err := compiler.Compile(schemaURL)
	if err != nil {
		return nil, fmt.Errorf("compile schema: %w", err)
	}
	actual, _ := argumentSchemas.LoadOrStore(key, schema)
	return actual.(*jsonschema.Schema), nil
}

// ValidateArgumentSchema validates the declaration without exposing mutable compiled state.
func ValidateArgumentSchema(schemaBytes []byte) error {
	_, err := compileArgumentSchema(schemaBytes)
	return err
}

// ValidateArgumentInstance shares retained structured argument validation with
// typed operation declarations. Validation errors preserve existing schema facts.
func ValidateArgumentInstance(schemaBytes, arguments []byte) error {
	schema, err := compileArgumentSchema(schemaBytes)
	if err != nil {
		return err
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(arguments))
	if err != nil {
		return fmt.Errorf("parse args: %w", err)
	}
	return schema.Validate(instance)
}
