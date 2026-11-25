package contract

import (
	"fmt"
	"testing"
)

// Every structured authority refusal (an isolation outcome or a policy or
// isolation failure class) names, on each route to it, the conditions that
// control it and a typed producer for each of their inputs. A route the
// inventory cannot attribute is unresolved capability scope.
func TestAuthorityRefusalDecisionsResolve(t *testing.T) {
	t.Parallel()
	inv := scanToolRejectEmission(t)
	authority, routes := 0, 0
	var unresolved []string
	for _, site := range inv.Sites {
		if !site.Authority {
			continue
		}
		authority++
		if len(site.Decisions) == 0 {
			unresolved = append(unresolved, fmt.Sprintf("%s:%d in %s %v: no decision route", site.Rel, site.Line, site.Func, site.Codes))
		}
		for _, route := range site.Decisions {
			routes++
			for _, in := range route.Inputs {
				if !in.Resolved {
					unresolved = append(unresolved, fmt.Sprintf("%s:%d in %s %v: route %s:%d input %s (%s)",
						site.Rel, site.Line, site.Func, site.Codes, route.Rel, route.Line, in.Expr, in.Producer))
				}
			}
		}
	}
	if authority == 0 {
		t.Fatal("inventory found no structured authority refusals")
	}
	t.Logf("structured authority refusals: %d sites, %d decision routes, %d unresolved inputs", authority, routes, len(unresolved))
	for _, u := range unresolved {
		t.Errorf("unresolved authority decision: %s", u)
	}
}
