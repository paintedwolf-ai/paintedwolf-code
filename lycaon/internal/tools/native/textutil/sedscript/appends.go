package sedscript

// queueAppend borrows immutable script/file text and admits the queue's
// backing before growth. The queue is reused after each cycle.
func (m *Machine) queueAppend(item appendItem) error {
	if len(m.appends) == cap(m.appends) {
		capacity := max(1, 2*cap(m.appends))
		if m.config.Reserve != nil {
			if err := m.config.Reserve(int64(capacity) * 64); err != nil {
				return err
			}
		}
		grown := make([]appendItem, len(m.appends), capacity)
		copy(grown, m.appends)
		m.appends = grown
	}
	m.appends = append(m.appends, item)
	return nil
}
