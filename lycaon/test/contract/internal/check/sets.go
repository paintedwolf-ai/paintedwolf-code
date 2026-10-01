package check

func SortedSetEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ma := make(map[string]struct{}, len(a))
	for _, v := range a {
		ma[v] = struct{}{}
	}
	for _, v := range b {
		if _, ok := ma[v]; !ok {
			return false
		}
	}
	return true
}
