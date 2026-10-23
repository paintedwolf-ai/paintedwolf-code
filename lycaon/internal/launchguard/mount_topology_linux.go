//go:build linux

package launchguard

import (
	"strconv"
	"strings"
)

const (
	maxMountTopologyBytes   = 16 << 20
	maxMountTopologyEntries = 32768
)

type mountEntry struct {
	id            int
	parent        int
	device        string
	root          string
	mountPoint    string
	options       string
	filesystem    string
	source        string
	superOptions  string
	shared        int
	master        int
	propagateFrom int
	unbindable    bool
}

func parseMountInfoLine(line string) (mountEntry, bool) {
	var fields [6]string
	rest := line
	for index := range fields {
		value, next, found := strings.Cut(rest, " ")
		if !found || value == "" {
			return mountEntry{}, false
		}
		fields[index], rest = value, next
	}
	id, errID := strconv.Atoi(fields[0])
	parent, errParent := strconv.Atoi(fields[1])
	// A kernel root identity need not be a pathname: nsfs emits net:[inode].
	root, rootOK := decodeMountInfoField(fields[3])
	point, pointOK := decodeMountInfoPath(fields[4])
	if errID != nil || errParent != nil || id <= 0 || parent <= 0 || !rootOK || !pointOK || !validMountDevice(fields[2]) || !validMountOptions(fields[5]) {
		return mountEntry{}, false
	}
	entry := mountEntry{id: id, parent: parent, device: fields[2], root: root, mountPoint: point, options: fields[5]}
	for {
		optional, next, found := strings.Cut(rest, " ")
		if !found {
			return mountEntry{}, false
		}
		rest = next
		if optional == "-" {
			break
		}
		tag, number, numbered := strings.Cut(optional, ":")
		if !numbered {
			if optional != "unbindable" || entry.unbindable {
				return mountEntry{}, false
			}
			entry.unbindable = true
			continue
		}
		value, err := strconv.Atoi(number)
		if err != nil || value <= 0 {
			return mountEntry{}, false
		}
		var destination *int
		switch tag {
		case "shared":
			destination = &entry.shared
		case "master":
			destination = &entry.master
		case "propagate_from":
			destination = &entry.propagateFrom
		default:
			return mountEntry{}, false
		}
		if *destination != 0 {
			return mountEntry{}, false
		}
		*destination = value
	}
	tail := strings.Split(rest, " ")
	if len(tail) != 3 || tail[0] == "" || !validMountOptions(tail[2]) {
		return mountEntry{}, false
	}
	source, sourceOK := decodeMountInfoField(tail[1])
	if !sourceOK {
		return mountEntry{}, false
	}
	entry.filesystem, entry.source, entry.superOptions = tail[0], source, tail[2]
	return entry, true
}

func validMountDevice(value string) bool {
	major, minor, found := strings.Cut(value, ":")
	if !found || major == "" || minor == "" {
		return false
	}
	_, a := strconv.ParseUint(major, 10, 32)
	_, b := strconv.ParseUint(minor, 10, 32)
	return a == nil && b == nil
}

func validMountOptions(raw string) bool {
	if raw == "" || strings.ContainsAny(raw, " \t\r\n\x00") {
		return false
	}
	seen := map[string]bool{}
	for option := range strings.SplitSeq(raw, ",") {
		if option == "" || seen[option] {
			return false
		}
		seen[option] = true
	}
	return true
}

func decodeMountInfoPath(raw string) (string, bool) {
	value, ok := decodeMountInfoField(raw)
	return value, ok && strings.HasPrefix(value, "/")
}

func decodeMountInfoField(raw string) (string, bool) {
	if raw == "" || strings.ContainsRune(raw, 0) {
		return "", false
	}
	for index := 0; index < len(raw); index++ {
		if raw[index] != '\\' {
			continue
		}
		if index+3 >= len(raw) || raw[index+1] < '0' || raw[index+1] > '3' || !isOctal(raw[index+2]) || !isOctal(raw[index+3]) {
			return "", false
		}
		index += 3
	}
	value := unescapeMountPath(raw)
	return value, !strings.ContainsRune(value, 0)
}

// Complete-topology admission refuses the entire snapshot on any malformed,
// oversized, cyclic or incomplete record set.
func parseMountTopology(raw []byte) ([]mountEntry, error) {
	if len(raw) == 0 || len(raw) > maxMountTopologyBytes || raw[len(raw)-1] != '\n' {
		return nil, &mountTopologyRefusal{predicate: topologyTruncated, bytes: len(raw)}
	}
	var entries []mountEntry
	byID := map[int]mountEntry{}
	for line := range strings.SplitSeq(string(raw[:len(raw)-1]), "\n") {
		entry, ok := parseMountInfoLine(line)
		if !ok || len(entries) == maxMountTopologyEntries {
			return nil, &mountTopologyRefusal{predicate: topologyInvalidRecord, bytes: len(raw), rows: len(entries)}
		}
		if _, exists := byID[entry.id]; exists {
			return nil, &mountTopologyRefusal{predicate: topologyDuplicateIdentity, bytes: len(raw), rows: len(entries), id: entry.id, parent: entry.parent}
		}
		byID[entry.id] = entry
		entries = append(entries, entry)
	}
	roots := 0
	for _, entry := range entries {
		if _, exists := byID[entry.parent]; !exists {
			if entry.mountPoint != "/" {
				return nil, &mountTopologyRefusal{predicate: topologyMissingParent, bytes: len(raw), rows: len(entries), id: entry.id, parent: entry.parent}
			}
			roots++
		}
	}
	if roots != 1 {
		return nil, &mountTopologyRefusal{predicate: topologyAmbiguousRoot, bytes: len(raw), rows: len(entries), roots: roots}
	}
	// Each identity moves unseen → visiting → complete exactly once. A deep
	// caller-created topology must not repeat an ancestor walk for every row.
	state := make(map[int]byte, len(entries))
	for _, entry := range entries {
		if state[entry.id] == 2 {
			continue
		}
		var chain []int
		for current, exists := entry, true; exists; current, exists = byID[current.parent] {
			if state[current.id] == 2 {
				break
			}
			if state[current.id] == 1 {
				return nil, &mountTopologyRefusal{predicate: topologyCycle, bytes: len(raw), rows: len(entries), id: current.id, parent: current.parent}
			}
			state[current.id] = 1
			chain = append(chain, current.id)
		}
		for _, id := range chain {
			state[id] = 2
		}
	}
	return entries, nil
}

func unescapeMountPath(raw string) string {
	if !strings.ContainsRune(raw, '\\') {
		return raw
	}
	var out strings.Builder
	out.Grow(len(raw))
	for i := 0; i < len(raw); i++ {
		if raw[i] == '\\' && i+3 < len(raw) && raw[i+1] >= '0' && raw[i+1] <= '3' && isOctal(raw[i+2]) && isOctal(raw[i+3]) {
			out.WriteByte(((raw[i+1] - '0') << 6) | ((raw[i+2] - '0') << 3) | (raw[i+3] - '0'))
			i += 3
			continue
		}
		out.WriteByte(raw[i])
	}
	return out.String()
}

func isOctal(b byte) bool {
	return b >= '0' && b <= '7'
}

// Diagnostic predicates retain the parser's existing complete-snapshot refusal.
// Scalar facts contain no mount paths and grant no retry or admission authority.
type topologyPredicate uint8

const (
	topologyTruncated topologyPredicate = iota
	topologyInvalidRecord
	topologyDuplicateIdentity
	topologyMissingParent
	topologyAmbiguousRoot
	topologyCycle
)

type mountTopologyRefusal struct {
	predicate                      topologyPredicate
	bytes, rows, id, parent, roots int
}

func (refusal *mountTopologyRefusal) Error() string {
	switch refusal.predicate {
	case topologyTruncated:
		return "truncated mount topology"
	case topologyInvalidRecord:
		return "invalid mount topology record"
	case topologyDuplicateIdentity:
		return "duplicate mount topology identity"
	case topologyMissingParent:
		return "missing mount topology parent"
	case topologyAmbiguousRoot:
		return "ambiguous mount topology root"
	case topologyCycle:
		return "mount topology cycle"
	default:
		return "mount topology refused"
	}
}
