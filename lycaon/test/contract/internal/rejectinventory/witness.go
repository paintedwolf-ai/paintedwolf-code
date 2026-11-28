package rejectinventory

import (
	"fmt"
	"reflect"
	"runtime"
	"slices"
)

// Witness records one observed refusal: the typed decision that denied the
// call, the inputs it was decided on, and the receipt the call settled with.
type Witness struct {
	Tool      string
	Recipient string
	State     string
	// Decision is the producer identity of the typed admission decision.
	Decision     string
	Inputs       map[string]any
	Code         string
	FailureClass string
	Disposition  string
	Status       string
	Invoked      bool
}

// DecisionIdentity returns the producer identity the inventory gives a
// package-level decision function.
func DecisionIdentity(fn any) string {
	return runtime.FuncForPC(reflect.ValueOf(fn).Pointer()).Name()
}

// Finding is one authority site no witness covers.
type Finding struct {
	Site   Site
	Detail string
}

func (f Finding) String() string {
	return fmt.Sprintf("%s:%d in %s %v: %s", f.Site.Rel, f.Site.Line, f.Site.Func, f.Site.Codes, f.Detail)
}

// UnwitnessedDecisions returns the authority sites selected by inScope that no
// witness reaches. A witness covers a site only when its code is one the site
// emits and its decision is a producer on one of the site's decision routes,
// so evidence for another emitter of the same code does not count.
func UnwitnessedDecisions(inv Inventory, inScope func(Site) bool, witnesses []Witness) []Finding {
	var out []Finding
	for _, site := range inv.Sites {
		if !site.Authority || !inScope(site) {
			continue
		}
		if !witnessed(site, witnesses) {
			out = append(out, Finding{Site: site, Detail: "no witness records this decision: " + describeRoutes(site)})
		}
	}
	return out
}

func witnessed(site Site, witnesses []Witness) bool {
	for _, w := range witnesses {
		if !slices.Contains(site.Codes, w.Code) {
			continue
		}
		for _, route := range site.Decisions {
			if slices.Contains(route.Producers(), w.Decision) {
				return true
			}
		}
	}
	return false
}

func describeRoutes(site Site) string {
	var out string
	for i, route := range site.Decisions {
		if i > 0 {
			out += "; "
		}
		out += fmt.Sprintf("route %s:%d inputs %v", route.Rel, route.Line, route.Producers())
	}
	return out
}
