package interp

type replacementAbort struct{ cause error }

// Guts of the sub() and gsub() functions
func (p *interp) sub(regex, repl, in string, global bool) (out string, num int, err error) {
	// Only this callback's admission abort is recovered. No result is assigned
	// by the VM until this complete replacement returns successfully.
	defer func() {
		if stopped := recover(); stopped != nil {
			if refused, ok := stopped.(replacementAbort); ok {
				out, num, err = "", 0, refused.cause
				return
			}
			panic(stopped)
		}
	}()
	admit := func(n int) {
		if err := p.allocate(n); err != nil {
			panic(replacementAbort{err})
		}
	}
	// Unmatched bytes and old/new result growth overlap; callback-local small
	// replacement scratch is reused. Expansion is admitted at each match.
	admit(4*len(in) + 64)
	re, err := p.compileRegex(regex)
	if err != nil {
		return "", 0, err
	}
	count := 0
	out = re.ReplaceAllStringFunc(in, func(s string) string {
		// Only do the first replacement for sub(), or all for gsub()
		if !global && count > 0 {
			return s
		}
		if err := p.step(); err != nil {
			panic(replacementAbort{err})
		}
		count++
		size := 0
		for i := 0; i < len(repl); i++ {
			switch repl[i] {
			case '&':
				size += len(s)
			case '\\':
				i++
				if i < len(repl) && repl[i] != '&' && repl[i] != '\\' {
					size += 2
				} else {
					size++
				}
			default:
				size++
			}
		}
		// Replacement builder capacity, conversion and aggregate result growth
		// may overlap. This conservative charge precedes every such allocation.
		admit(8 * size)
		// Handle & (ampersand) properly in replacement string
		r := make([]byte, 0, 64) // Up to 64 byte replacement won't require heap allocation
		for i := 0; i < len(repl); i++ {
			switch repl[i] {
			case '&':
				r = append(r, s...)
			case '\\':
				i++
				if i < len(repl) {
					switch repl[i] {
					case '&':
						r = append(r, '&')
					case '\\':
						r = append(r, '\\')
					default:
						r = append(r, '\\', repl[i])
					}
				} else {
					r = append(r, '\\')
				}
			default:
				r = append(r, repl[i])
			}
		}
		return string(r)
	})
	return out, count, nil
}
