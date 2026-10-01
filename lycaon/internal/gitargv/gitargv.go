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
	if rest, ok := cutPrefixFold(trimmed, "file://"); ok {
		if rest == "" {
			return "", false
		}
		return rest, true
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

func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) < len(prefix) || !strings.EqualFold(s[:len(prefix)], prefix) {
		return "", false
	}
	return s[len(prefix):], true
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
