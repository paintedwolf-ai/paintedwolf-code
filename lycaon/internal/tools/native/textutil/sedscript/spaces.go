package sedscript

// spaces owns reusable pattern, hold and transformation storage. Growth is
// admitted before allocation, including the old/new backing overlap. Earlier
// growth claims live until the invocation ends; geometric growth bounds them
// by less than three times each buffer's largest capacity, including the
// final growth clipped to the record limit.
type spaces struct {
	pattern, hold, scratch []byte
	limit                  int64
	reserve                func(int64) error
}

func (s *spaces) grow(dst *[]byte, size int) error {
	if s.limit > 0 && int64(size) > s.limit {
		return &LimitError{Resource: "record_bytes", Maximum: s.limit, Observed: int64(size)}
	}
	if size <= cap(*dst) {
		return nil
	}
	capacity := max(size, 2*cap(*dst))
	if s.limit > 0 && int64(capacity) > s.limit {
		capacity = int(s.limit)
	}
	if s.reserve != nil {
		if err := s.reserve(int64(capacity)); err != nil {
			return err
		}
	}
	grown := make([]byte, len(*dst), capacity)
	copy(grown, *dst)
	*dst = grown
	return nil
}

func (s *spaces) append(dst *[]byte, text []byte) error {
	if err := s.grow(dst, len(*dst)+len(text)); err != nil {
		return err
	}
	*dst = append(*dst, text...)
	return nil
}

func (s *spaces) assign(dst *[]byte, text []byte) error {
	if err := s.grow(dst, len(text)); err != nil {
		return err
	}
	*dst = append((*dst)[:0], text...)
	return nil
}

func (s *spaces) join(dst *[]byte, delimiter byte, text []byte) error {
	if err := s.grow(dst, len(*dst)+1+len(text)); err != nil {
		return err
	}
	*dst = append(*dst, delimiter)
	*dst = append(*dst, text...)
	return nil
}

func (s *spaces) swapScratch() {
	s.pattern, s.scratch = s.scratch, s.pattern[:0]
}
