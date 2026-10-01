// Package browsertestsetup enables the staged browser for test binaries.
package browsertestsetup

import "github.com/lycaon/lycaon/internal/browserengine"

// Enable makes the staged browser available to a test binary.
func Enable() {
	browserengine.TestingEnableBundledBinary()
}
