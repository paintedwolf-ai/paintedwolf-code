//go:build windows

package db

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

func upgradeTempDirectories(configured string) ([]string, error) {
	if configured != "" {
		return []string{configured}, nil
	}
	buffer := make([]uint16, 32768)
	n, err := windows.GetTempPath(uint32(len(buffer)), &buffer[0])
	if err != nil {
		return nil, err
	}
	root, err := filepath.Abs(windows.UTF16ToString(buffer[:n]))
	if err != nil {
		return nil, err
	}
	return []string{root}, nil
}
