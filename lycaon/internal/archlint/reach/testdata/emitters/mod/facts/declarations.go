package facts

// Declared lists every name; a declaration table is not production.
var Declared = map[string]string{"declared_only": "bool", "live_field": "bool"}

// Providers declares facts together with the function that produces them.
var Providers = []struct {
	Name string
	Get  func() any
}{{"provider_fact", func() any { return 1 }}}
