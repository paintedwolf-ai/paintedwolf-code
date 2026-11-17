package observability

import "sort"

// SecretFieldNames returns the sorted field-name needles.
func SecretFieldNames() []string {
	out := append([]string(nil), secretFieldNames...)
	sort.Strings(out)
	return out
}
