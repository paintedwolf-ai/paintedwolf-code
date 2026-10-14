// Command helpermain is the confinement helper alone. A coverage build of it
// records the helper's statements when it exits, which a test binary run as
// the helper never does.
package main

import _ "github.com/lycaon/lycaon/internal/confine"

func main() {}
