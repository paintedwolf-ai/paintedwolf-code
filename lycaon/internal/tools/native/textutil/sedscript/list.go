package sedscript

// list streams l output with C and octal escapes. Its reusable buffer does
// not grow with the record or with the number of records.
func (m *Machine) list(pattern []byte, width int) error {
	if m.listing == nil {
		if m.config.Reserve != nil {
			if err := m.config.Reserve(4096); err != nil {
				return err
			}
		}
		m.listing = make([]byte, 0, 4096)
	}
	if width < 0 {
		width = 70
	}
	m.listing = m.listing[:0]
	column := 0
	for _, c := range pattern {
		var encoded [4]byte
		item := encoded[:1]
		switch {
		case c == '\\':
			encoded[0], encoded[1] = '\\', '\\'
			item = encoded[:2]
		case listEscapes[c] != 0:
			encoded[0], encoded[1] = '\\', listEscapes[c]
			item = encoded[:2]
		case c < 0x20 || c >= 0x7f:
			encoded = [4]byte{'\\', '0' + c>>6, '0' + c>>3&7, '0' + c&7}
			item = encoded[:]
		default:
			encoded[0] = c
		}
		if len(m.listing)+6 > cap(m.listing) {
			if err := m.stream.out.raw(m.listing); err != nil {
				return err
			}
			m.listing = m.listing[:0]
		}
		if width > 1 && column+len(item) > width-1 {
			m.listing = append(m.listing, '\\', '\n')
			column = 0
		}
		m.listing = append(m.listing, item...)
		column += len(item)
	}
	if err := m.stream.out.raw(m.listing); err != nil {
		return err
	}
	return m.stream.out.raw([]byte{'$', '\n'})
}

var listEscapes = [256]byte{'\a': 'a', '\b': 'b', '\f': 'f', '\n': 'n', '\r': 'r', '\t': 't', '\v': 'v'}
