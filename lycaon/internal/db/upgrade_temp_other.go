//go:build !darwin && !linux && !windows

package db

import "fmt"

func upgradeTempDirectories(string) ([]string, error) {
	return nil, fmt.Errorf("SQLite temporary volume inspection is unsupported")
}
