// Package gitargv validates user-supplied strings that are about to become git argv.
package gitargv

import (
	"fmt"
	"strings"
)

// unsafeTransportPrefixes are git remote-helper schemes that execute commands named in URLs.
var unsafeTransportPrefixes = []string{"ext::", "fd::"}

// ValidateCloneURL rejects a clone URL that git would parse as an option, and the
// remote-helper transports that turn a URL into arbitrary command execution.
func ValidateCloneURL(url string) error {
	trimmed := strings.TrimSpace(url)
	if trimmed == "" {
		return fmt.Errorf("url is required")
	}
	if strings.HasPrefix(trimmed, "-") {
		return fmt.Errorf("invalid url %q", url)
	}
	lower := strings.ToLower(trimmed)
	for _, prefix := range unsafeTransportPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return fmt.Errorf("refusing unsafe git transport in url")
		}
	}
	return nil
}

// LocalCloneSourcePath reports the on-disk path a clone or ls-remote source names for local sources.
func LocalCloneSourcePath(source string) (string, bool) {
	trimmed := strings.TrimSpace(source)
	if trimmed == "" || strings.HasPrefix(trimmed, "-") {
		return "", false
	}
	// Git reads file:// only in lowercase; another case names a remote helper.
	if rest, ok := strings.CutPrefix(trimmed, "file://"); ok {
		// Git ignores a file URL's host and decodes the path from the first
		// slash after it: file:///p and file://localhost/p both name /p.
		slash := strings.IndexByte(rest, '/')
		if slash < 0 {
			return "", false
		}
		return gitURLDecode(rest[slash:]), true
	}
	if strings.Contains(trimmed, "://") {
		return "", false
	}
	// A colon before the first slash indicates scp-like remote syntax.
	head := trimmed
	if slash := strings.IndexByte(trimmed, '/'); slash >= 0 {
		head = trimmed[:slash]
	}
	if strings.Contains(head, ":") {
		return "", false
	}
	return trimmed, true
}

// gitURLDecode decodes a file URL's path as Git does: %XX becomes its byte
// and a % without two hex digits stays as written.
func gitURLDecode(path string) string {
	var out strings.Builder
	for i := 0; i < len(path); i++ {
		if path[i] == '%' && i+2 < len(path) && isHex(path[i+1]) && isHex(path[i+2]) {
			out.WriteByte(unhex(path[i+1])<<4 | unhex(path[i+2]))
			i += 2
			continue
		}
		out.WriteByte(path[i])
	}
	return out.String()
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

func unhex(c byte) byte {
	switch {
	case c <= '9':
		return c - '0'
	case c <= 'F':
		return c - 'A' + 10
	default:
		return c - 'a' + 10
	}
}

// ValidateRefArg rejects a ref that git would parse as an option.
func ValidateRefArg(ref string) error {
	if strings.HasPrefix(strings.TrimSpace(ref), "-") {
		return fmt.Errorf("invalid git ref %q", ref)
	}
	return nil
}

// EndOfOptions terminates option parsing for subsequent arguments.
const EndOfOptions = "--end-of-options"
