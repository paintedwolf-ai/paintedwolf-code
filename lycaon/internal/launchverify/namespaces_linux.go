//go:build linux

package launchverify

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// ReadNamespace observes the owning user namespace, mappings and sealed
// credentials through procfs. pid0 selects the verifier's own namespace.
func ReadNamespace(procRoot string, pid int) (NamespaceFacts, error) {
	if procRoot == "" {
		procRoot = "/proc"
	}
	name := "self"
	if pid != 0 {
		name = strconv.Itoa(pid)
	}
	base := filepath.Join(procRoot, name)
	file, err := os.Open(filepath.Join(base, "ns/user"))
	if err != nil {
		return NamespaceFacts{}, err
	}
	defer func() { _ = file.Close() }()
	kind, err := unix.IoctlGetInt(int(file.Fd()), unix.NS_GET_NSTYPE)
	if err != nil || kind != unix.CLONE_NEWUSER {
		return NamespaceFacts{}, errors.New("process namespace descriptor is not a user namespace")
	}
	owner, err := unix.IoctlGetInt(int(file.Fd()), unix.NS_GET_OWNER_UID)
	if err != nil {
		return NamespaceFacts{}, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return NamespaceFacts{}, err
	}
	facts := NamespaceFacts{Device: uint64(stat.Dev), Inode: stat.Ino, OwnerUID: uint32(owner)}
	if pid == 0 {
		return facts, nil
	}
	uidMap, err := readNamespaceFile(filepath.Join(base, "uid_map"))
	if err != nil {
		return facts, err
	}
	gidMap, err := readNamespaceFile(filepath.Join(base, "gid_map"))
	if err != nil {
		return facts, err
	}
	facts.FullUIDMap = fullNamespaceMap(uidMap)
	facts.FullGIDMap = fullNamespaceMap(gidMap)
	status, err := readNamespaceFile(filepath.Join(base, "status"))
	if err != nil {
		return facts, err
	}
	return namespaceCredentials(facts, status)
}

func readNamespaceFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(io.LimitReader(file, 65537))
	if err != nil {
		return "", err
	}
	if len(raw) > 65536 {
		return "", errors.New("process namespace metadata exceeds its bound")
	}
	return string(raw), nil
}

func fullNamespaceMap(raw string) bool {
	fields := strings.Fields(raw)
	if len(fields) != 3 {
		return false
	}
	for i, want := range []uint64{0, 0, uint64(^uint32(0))} {
		value, err := strconv.ParseUint(fields[i], 10, 32)
		if err != nil || value != want {
			return false
		}
	}
	return true
}

func namespaceCredentials(facts NamespaceFacts, status string) (NamespaceFacts, error) {
	keys := map[string]int{"CapPrm": 0, "CapEff": 1, "CapInh": 2, "CapAmb": 3, "NoNewPrivs": 4}
	seen := [5]bool{}
	for _, line := range strings.Split(status, "\n") {
		key, value, ok := strings.Cut(line, ":")
		index, needed := keys[key]
		if !ok || !needed {
			continue
		}
		if seen[index] {
			return facts, fmt.Errorf("duplicate process credential %s", key)
		}
		seen[index] = true
		if index == 4 {
			value = strings.TrimSpace(value)
			if value != "0" && value != "1" {
				return facts, errors.New("invalid no_new_privs fact")
			}
			facts.NoNewPrivileges = value == "1"
		} else {
			parsed, err := strconv.ParseUint(strings.TrimSpace(value), 16, 64)
			if err != nil {
				return facts, err
			}
			facts.Capabilities[index] = parsed
		}
	}
	for _, found := range seen {
		if !found {
			return facts, errors.New("missing process credential fact")
		}
	}
	return facts, nil
}
