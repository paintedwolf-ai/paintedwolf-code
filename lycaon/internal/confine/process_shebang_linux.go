//go:build linux

package confine

import (
	"bytes"
	"strings"
	"syscall"
)

const platformShebangBytes = 256

// This defensive capture bound exceeds the kernel's supported interpreter
// recursion. The kernel still produces its own ordinary ELOOP outcome.
const maximumInterpreterDepth = 64

// Linux binfmt_script preserves the optional remainder as one argument, and
// permits an EOF-terminated header when the interpreter path is complete.
func platformShebang(header []byte) ([]string, error) {
	if !bytes.HasPrefix(header, []byte("#!")) {
		return nil, nil
	}
	var buffer [platformShebangBytes]byte
	copy(buffer[:], header)
	end := bytes.IndexByte(buffer[:], '\n')
	if end < 0 {
		path := bytes.TrimLeft(buffer[2:], " \t")
		if bytes.IndexAny(path, " \t\x00") < 0 {
			return nil, syscall.ENOEXEC
		}
		end = len(buffer) - 1
	}
	line := strings.Trim(string(buffer[2:end]), " \t")
	if nul := strings.IndexByte(line, 0); nul >= 0 {
		line = line[:nul]
	}
	if line == "" {
		return nil, syscall.ENOEXEC
	}
	separator := strings.IndexAny(line, " \t")
	if separator < 0 {
		return []string{line}, nil
	}
	words := []string{line[:separator]}
	if argument := strings.TrimLeft(line[separator:], " \t"); argument != "" {
		words = append(words, argument)
	}
	return words, nil
}
