package toolcontract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
)

var operationLanguage = sync.OnceValues(LanguageV1)

// LanguageCatalogDigest identifies exact frozen compiled language facts.
var languageDigest = sync.OnceValue(func() string {
	sum := sha256.Sum256([]byte(languageV1JSON))
	return hex.EncodeToString(sum[:])
})

func LanguageCatalogDigest() string { return languageDigest() }

// ToolOperation compiles a retained interface from its existing schema and
// invocation contract, attaching selected command forms from the same catalog.
// This declaration grants no capability to execute it.
func ToolOperation(name string, schema json.RawMessage, contract Contract) (OperationDefinition, error) {
	catalog, err := operationLanguage()
	if err != nil {
		return OperationDefinition{}, err
	}
	var commands []CommandSpecification
	for _, disposition := range catalog.Tools {
		if disposition.Tool != name {
			continue
		}
		for _, command := range catalog.Commands {
			for _, declared := range disposition.CommandForms {
				selected := command.Name == declared
				for _, alias := range command.Aliases {
					selected = selected || alias == declared
				}
				if selected {
					commands = append(commands, command)
					break
				}
			}
		}
	}
	return NewOperationDefinition(OperationFacts{
		Identity: name, Version: 1, Contract: contract, Arguments: schema, Commands: commands,
		Streams:   StreamContract{Input: "typed arguments; command byte contract per attached form", Output: "retained owner projection; command byte encoding per form", Error: "typed owner rejection; prose is diagnostic", Statuses: map[int]string{0: "owner terminal outcome; command statuses per form"}},
		Resources: catalog.Language.Limits, ProgressGate: ProgressExistingAdmission, RejectionContract: name,
	})
}
