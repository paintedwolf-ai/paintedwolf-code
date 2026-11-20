//go:build !linux

package main

import "errors"

type openWatch struct{}

// watchOpens needs Linux inotify; elsewhere the scale assertions do not run.
func watchOpens(string) (*openWatch, error) {
	return nil, errors.New("counting project file opens needs Linux")
}

func (*openWatch) Count() int64 { return -1 }
func (*openWatch) Close() error { return nil }
