//go:build darwin

package confine

import (
	"bytes"
	"strings"
	"syscall"
)

const platformShebangBytes = 512
const maximumInterpreterDepth = 2

// The bounded native launch header follows XNU exec_shell_imgact and
// exec_extract_strings, including comment termination and separate arguments.
func platformShebang(header []byte) ([]string, error) {
	if !bytes.HasPrefix(header, []byte("#!")) {
		return nil, nil
	}
	end := bytes.IndexAny(header[2:], "#\n")
	if end < 0 {
		return nil, syscall.ENOEXEC
	}
	line := string(header[2 : 2+end])
	if nul := strings.IndexByte(line, 0); nul >= 0 {
		line = line[:nul]
	}
	words := strings.FieldsFunc(line, func(r rune) bool { return r == ' ' || r == '\t' })
	if len(words) == 0 {
		return nil, syscall.ENOEXEC
	}
	return words, nil
}
