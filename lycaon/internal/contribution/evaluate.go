package contribution

// FactLookup answers one typed fact query.
type FactLookup func(fact, operand string) bool

// HostVerdict is the three-valued host evaluation result.
type HostVerdict int

const (
	HostFalse HostVerdict = iota
	HostUnknown
	HostTrue
)

// EvaluateHostCondition propagates shell facts as unknown.
func EvaluateHostCondition(c *Condition, lookup FactLookup) HostVerdict {
	if c == nil {
		return HostTrue
	}
	switch {
	case len(c.All) > 0:
		verdict := HostTrue
		for _, child := range c.All {
			switch EvaluateHostCondition(&child, lookup) {
			case HostFalse:
				return HostFalse
			case HostUnknown:
				verdict = HostUnknown
			case HostTrue:
			}
		}
		return verdict
	case len(c.Any) > 0:
		verdict := HostFalse
		for _, child := range c.Any {
			switch EvaluateHostCondition(&child, lookup) {
			case HostTrue:
				return HostTrue
			case HostUnknown:
				verdict = HostUnknown
			case HostFalse:
			}
		}
		return verdict
	case c.Not != nil:
		switch EvaluateHostCondition(c.Not, lookup) {
		case HostTrue:
			return HostFalse
		case HostFalse:
			return HostTrue
		default:
			return HostUnknown
		}
	default:
		if facts[c.Fact].Plane != PlaneHost {
			return HostUnknown
		}
		if lookup(c.Fact, c.Is) {
			return HostTrue
		}
		return HostFalse
	}
}
