package fspath

// NameGrammar is the platform's grammar for native path operands.
type NameGrammar uint8

const (
	// POSIXNames are exact bytes separated by '/'.
	POSIXNames NameGrammar = iota
	// WindowsNames add drives, UNC shares, device namespaces, reserved device
	// names and the trailing dot and space aliases.
	WindowsNames
)

// OperandNames are the facts a platform declares about native path operands.
type OperandNames struct {
	Grammar NameGrammar
	// UTF8 requires every operand to be valid UTF-8.
	UTF8 bool
	// NullDevice is the platform's own name for the null device beside the
	// portable /dev/null, matched without regard to case; empty when none.
	NullDevice string
}

// LinuxOperandNames are Linux's declared operand facts: exact bytes.
func LinuxOperandNames() OperandNames { return OperandNames{Grammar: POSIXNames} }

// DarwinOperandNames are macOS's declared operand facts: names must be UTF-8.
func DarwinOperandNames() OperandNames { return OperandNames{Grammar: POSIXNames, UTF8: true} }

// WindowsOperandNames are Windows's declared operand facts.
func WindowsOperandNames() OperandNames {
	return OperandNames{Grammar: WindowsNames, UTF8: true, NullDevice: "NUL"}
}
