// Package argv tokenizes command lines without a shell.
package argv

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ShellSyntaxChars require quoting within a command stage.
const ShellSyntaxChars = ";|&$`<>"

// SubstitutionChars require shell expansion and are rejected.
const SubstitutionChars = "$`"

var (
	// ErrCommandRequired reports a missing executable name.
	ErrCommandRequired = errors.New("command name is required")
	// ErrEnvAssignmentCommand reports environment syntax with no executable.
	ErrEnvAssignmentCommand = errors.New("environment assignments belong in env")
	// ErrShellMetacharacters reports unquoted shell syntax.
	ErrShellMetacharacters = errors.New("command contains unquoted shell metacharacters")
	// ErrUnterminatedQuote reports an open quote.
	ErrUnterminatedQuote = errors.New("unterminated quote in command")
	// ErrUnquotedNewline reports a line break outside quotes.
	ErrUnquotedNewline = errors.New("command contains a line break outside quotes; run one command per call")
	// ErrRedirectionTargetRequired reports a missing redirection target path.
	ErrRedirectionTargetRequired = errors.New("redirection operator missing target file")
)

// MetacharacterError names the unquoted shell syntax a stage cannot carry.
type MetacharacterError struct {
	Char string
	Var  string
}

func (e *MetacharacterError) Error() string {
	return fmt.Sprintf("%s: `%s`", ErrShellMetacharacters, e.Char)
}

// Is lets errors.Is match ErrShellMetacharacters.
func (e *MetacharacterError) Is(target error) bool { return target == ErrShellMetacharacters }

// ContainsShellMetacharacters checks one argument for shell syntax.
func ContainsShellMetacharacters(s string) bool {
	return strings.ContainsAny(s, ShellSyntaxChars)
}

// SplitCommandLine parses one shell-free stage and preserves quoted literals.
// Leading NAME=value assignments are parsed into env; the first subsequent token
// is the executable name. Redirections are validated and omitted.
func SplitCommandLine(line string) (map[string]string, string, []string, error) {
	el, err := SplitElement(line)
	if err != nil {
		return nil, "", nil, err
	}
	return el.Env, el.Name, el.Args, nil
}

// IsEnvIdentifier reports whether s is a valid POSIX environment variable name.
func IsEnvIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (i > 0 && r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

// JoinCommandLine round-trips argv through SplitCommandLine.
func JoinCommandLine(env map[string]string, name string, args []string) string {
	return JoinAddressedCommandLine(env, name, args, nil)
}

// JoinAddressedCommandLine renders like JoinCommandLine and also round-trips
// each argument's address marking, aligned with args: an argument that begins
// with `@` and is not marked renders quoted, so a literal stays literal. A nil
// addressed renders as JoinCommandLine does.
func JoinAddressedCommandLine(env map[string]string, name string, args []string, addressed []bool) string {
	var parts []string
	if len(env) > 0 {
		keys := make([]string, 0, len(env))
		for k := range env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			parts = append(parts, k+"="+quoteCommandLineArg(env[k]))
		}
	}
	if eq := strings.IndexByte(name, '='); eq > 0 && IsEnvIdentifier(name[:eq]) {
		// An unquoted NAME=value would parse back as an assignment.
		parts = append(parts, `"`+name+`"`)
	} else {
		parts = append(parts, quoteCommandLineArg(name))
	}
	for i, arg := range args {
		literalAddress := addressed != nil && strings.HasPrefix(arg, "@") && (i >= len(addressed) || !addressed[i])
		if literalAddress {
			parts = append(parts, doubleQuote(arg))
			continue
		}
		parts = append(parts, quoteCommandLineArg(arg))
	}
	return strings.Join(parts, " ")
}

// needsQuoting reports whether s needs quotes to round-trip as one argument.
func needsQuoting(s string) bool {
	if strings.ContainsAny(s, `"'\\`+ShellSyntaxChars+GlobChars) {
		return true
	}
	return strings.IndexFunc(s, func(r rune) bool {
		return argSeparator(r) || trimmedEdge(r)
	}) >= 0
}

func quoteCommandLineArg(s string) string {
	if s == "" {
		return `""`
	}
	if needsQuoting(s) {
		return doubleQuote(s)
	}
	return s
}

func doubleQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
