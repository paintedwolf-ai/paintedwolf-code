package facts

// Context holds what facts are projected from.
type Context struct {
	Live  bool
	Never bool
}

// Project publishes facts under their names.
func Project(c *Context) map[string]any {
	return map[string]any{
		"live_field":  c.Live,
		"never_field": c.Never,
	}
}

// Family builds a fact name from a field of a family.
func Family(name string) string { return "paintedwolf.family_" + name }
