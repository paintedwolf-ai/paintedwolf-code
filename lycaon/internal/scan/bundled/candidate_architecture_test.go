package bundled

import "testing"

func TestCandidateArchitectureMatchesBuildProducer(t *testing.T) {
	for _, tc := range []struct{ goos, goarch, machine string }{
		{"darwin", "arm64", "arm64"},
		{"darwin", "amd64", "x86_64"},
		{"linux", "arm64", "aarch64"},
		{"linux", "amd64", "x86_64"},
	} {
		t.Run(tc.goos+"/"+tc.goarch, func(t *testing.T) {
			if got := candidateArchitecture(tc.goos, tc.goarch); got != tc.machine {
				t.Fatalf("candidate architecture = %q, want build producer %q", got, tc.machine)
			}
		})
	}
}
