package interp

import "strings"

// Budget meters an embedding host's work and allocation ceilings. Step is
// called before each backward jump, for-in iteration and user function call,
// which bounds every unbounded execution path; Allocate is called before a
// program builds a new string or array element of n bytes. A non-nil error
// stops execution and is returned by Execute.
type Budget interface {
	Step() error
	Allocate(n int) error
}

func (p *interp) step() error {
	if p.budget == nil {
		return nil
	}
	return p.budget.Step()
}

func (p *interp) allocate(n int) error {
	if p.budget == nil {
		return nil
	}
	return p.budget.Allocate(n)
}

// assignElement charges a new element's key and value before storing it.
func (p *interp) assignElement(array map[string]value, key string, v value) error {
	if _, exists := array[key]; !exists {
		if err := p.allocate(len(key) + len(p.toString(v)) + elementOverhead); err != nil {
			return err
		}
	}
	array[key] = v
	return nil
}

// elementOverhead covers one map entry and value header beyond its bytes.
const elementOverhead = 64

// formatBound is an upper bound on the bytes fmt.Sprintf produces for a
// parsed format: its literal bytes, every width and precision (including *
// arguments) and each argument's own size.
func formatBound(format string, args []any) int {
	bound, arg := 0, 0
	argSize := func() int {
		if arg >= len(args) {
			return 0
		}
		switch v := args[arg].(type) {
		case string:
			return len(v)
		case []byte:
			return len(v)
		}
		return 32
	}
	for i := 0; i < len(format); i++ {
		if format[i] != '%' {
			bound++
			continue
		}
		i++
		number := 0
		for i < len(format) && strings.IndexByte(" .-+*#0123456789", format[i]) >= 0 {
			switch c := format[i]; {
			case c >= '0' && c <= '9':
				number = number*10 + int(c-'0')
				if number > 1<<30 {
					number = 1 << 30
				}
			case c == '*':
				if arg < len(args) {
					if width, ok := args[arg].(int64); ok && width > 0 {
						if width > 1<<30 {
							width = 1 << 30
						}
						bound += int(width)
					}
				}
				arg++
			default:
				bound += number
				number = 0
			}
			i++
		}
		bound += number
		if i < len(format) && format[i] == '%' {
			bound++
			continue
		}
		bound += argSize()
		arg++
	}
	return bound
}
