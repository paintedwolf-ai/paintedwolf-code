//go:build confinehelpermain

// Command helpermain is the confinement helper alone, dispatched from main. A
// coverage build of it records the helper's statements, which a test binary
// run as the helper never does.
package main

import "github.com/lycaon/lycaon/internal/confine"

func main() { confine.RunHelperIfInvoked() }
