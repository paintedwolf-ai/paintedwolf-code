package toolcontract

import "sort"

// SecretSurface names the outbound screen that reviews a tool's resolved
// managed-secret references. Values equal secretmatch screen surfaces.
type SecretSurface string

const (
	SecretSurfaceNone        SecretSurface = ""
	SecretSurfaceCommand     SecretSurface = "command"
	SecretSurfaceTerminal    SecretSurface = "terminal"
	SecretSurfaceHTTPRequest SecretSurface = "http_request"
	SecretSurfaceMCP         SecretSurface = "mcp"
	SecretSurfaceFile        SecretSurface = "file"
)

// ProcessArguments reports whether resolved values reach a host process
// through tool arguments, screened by the executor before dispatch.
func (s SecretSurface) ProcessArguments() bool {
	return s == SecretSurfaceCommand || s == SecretSurfaceTerminal
}

// IsFile reports whether resolved values reach a file written to disk.
func (s SecretSurface) IsFile() bool {
	return s == SecretSurfaceFile
}

// AcceptsSecretReferences reports whether arguments resolve managed-secret references.
func (c Contract) AcceptsSecretReferences() bool {
	return c.SecretReferenceSurface != SecretSurfaceNone
}

// SecretReferenceTools returns the sorted catalog tools that resolve references.
func SecretReferenceTools() []string {
	var out []string
	for name, contract := range compiledContracts {
		if contract.AcceptsSecretReferences() {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
