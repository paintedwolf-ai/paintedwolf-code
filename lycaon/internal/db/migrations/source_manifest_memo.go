package migrations

import "container/list"

// migrationMemo bounds re-derivable work retained between converted roots.
// Each caller charges its strings, entry records, and fixed bookkeeping cost.
type migrationMemo[T any] struct {
	limit, used int64
	entries     map[string]*list.Element
	order       list.List
}

type migrationMemoEntry[T any] struct {
	key    string
	value  T
	weight int64
}

func newMigrationMemo[T any](limit int64) *migrationMemo[T] {
	return &migrationMemo[T]{limit: limit, entries: make(map[string]*list.Element)}
}

func (m *migrationMemo[T]) load(key string) (T, bool) {
	if element := m.entries[key]; element != nil {
		m.order.MoveToFront(element)
		return element.Value.(migrationMemoEntry[T]).value, true
	}
	var zero T
	return zero, false
}

func (m *migrationMemo[T]) store(key string, value T, weight int64) {
	if old := m.entries[key]; old != nil {
		m.remove(old)
	}
	if weight <= 0 || weight > m.limit {
		return
	}
	for m.used > m.limit-weight {
		m.remove(m.order.Back())
	}
	m.entries[key] = m.order.PushFront(migrationMemoEntry[T]{key: key, value: value, weight: weight})
	m.used += weight
}

func (m *migrationMemo[T]) remove(element *list.Element) {
	entry := element.Value.(migrationMemoEntry[T])
	delete(m.entries, entry.key)
	m.used -= entry.weight
	m.order.Remove(element)
}
