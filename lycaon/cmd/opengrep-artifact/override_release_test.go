//go:build paintedwolf_release

package main

import "testing"

func TestReleaseArtifactToolRejectsIndependentExecutable(t *testing.T) {
	for _, mode := range []string{"verify", "resolve"} {
		if err := (options{mode: mode, executable: "/other/opengrep", digest: "independent"}).validate(); err == nil {
			t.Fatal("release accepted independent executable authority")
		}
	}
}
