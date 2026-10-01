package gitexec

import (
	"fmt"
	"os"
	"strings"
)

var executablePath = os.Executable

func resolveCredentialHelper(helper string) (string, error) {
	helper = strings.TrimSpace(helper)
	if helper != "osxkeychain" {
		return helper, nil
	}
	executable, err := executablePath()
	if err != nil {
		return "", fmt.Errorf("resolve built-in macOS Keychain credential helper: %w", err)
	}
	// Shell snippets require the executable path as one quoted token.
	return "!" + shellQuote(executable) + " git-credential-osxkeychain", nil
}
