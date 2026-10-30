package toolcontract

import (
	"encoding/json"
	"fmt"
)

// ValidateNativeCommands binds native exposure to exact frozen public catalog
// facts. A registered structured handler with no command forms stays hidden.
func (d OperationDefinition) ValidateNativeCommands() error {
	facts := d.Facts()
	if d.Digest() == "" || len(facts.Commands) == 0 {
		return fmt.Errorf("native operation requires explicit public command forms")
	}
	catalog, err := operationLanguage()
	if err != nil {
		return err
	}
	for _, selected := range facts.Commands {
		found := false
		for _, declared := range catalog.Commands {
			if selected.Name != declared.Name {
				continue
			}
			//nolint:errchkjson // Facts is detached from the immutable definition whose complete JSON was validated at construction.
			selectedBytes, _ := json.Marshal(selected)
			//nolint:errchkjson // The private operationLanguage catalog is parsed JSON with no mutable escape or custom marshaler.
			declaredBytes, _ := json.Marshal(declared)
			if string(selectedBytes) != string(declaredBytes) || declared.Owner != facts.Contract.Owner {
				return fmt.Errorf("native command %q differs from frozen catalog", selected.Name)
			}
			found = true
			break
		}
		if !found {
			return fmt.Errorf("native command %q is not in the language catalog", selected.Name)
		}
	}
	return nil
}

// CommandPolicyIdentities preserves explicit existing operation denies after
// standalone schema retirement. These are catalog links, not program-name floors.
func CommandPolicyIdentities(command string) ([]string, error) {
	catalog, err := operationLanguage()
	if err != nil {
		return nil, err
	}
	identities := []string{command}
	for _, disposition := range catalog.Tools {
		for _, form := range disposition.CommandForms {
			if form == command {
				identities = append(identities, disposition.Tool)
				break
			}
		}
	}
	return identities, nil
}
