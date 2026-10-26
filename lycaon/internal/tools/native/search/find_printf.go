package search

import (
	"fmt"
	"io/fs"
	"strconv"
	"strings"
)

// printfPrimary compiles -printf FORMAT. It supports %p (path), %f (name),
// %h (leading directories), %P (path below the starting point), %s (size),
// %d (depth), %y (type letter), %m (octal permissions), %U and %G (numeric
// owner and group), %T@ (modification time in seconds) and %%, with the
// escapes \n, \t, \0 and \\. Any other directive or escape refuses.
func (r *findRun) printfPrimary(format string) (*findNode, error) {
	var pieces []func(entry) string
	literal := func(text string) { pieces = append(pieces, func(entry) string { return text }) }
	for i := 0; i < len(format); i++ {
		c := format[i]
		switch c {
		case '\\':
			if i+1 >= len(format) {
				return nil, r.inv.usage(format, "a -printf escape after the backslash")
			}
			i++
			escape, ok := map[byte]string{'n': "\n", 't': "\t", '0': "\x00", '\\': "\\"}[format[i]]
			if !ok {
				return nil, r.inv.usage(format, `-printf escapes \n, \t, \0 and \\`)
			}
			literal(escape)
		case '%':
			if i+1 >= len(format) {
				return nil, r.inv.usage(format, "a -printf directive after %")
			}
			i++
			directive := format[i]
			if directive == 'T' {
				if i+1 >= len(format) || format[i+1] != '@' {
					return nil, r.inv.usage(format, "%T@ as the only time directive")
				}
				i++
			}
			piece, ok := printfDirectives[directive]
			if !ok {
				return nil, r.inv.usage(format, "-printf directives %p %f %h %P %s %d %y %m %U %G %T@ %%")
			}
			pieces = append(pieces, piece)
		default:
			end := i
			for end < len(format) && format[end] != '\\' && format[end] != '%' {
				end++
			}
			literal(format[i:end])
			i = end - 1
		}
	}
	return &findNode{op: "primary", test: func(r *findRun, e entry) (bool, error) {
		var out strings.Builder
		for _, piece := range pieces {
			out.WriteString(piece(e))
		}
		return true, r.inv.out.text(out.String())
	}}, nil
}

var printfDirectives = map[byte]func(entry) string{
	'p': func(e entry) string { return e.display },
	'f': func(e entry) string { return e.name },
	'h': func(e entry) string {
		trimmed := strings.TrimRight(e.display, "/")
		if i := strings.LastIndexByte(trimmed, '/'); i >= 0 {
			if i == 0 {
				return "/"
			}
			return trimmed[:i]
		}
		return "."
	},
	'P': func(e entry) string {
		if e.depth == 0 {
			return ""
		}
		return strings.TrimPrefix(strings.TrimPrefix(e.display, e.start), "/")
	},
	's': func(e entry) string { return strconv.FormatInt(e.stat.Size, 10) },
	'd': func(e entry) string { return strconv.Itoa(e.depth) },
	'y': func(e entry) string { return string(typeLetterOf(e.stat.Mode)) },
	'm': func(e entry) string {
		bits := uint32(e.stat.Mode.Perm())
		for flag, bit := range map[fs.FileMode]uint32{fs.ModeSetuid: 0o4000, fs.ModeSetgid: 0o2000, fs.ModeSticky: 0o1000} {
			if e.stat.Mode&flag != 0 {
				bits |= bit
			}
		}
		return strconv.FormatUint(uint64(bits), 8)
	},
	'U': func(e entry) string { return strconv.FormatUint(uint64(e.stat.UID), 10) },
	'G': func(e entry) string { return strconv.FormatUint(uint64(e.stat.GID), 10) },
	'T': func(e entry) string {
		return fmt.Sprintf("%d.%010d", e.stat.ModTime.Unix(), e.stat.ModTime.Nanosecond())
	},
	'%': func(entry) string { return "%" },
}

// typeLetterOf is find's %y letter.
func typeLetterOf(mode fs.FileMode) byte {
	switch {
	case mode&fs.ModeDir != 0:
		return 'd'
	case mode&fs.ModeSymlink != 0:
		return 'l'
	case mode&fs.ModeNamedPipe != 0:
		return 'p'
	case mode&fs.ModeSocket != 0:
		return 's'
	case mode&fs.ModeCharDevice != 0:
		return 'c'
	case mode&fs.ModeDevice != 0:
		return 'b'
	}
	return 'f'
}
