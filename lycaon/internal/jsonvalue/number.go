package jsonvalue

// Int coerces a decoded JSON number to int, returning 0 for anything else.
//
// encoding/json produces float64 while sqlc rows and hand-built maps produce int
// or int64, so a caller reading either has to accept all three.
func Int(value any) int {
	switch n := value.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		return 0
	}
}
