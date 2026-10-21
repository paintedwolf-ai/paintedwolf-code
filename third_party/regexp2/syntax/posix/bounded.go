package syntax

import (
	"unsafe"

	"github.com/dlclark/regexp2/v2/internal/budget"
)

func ParseMetered(source string, flags Flags, m *budget.Meter) (*Regexp, error) {
	return parse(source, flags, m)
}
func appendMetered[T any](m *budget.Meter, dst []T, values ...T) []T {
	if m == nil {
		return append(dst, values...)
	}
	m.Step(int64(len(values)))
	if len(dst)+len(values) > cap(dst) {
		capacity := max(2*cap(dst), len(dst)+len(values))
		var item T
		m.Alloc(int64(capacity) * int64(unsafe.Sizeof(item)))
		m.Step(int64(len(dst)))
		next := make([]T, len(dst), capacity)
		copy(next, dst)
		dst = next
	}
	return append(dst, values...)
}

// Unicode category aliases are immutable package data. Initialize them before
// any operation can cancel halfway through sync.Once and retain a partial map.
// Their storage remains module-static, outside per-operation scratch admission.
func init() { categoryAliases.once.Do(func() { initCategoryAliases(nil) }) }
