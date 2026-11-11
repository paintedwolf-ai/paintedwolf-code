//go:build unix

package exec

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
	"syscall"

	"github.com/lycaon/lycaon/internal/osprocess"
)

type reaperOps struct {
	sessionMembers   func(int) []int
	incarnationOf    func(int) (osprocess.Incarnation, bool)
	incarnationAlive func(osprocess.Incarnation) bool
	signalProcess    func(osprocess.Incarnation, syscall.Signal) error
	signalGroup      func(osprocess.Incarnation, syscall.Signal) error
}

func defaultReaperOps() reaperOps {
	return reaperOps{
		sessionMembers:   osprocess.SessionMembers,
		incarnationOf:    osprocess.IncarnationOf,
		incarnationAlive: osprocess.Incarnation.Alive,
		signalProcess:    osprocess.Signal,
		signalGroup:      osprocess.SignalGroup,
	}
}

type reaperEntry struct {
	kind    byte
	leader  osprocess.Incarnation
	members []osprocess.Incarnation
	path    string
	count   int
}

// readReaperEntriesWithOps replays the engine's registrations until in closes
// and returns what is still tracked. A group whose leader exited while
// transferred members remain alive stays tracked until those members are gone.
func readReaperEntriesWithOps(in io.Reader, ops reaperOps) map[string]*reaperEntry {
	entries := map[string]*reaperEntry{}
	scanner := bufio.NewScanner(in)
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) < 3 {
			continue
		}
		op, kind, payload := line[0], line[1], line[2:]
		if op == '+' && kind == reaperMember {
			recordReaperMember(entries, payload)
			continue
		}
		switch op {
		case '+':
			key, entry := parseReaperEntry(kind, payload, ops)
			if entry == nil {
				continue
			}
			if prior := entries[key]; prior != nil && prior.leader.Start == entry.leader.Start {
				entry = prior
			}
			// A reused pid replaces the stale incarnation.
			entries[key] = entry
			entry.count++
		case '-':
			key, ok := reaperEntryKey(kind, payload)
			if !ok {
				continue
			}
			if entry := entries[key]; entry != nil {
				entry.count--
				if entry.count <= 0 {
					entry.members = retainAliveIncarnations(entry.members, ops.incarnationAlive)
					if len(entry.members) == 0 {
						delete(entries, key)
					} else {
						entry.count = 0
					}
				}
			}
		}
	}
	return entries
}

func recordReaperMember(entries map[string]*reaperEntry, payload string) {
	parts := strings.Split(payload, ":")
	if len(parts) != 3 {
		return
	}
	leaderID, err1 := strconv.Atoi(parts[0])
	memberID, err2 := strconv.Atoi(parts[1])
	memberStart, err3 := strconv.ParseInt(parts[2], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil || leaderID <= 0 || memberID <= 0 || memberStart <= 0 {
		return
	}
	key := fmt.Sprintf("%c%d", reaperGroup, leaderID)
	entry := entries[key]
	if entry == nil {
		entry = &reaperEntry{
			kind:   reaperGroup,
			leader: osprocess.Incarnation{PID: leaderID},
		}
		entries[key] = entry
	}
	entry.members = appendIncarnation(entry.members, osprocess.Incarnation{PID: memberID, Start: memberStart})
}

func reaperEntryKey(kind byte, payload string) (string, bool) {
	if kind == reaperDir {
		return string(kind) + payload, true
	}
	idText, _, _ := strings.Cut(payload, ":")
	id, err := strconv.Atoi(idText)
	if err != nil || id <= 0 {
		return "", false
	}
	return fmt.Sprintf("%c%d", kind, id), true
}

func parseReaperEntry(kind byte, payload string, ops reaperOps) (string, *reaperEntry) {
	switch kind {
	case reaperDir:
		if !reaperDirPath(payload) {
			return "", nil
		}
		return string(kind) + payload, &reaperEntry{kind: kind, path: payload}
	case reaperGroup, reaperProcess, reaperSupervisor:
		idText, startText, hasStart := strings.Cut(payload, ":")
		id, err := strconv.Atoi(idText)
		if err != nil || id <= 0 {
			return "", nil
		}
		var inc osprocess.Incarnation
		if hasStart {
			start, startErr := strconv.ParseInt(startText, 10, 64)
			if startErr != nil || start <= 0 {
				return "", nil
			}
			inc = osprocess.Incarnation{PID: id, Start: start}
		} else {
			var alive bool
			inc, alive = ops.incarnationOf(id)
			if !alive {
				return "", nil
			}
		}
		return fmt.Sprintf("%c%d", kind, id), &reaperEntry{kind: kind, leader: inc}
	}
	return "", nil
}

func retainAliveIncarnations(members []osprocess.Incarnation, alive func(osprocess.Incarnation) bool) []osprocess.Incarnation {
	var kept []osprocess.Incarnation
	for _, member := range members {
		if alive(member) {
			kept = append(kept, member)
		}
	}
	return kept
}

func aliveGroupWitness(entry *reaperEntry, alive func(osprocess.Incarnation) bool) (osprocess.Incarnation, bool) {
	if alive(entry.leader) {
		return entry.leader, true
	}
	for _, member := range entry.members {
		if alive(member) {
			return member, true
		}
	}
	return osprocess.Incarnation{}, false
}

func scanReaperSession(sid int, witness osprocess.Incarnation, ops reaperOps) []osprocess.Incarnation {
	if sid <= 0 || !ops.incarnationAlive(witness) {
		return nil
	}
	pids := ops.sessionMembers(sid)
	var members []osprocess.Incarnation
	for _, pid := range pids {
		if pid <= 0 || pid == sid {
			continue
		}
		if inc, ok := ops.incarnationOf(pid); ok {
			members = appendIncarnation(members, inc)
		}
	}
	if !ops.incarnationAlive(witness) {
		return nil
	}
	return members
}
