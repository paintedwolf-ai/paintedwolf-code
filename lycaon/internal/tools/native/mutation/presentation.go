package mutation

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
)

// quote renders a name the way GNU coreutils quotes it in -v messages:
// single quotes, double quotes around a name holding a single quote, and
// shell escaping when both would be ambiguous.
func quote(name string) string {
	if !strings.ContainsRune(name, '\'') {
		return "'" + name + "'"
	}
	if !strings.ContainsAny(name, "\"$`\\") {
		return `"` + name + `"`
	}
	return "'" + strings.ReplaceAll(name, "'", `'\''`) + "'"
}

// displayed maps an owner's absolute result path back onto the operand the
// program wrote, so -v names what the user named.
func displayed(abs, boundAbs, operand string) string {
	if abs == boundAbs {
		return operand
	}
	if rel, ok := strings.CutPrefix(abs, boundAbs+string(filepath.Separator)); ok {
		return strings.TrimSuffix(operand, "/") + "/" + filepath.ToSlash(rel)
	}
	return abs
}

// destinationDisplay is DEST, or DEST/NAME when the source moves into it.
func destinationDisplay(dest, destAbs, toAbs string) string {
	if toAbs == destAbs {
		return dest
	}
	return strings.TrimSuffix(dest, "/") + "/" + filepath.Base(toAbs)
}

func (inv *invocation) verbose(format string, args ...any) {
	if inv.flag("-v") {
		_, _ = fmt.Fprintf(inv.stdout, format+"\n", args...)
	}
}

// modeString is ls/tar's ten-character mode with setuid, setgid and sticky.
func modeString(kind byte, perm fs.FileMode, setuid, setgid, sticky bool) string {
	b := []byte{kind, '-', '-', '-', '-', '-', '-', '-', '-', '-'}
	for i, c := range "rwxrwxrwx" {
		if perm&(1<<uint(8-i)) != 0 {
			b[i+1] = byte(c)
		}
	}
	special := func(index int, on bool, set, unset byte) {
		if !on {
			return
		}
		if b[index] == 'x' {
			b[index] = set
		} else {
			b[index] = unset
		}
	}
	special(3, setuid, 's', 'S')
	special(6, setgid, 's', 'S')
	special(9, sticky, 't', 'T')
	return string(b)
}
