package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func processGroupMemory(group int) (processMemorySample, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return processMemorySample{}, err
	}
	var memory processMemorySample
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		path := filepath.Join("/proc", entry.Name(), "stat")
		raw, err := os.ReadFile(path)
		if vanishedProcess(err) {
			continue
		}
		if err != nil {
			return processMemorySample{}, err
		}
		pgid, pages, err := linuxProcessMemory(string(raw))
		if err != nil {
			return processMemorySample{}, fmt.Errorf("%s: %w", path, err)
		}
		if pgid == group {
			memory.RSSBytes += pages * int64(os.Getpagesize())
			memory.Processes++
		}
	}
	return memory, nil
}

func linuxProcessMemory(stat string) (int, int64, error) {
	closing := strings.LastIndexByte(stat, ')')
	if closing < 0 {
		return 0, 0, fmt.Errorf("process stat has no command boundary")
	}
	fields := strings.Fields(stat[closing+1:])
	if len(fields) < 22 {
		return 0, 0, fmt.Errorf("incomplete process stat")
	}
	group, err := strconv.Atoi(fields[2])
	if err != nil {
		return 0, 0, err
	}
	rss, err := strconv.ParseInt(fields[21], 10, 64)
	if err != nil || rss < 0 {
		return 0, 0, fmt.Errorf("invalid resident page count %q", fields[21])
	}
	return group, rss, nil
}
