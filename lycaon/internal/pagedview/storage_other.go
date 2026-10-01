//go:build !windows

package pagedview

func platformStorageFull(error) bool { return false }
