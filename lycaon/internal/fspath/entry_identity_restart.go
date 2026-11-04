package fspath

// SameEntryAcrossRestart compares a durably recorded identity with a live one
// ignoring the device number, which a reboot or remount may change. Compare two
// live entries with os.SameFile instead; this exists for what cannot be compared
// live, such as whether a retained file is the one a journal recorded.
func SameEntryAcrossRestart(recorded, current string) bool {
	return recorded != "" && current != "" && identityWithoutDevice(recorded) == identityWithoutDevice(current)
}
