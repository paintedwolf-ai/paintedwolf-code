package documentcore

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/configlayout"
)

const (
	// EnvBinary names the core executable explicitly; verification and
	// development checkouts set it to the build of the current source.
	EnvBinary = "LYCAON_DOCUMENT_CORE_BINARY"
	// binaryName is the core executable shipped beside the host.
	binaryName = "pw-document-core"
)

// ErrBinaryMissing reports that no core executable is installed for this host.
var ErrBinaryMissing = errors.New("document core executable is not installed")

// Binary resolves the core executable: the explicit override, otherwise the
// sibling shipped with the host.
func Binary() (string, error) {
	if path := strings.TrimSpace(os.Getenv(EnvBinary)); path != "" {
		st, err := os.Stat(path) // #nosec G703 -- an explicit development override; the core it names runs confined
		if err != nil || st.IsDir() {
			return "", fmt.Errorf("%w: %s=%q", ErrBinaryMissing, EnvBinary, path)
		}
		return path, nil
	}
	if path := configlayout.SiblingExecutable(binaryName); path != "" {
		return path, nil
	}
	return "", fmt.Errorf("%w: no %s beside the host (build it with ./task build:document-core)", ErrBinaryMissing, binaryName)
}
