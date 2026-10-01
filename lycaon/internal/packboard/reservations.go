package packboard

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// ReservationsFromSnapshot returns active handoff reservations embedded in a board snapshot.
func ReservationsFromSnapshot(snap api.BoardSnapshot) []api.BoardReservationEntry {
	if snap.Workers == nil {
		return nil
	}
	raw, _ := (*snap.Workers)["active_reservations"].([]api.BoardReservationEntry)
	return raw
}

// EnrichSnapshotReservations attaches active path reservations for pack_board orientation.
func EnrichSnapshotReservations(snap *api.BoardSnapshot, rows []api.BoardReservationEntry) {
	if snap == nil || len(rows) == 0 {
		return
	}
	if snap.Workers == nil {
		snap.Workers = &api.BoardWorkersSlice{}
	}
	(*snap.Workers)["active_reservations"] = append([]api.BoardReservationEntry(nil), rows...)
}

// FormatReservedPathsLines renders active handoff reservations for coordinator peer digest.
func FormatReservedPathsLines(reservations []api.BoardReservationEntry) []string {
	if len(reservations) == 0 {
		return nil
	}
	lines := make([]string, 0, len(reservations)+1)
	lines = append(lines, "Reserved paths (active):")
	for _, r := range reservations {
		path := strings.TrimSpace(r.Path)
		job := strings.TrimSpace(r.JobID)
		if path == "" || job == "" {
			continue
		}
		label := strings.TrimSpace(r.LegLabel)
		if label != "" {
			lines = append(lines, fmt.Sprintf("- %s — job %s (%s)", path, WorkerJobIDPrefix(job), label))
		} else {
			lines = append(lines, fmt.Sprintf("- %s — job %s", path, WorkerJobIDPrefix(job)))
		}
	}
	if len(lines) <= 1 {
		return nil
	}
	return lines
}
