package literal

import (
	"io/fs"
	"strconv"

	"github.com/lycaon/lycaon/internal/fseffect"
)

// typeLetter is ls's leading mode character.
func typeLetter(mode fs.FileMode) byte {
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
	return '-'
}

// modeString renders permissions as ls -l and stat %A do.
func modeString(mode fs.FileMode) string {
	out := []byte{typeLetter(mode)}
	const rwx = "rwx"
	for i := 8; i >= 0; i-- {
		if mode&(1<<uint(i)) != 0 {
			out = append(out, rwx[(8-i)%3])
		} else {
			out = append(out, '-')
		}
	}
	special := func(index int, bit fs.FileMode, set, unset byte) {
		if mode&bit == 0 {
			return
		}
		if out[index] == '-' {
			out[index] = unset
		} else {
			out[index] = set
		}
	}
	special(3, fs.ModeSetuid, 's', 'S')
	special(6, fs.ModeSetgid, 's', 'S')
	special(9, fs.ModeSticky, 't', 'T')
	return string(out)
}

// octalMode is stat's %a and BSD %Lp: permission and special bits in octal.
func octalMode(mode fs.FileMode) string {
	bits := uint32(mode.Perm())
	if mode&fs.ModeSetuid != 0 {
		bits |= 0o4000
	}
	if mode&fs.ModeSetgid != 0 {
		bits |= 0o2000
	}
	if mode&fs.ModeSticky != 0 {
		bits |= 0o1000
	}
	return strconv.FormatUint(uint64(bits), 8)
}

// gnuType is GNU stat %F.
func gnuType(st fseffect.EntryStat) string {
	switch typeLetter(st.Mode) {
	case 'd':
		return "directory"
	case 'l':
		return "symbolic link"
	case 'p':
		return "fifo"
	case 's':
		return "socket"
	case 'c':
		return "character special file"
	case 'b':
		return "block special file"
	}
	if st.Size == 0 {
		return "regular empty file"
	}
	return "regular file"
}

// bsdType is BSD stat %HT.
func bsdType(st fseffect.EntryStat) string {
	switch typeLetter(st.Mode) {
	case 'd':
		return "Directory"
	case 'l':
		return "Symbolic Link"
	case 'p':
		return "Fifo File"
	case 's':
		return "Socket"
	case 'c':
		return "Character Device"
	case 'b':
		return "Block Device"
	}
	return "Regular File"
}

// indicator is ls -F's classification suffix.
func indicator(mode fs.FileMode) string {
	switch typeLetter(mode) {
	case 'd':
		return "/"
	case 'l':
		return "@"
	case 'p':
		return "|"
	case 's':
		return "="
	case '-':
		if mode&0o111 != 0 {
			return "*"
		}
	}
	return ""
}
