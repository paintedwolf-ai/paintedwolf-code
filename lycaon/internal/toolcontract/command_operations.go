package toolcontract

import (
	"slices"
	"sync"
)

// RoutineSchemaRetired is the catalog disposition of a tool whose routine
// work runs through command. Its contract stays the operation that command
// programs bind; it is never offered to a model as a tool.
const RoutineSchemaRetired = "replace-routine-schema"

var commandOperations = sync.OnceValues(func() (map[string]bool, error) {
	catalog, err := operationLanguage()
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, disposition := range catalog.Tools {
		if disposition.Disposition == RoutineSchemaRetired {
			out[disposition.Tool] = true
		}
	}
	return out, nil
})

// IsCommandOperation reports whether name is served only through command
// programs. The compiled catalog is validated at generation, so a decode
// failure classifies nothing.
func IsCommandOperation(name string) bool {
	operations, err := commandOperations()
	return err == nil && operations[name]
}

// CommandOperations lists the command-only operation identities in name order.
func CommandOperations() ([]string, error) {
	operations, err := commandOperations()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(operations))
	for name := range operations {
		out = append(out, name)
	}
	slices.Sort(out)
	return out, nil
}
