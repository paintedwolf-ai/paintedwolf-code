package projectpaths

import (
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"
)

// IsNullOperand identifies native device operands only. Substrings in external
// code, URLs or arbitrary arguments are never passed through this classifier.
func IsNullOperand(operand string) bool {
	return operand == "/dev/null" || (runtime.GOOS == "windows" && strings.EqualFold(operand, "NUL"))
}

// ValidateNativeOperand refuses values the current platform cannot represent.
// On Unix non-Unicode names remain bytes; Windows validation is also exposed
// independently for shared design fixtures, without claiming runtime parity.
func ValidateNativeOperand(operand string) error {
	return ValidatePlatformOperand(operand, runtime.GOOS)
}

func ValidatePlatformOperand(operand, platform string) error {
	if operand == "" || strings.IndexByte(operand, 0) >= 0 {
		return invalidOperand(operand, "empty path or NUL in path")
	}
	if platform != "windows" {
		return nil
	}
	if !utf8.ValidString(operand) {
		return invalidOperand(operand, "Windows path is not Unicode")
	}
	if strings.HasPrefix(operand, `\\?\`) || strings.HasPrefix(operand, `\\.\`) {
		return invalidOperand(operand, "Windows device namespace is not an ordinary path")
	}
	if len(operand) >= 2 && operand[1] == ':' && (len(operand) == 2 || (operand[2] != '\\' && operand[2] != '/')) {
		return invalidOperand(operand, "drive-relative path requires explicit drive root")
	}
	if strings.HasPrefix(operand, `\\`) {
		parts := strings.FieldsFunc(operand[2:], func(r rune) bool { return r == '\\' || r == '/' })
		if len(parts) < 2 {
			return invalidOperand(operand, "UNC path requires server and share")
		}
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
		if reserved && !strings.EqualFold(operand, "NUL") {
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
		return invalidOperand(path, "scalar path is not representable as UTF-8; use a NUL list")
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
