//go:build confinehelpermain

// Command helpermain is the confinement helper alone, dispatched from main. A
// coverage build of it records the helper's statements, which a test binary
// run as the helper never does.
package main

import (
	"encoding/hex"
	"fmt"
	"os"

	"github.com/lycaon/lycaon/internal/confine"
)

// coverageDirectoryHex is embedded by the coverage fixture build. The
// restricted performer deliberately inherits no environment.
var coverageDirectoryHex string

// A variable initializer runs before Go's generated coverage startup hook;
// an init function or main would set the directory after metadata emission.
var coverageDirectoryError = configureCoverageDirectory()

func configureCoverageDirectory() error {
	if coverageDirectoryHex == "" {
		return nil
	}
	dir, err := hex.DecodeString(coverageDirectoryHex)
	if err != nil {
		return err
	}
	return os.Setenv("GOCOVERDIR", string(dir))
}

func main() {
	if coverageDirectoryError != nil {
		fmt.Fprintln(os.Stderr, "configure confinement helper coverage:", coverageDirectoryError)
		os.Exit(1)
	}
	confine.RunHelperIfInvoked()
}
