package confine

import (
	"os"
	"sync/atomic"
)

// Pack-source floor resolution canonicalizes every catalogue path through the
// kernel. The catalogues are pack data resolved against a stable host, so the
// resolved lists are memoized on the inputs that can change at runtime: the
// installed source and the home directory. A memo hit returns exactly what a
// fresh walk would resolve. Config-dir-derived floors never memoize: the
// config dir may be a symlink retargeted at runtime, and classification must
// recheck it on every call.

// sourceFloorMemo memoizes a source-installed floor on the source pointer and
// home directory. Set*Source swaps install a new pointer, which re-resolves.
type sourceFloorMemo struct {
	current atomic.Pointer[sourceResolvedFloor]
}

type sourceResolvedFloor struct {
	src   *func() []string
	home  string
	paths []string
}

func (m *sourceFloorMemo) get(src *func() []string, resolve func() []string) []string {
	home := homeDir()
	if c := m.current.Load(); c != nil && c.src == src && c.home == home {
		return c.paths
	}
	paths := resolve()
	m.current.Store(&sourceResolvedFloor{src: src, home: home, paths: paths})
	return paths
}

// homeDir is the home directory ~/ floor entries join onto.
func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}
