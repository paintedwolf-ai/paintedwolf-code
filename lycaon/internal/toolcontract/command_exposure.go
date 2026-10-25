package toolcontract

import (
	"encoding/json"
	"fmt"
	"sync"
)

var validatedNativeCommands sync.Map

var catalogCommandBytes = sync.OnceValues(func() (map[string]string, error) {
	catalog, err := operationLanguage()
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(catalog.Commands))
	for _, declared := range catalog.Commands {
		b, _ := json.Marshal(declared)
		out[declared.Name] = string(b)
	}
	return out, nil
})

type nativeValidationResult struct {
	err error
}

// ValidateNativeCommands binds native exposure to exact frozen public catalog
// facts. A registered structured handler with no command forms stays hidden.
func (d OperationDefinition) ValidateNativeCommands() error {
	if d.Digest() == "" {
		return fmt.Errorf("native operation requires explicit public command forms")
	}
	if cached, ok := validatedNativeCommands.Load(d.Digest()); ok {
		return cached.(nativeValidationResult).err
	}
	err := d.validateNativeCommandsUncached()
	validatedNativeCommands.Store(d.Digest(), nativeValidationResult{err: err})
	return err
}

func (d OperationDefinition) validateNativeCommandsUncached() error {
	facts := d.ReadOnlyFacts()
	if len(facts.Commands) == 0 {
		return fmt.Errorf("native operation requires explicit public command forms")
	}
	catalog, err := operationLanguage()
	if err != nil {
		return err
	}
	declaredMap, err := catalogCommandBytes()
	if err != nil {
		return err
	}
	for _, selected := range facts.Commands {
		declaredBytes, ok := declaredMap[selected.Name]
		if !ok {
			return fmt.Errorf("native command %q is not in the language catalog", selected.Name)
		}
		var declaredOwner string
		for _, declared := range catalog.Commands {
			if declared.Name == selected.Name {
				declaredOwner = declared.Owner
				break
			}
		}
		selectedBytes, err := json.Marshal(selected)
		if err != nil {
			return fmt.Errorf("encode native command %q: %w", selected.Name, err)
		}
		if string(selectedBytes) != declaredBytes || declaredOwner != facts.Contract.Owner {
			return fmt.Errorf("native command %q differs from frozen catalog", selected.Name)
		}
	}
	return nil
}

var policyIdentitiesByCommand = sync.OnceValues(func() (map[string][]string, error) {
	catalog, err := operationLanguage()
	if err != nil {
		return nil, err
	}
	out := make(map[string][]string, len(catalog.Commands))
	for _, disposition := range catalog.Tools {
		for _, form := range disposition.CommandForms {
			if out[form] == nil {
				out[form] = []string{form}
			}
			out[form] = append(out[form], disposition.Tool)
		}
	}
	return out, nil
})

// CommandPolicyIdentities preserves explicit existing operation denies after
// standalone schema retirement. These are catalog links, not program-name floors.
func CommandPolicyIdentities(command string) ([]string, error) {
	index, err := policyIdentitiesByCommand()
	if err != nil {
		return nil, err
	}
	if ids, ok := index[command]; ok {
		return append([]string(nil), ids...), nil
	}
	return []string{command}, nil
}
