package httpio

import (
	"fmt"
	"strings"
)

// Required names one dependency a route family always serves with.
type Required struct {
	Name    string
	Present bool
}

// RequireDependencies panics when the composition root omits a dependency a
// route family needs. Handlers never answer for missing wiring.
func RequireDependencies(family string, required ...Required) {
	var missing []string
	for _, dep := range required {
		if !dep.Present {
			missing = append(missing, dep.Name)
		}
	}
	if len(missing) > 0 {
		panic(fmt.Sprintf("%s: missing required dependencies: %s", family, strings.Join(missing, ", ")))
	}
}
