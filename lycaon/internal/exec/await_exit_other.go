//go:build unix && !darwin && !linux

package exec

func awaitLeaderExit(int) {}
