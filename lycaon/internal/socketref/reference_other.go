//go:build !linux && !darwin

package socketref

import "os"

const Required = false

func Open(string) (*Reference, error) { return nil, ErrUnbound }

func descriptorPath(*os.File) string { return "" }

func duplicate(*os.File) (*os.File, error) { return nil, ErrUnbound }

func sameObject(*Reference, *Reference) bool { return false }

func matchesPath(*Reference, string) bool { return false }
