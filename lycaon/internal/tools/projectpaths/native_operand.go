package projectpaths

import (
	"errors"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/fspath"
)

// IsNullOperand identifies native device operands only. Substrings in external
// code, URLs or arbitrary arguments are never passed through this classifier.
func IsNullOperand(operand string) bool { return NullOperand(operand, fspath.HostOperandNames()) }

// NullOperand reports whether operand names the null device under names.
func NullOperand(operand string, names fspath.OperandNames) bool {
	return operand == "/dev/null" || (names.NullDevice != "" && strings.EqualFold(operand, names.NullDevice))
}

// ValidateNativeOperand refuses values the running platform cannot represent.
func ValidateNativeOperand(operand string) error {
	return ValidateOperand(operand, fspath.HostOperandNames())
}

// ValidateOperand checks representation under a platform's declared operand
// facts without rewriting path bytes. POSIX names stay exact bytes.
func ValidateOperand(operand string, names fspath.OperandNames) error {
	if operand == "" || strings.IndexByte(operand, 0) >= 0 {
		return invalidOperand(operand, "empty path or NUL in path")
	}
	if names.UTF8 && !utf8.ValidString(operand) {
		return invalidOperand(operand, "native path is not representable as UTF-8")
	}
	if names.Grammar != fspath.WindowsNames {
		return nil
	}
	return validateWindowsOperand(operand, names)
}

func validateWindowsOperand(operand string, names fspath.OperandNames) error {
	isSeparator := func(b byte) bool { return b == '/' || b == '\\' }
	unc := len(operand) >= 2 && isSeparator(operand[0]) && isSeparator(operand[1])
	if len(operand) >= 4 && unc && (operand[2] == '?' || operand[2] == '.') && isSeparator(operand[3]) {
		return invalidOperand(operand, "Windows device namespace is not an ordinary path")
	}
	if len(operand) >= 2 && operand[1] == ':' {
		if !((operand[0] >= 'A' && operand[0] <= 'Z') || (operand[0] >= 'a' && operand[0] <= 'z')) {
			return invalidOperand(operand, "invalid drive name")
		}
		if len(operand) == 2 || !isSeparator(operand[2]) {
			return invalidOperand(operand, "drive-relative path requires explicit drive root")
		}
	}
	if unc {
		parts := strings.FieldsFunc(operand[2:], func(r rune) bool { return r == '\\' || r == '/' })
		if len(parts) < 2 {
			return invalidOperand(operand, "UNC path requires server and share")
		}
	} else if isSeparator(operand[0]) && operand != "/dev/null" {
		return invalidOperand(operand, "rooted Windows path requires an explicit drive")
	}
	for _, part := range strings.FieldsFunc(operand, func(r rune) bool { return r == '\\' || r == '/' }) {
		if part == "." || part == ".." || (len(part) == 2 && part[1] == ':') {
			continue
		}
		if strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return invalidOperand(operand, "Windows aliases a trailing space or dot")
		}
		stem, _, _ := strings.Cut(part, ".")
		stem = strings.ToUpper(stem)
		reserved := stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL"
		if len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '1' && stem[3] <= '9' {
			reserved = true
		}
		if reserved && !NullOperand(operand, names) {
			return invalidOperand(operand, "reserved Windows device name")
		}
		if strings.ContainsAny(part, `<>"|?*`) || (strings.Contains(part, ":") && !(len(part) == 2 && part[1] == ':')) {
			return invalidOperand(operand, "invalid Windows path component")
		}
		for _, r := range part {
			if r < 32 {
				return invalidOperand(operand, "control character in Windows path")
			}
		}
	}
	return nil
}

func invalidOperand(operand, reason string) error {
	return &PathError{Code: "COMMAND_OPERAND_INVALID", Operand: operand, Cause: errors.New(reason)}
}
func validateOutputPath(path string, nul bool) error {
	if !nul && !utf8.ValidString(path) {
		return invalidOperand(path, "scalar path is not representable as UTF-8")
	}
	return nil
}

// PathBase and PathDir are pure platform path operations with exact input bytes.
func PathBase(path, suffix string) string {
	if path == "" {
		return ""
	}
	base := filepath.Base(path)
	if suffix != "" && suffix != base {
		base = strings.TrimSuffix(base, suffix)
	}
	return base
}
func PathDir(path string) string { return filepath.Dir(path) }
