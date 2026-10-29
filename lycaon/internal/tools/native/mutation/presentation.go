package mutation

import (
	"archive/tar"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
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

// tarLongListing is GNU tar's tv line. The owner/size column widens to the
// longest seen so far, starting at 19, and the time is local.
func tarLongListing(hdr *tar.Header, name string, width int) (string, int) {
	kind := byte('-')
	switch hdr.Typeflag {
	case tar.TypeDir:
		kind = 'd'
	case tar.TypeSymlink:
		kind = 'l'
	case tar.TypeLink:
		kind = 'h'
	case tar.TypeChar:
		kind = 'c'
	case tar.TypeBlock:
		kind = 'b'
	case tar.TypeFifo:
		kind = 'p'
	}
	mode := modeString(kind, os.FileMode(hdr.Mode)&os.ModePerm, hdr.Mode&0o4000 != 0, hdr.Mode&0o2000 != 0, hdr.Mode&0o1000 != 0)
	user, group := hdr.Uname, hdr.Gname
	if user == "" {
		user = strconv.Itoa(hdr.Uid)
	}
	if group == "" {
		group = strconv.Itoa(hdr.Gid)
	}
	size := strconv.FormatInt(hdr.Size, 10)
	if kind == 'c' || kind == 'b' {
		size = fmt.Sprintf("%d,%d", hdr.Devmajor, hdr.Devminor)
	}
	pad := len(user) + 1 + len(group) + 1 + len(size)
	width = max(width, pad)
	line := fmt.Sprintf("%s %s/%s %*s %s %s", mode, user, group, width-pad+len(size), size, hdr.ModTime.Local().Format("2006-01-02 15:04"), name)
	switch kind {
	case 'l':
		line += " -> " + hdr.Linkname
	case 'h':
		line += " link to " + hdr.Linkname
	}
	return line, width
}
