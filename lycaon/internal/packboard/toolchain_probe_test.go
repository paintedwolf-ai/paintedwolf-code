package packboard

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestToolchainParsing(t *testing.T) {
	byBinary := func(binary string) toolchainDef {
		for _, tc := range knownToolchains {
			if tc.binary == binary {
				return tc
			}
		}
		t.Fatalf("no toolchain %s", binary)
		return toolchainDef{}
	}
	for _, tc := range []struct{ binary, input, want string }{
		{"bun", "1.3.11\n", "bun 1.3.11"},
		{"node", "v22.1.0\n", "node 22.1.0"},
		{"go", "go version go1.26.6 darwin/arm64\n", "go 1.26.6"},
		{"rustc", "rustc 1.84.0 (9fc6b4312 2025-01-07)\n", "rustc 1.84.0"},
		{"python3", "Python 3.14.0\n", "python 3.14.0"},
		{"java", "openjdk version \"21.0.2\" 2024-01-16\n", "java 21.0.2"},
	} {
		if got := byBinary(tc.binary).parse(tc.input); got != tc.want {
			t.Errorf("%s parse(%q) = %q, want %q", tc.binary, tc.input, got, tc.want)
		}
	}
}

// fakeToolchainHost resolves only binaries listed in dirs of the given PATH value.
type fakeToolchainHost struct {
	path     atomic.Value
	delay    time.Duration
	mu       sync.Mutex
	lookups  []string
	versions map[string]string
	running  atomic.Int32
	peak     atomic.Int32
}

func (f *fakeToolchainHost) host() toolchainHost {
	return toolchainHost{
		pathValue: func() string { return f.path.Load().(string) },
		lookPath: func(name, pathValue string) (string, error) {
			f.mu.Lock()
			f.lookups = append(f.lookups, pathValue)
			f.mu.Unlock()
			if _, ok := f.versions[name]; ok && pathValue == "/resolved/bin" {
				return "/resolved/bin/" + name, nil
			}
			return "", exec.ErrNotInPath
		},
		version: func(ctx context.Context, binary string, _ []string) string {
			n := f.running.Add(1)
			defer f.running.Add(-1)
			for {
				peak := f.peak.Load()
				if n <= peak || f.peak.CompareAndSwap(peak, n) {
					break
				}
			}
			select {
			case <-time.After(f.delay):
			case <-ctx.Done():
				return ""
			}
			return f.versions[filepath.Base(binary)]
		},
	}
}

func newFakeToolchainHost(delay time.Duration) *fakeToolchainHost {
	f := &fakeToolchainHost{delay: delay, versions: map[string]string{
		"node": "v22.1.0", "pnpm": "9.1.0", "go": "go version go1.26.6 darwin/arm64",
	}}
	f.path.Store("/resolved/bin")
	return f
}

func TestToolchainsNeverBlockAssemblyAndUseResolvedPath(t *testing.T) {
	fake := newFakeToolchainHost(200 * time.Millisecond)
	probes := newToolchainProbes(fake.host())
	start := time.Now()
	if got := probes.toolchains(t.Context(), []string{"TypeScript", "Go"}); len(got) != 0 {
		t.Fatalf("first read returned %v before any probe completed", got)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("assembly read blocked for %v", elapsed)
	}
	probes.wg.Wait()
	got := probes.toolchains(t.Context(), []string{"Go", "TypeScript"})
	if want := []string{"node 22.1.0", "pnpm 9.1.0", "go 1.26.6"}; !slices.Equal(got, want) {
		t.Fatalf("toolchains = %v, want %v", got, want)
	}
	for _, path := range fake.lookups {
		if path != "/resolved/bin" {
			t.Fatalf("lookup used PATH %q instead of the resolved PATH", path)
		}
	}
	if fake.peak.Load() < 2 {
		t.Fatalf("probes ran sequentially (peak concurrency %d)", fake.peak.Load())
	}
}

func TestInterruptedToolchainProbeIsNotCached(t *testing.T) {
	// Each version query outlives its timeout, as a hung binary would.
	fake := newFakeToolchainHost(time.Hour)
	probes := newToolchainProbes(fake.host())
	probes.toolchains(t.Context(), []string{"Go"})
	probes.wg.Wait()
	probes.mu.Lock()
	_, cached := probes.results[pathHash("/resolved/bin")+":go"]
	_, retry := probes.failed[pathHash("/resolved/bin")+":go"]
	probes.mu.Unlock()
	if cached || !retry {
		t.Fatalf("interrupted probe cached=%v retry scheduled=%v", cached, retry)
	}
	// The retry waits out its interval instead of re-probing on every assembly.
	probes.toolchains(t.Context(), []string{"Go"})
	probes.mu.Lock()
	inflight := probes.inflight[pathHash("/resolved/bin")+":go"]
	probes.mu.Unlock()
	if inflight {
		t.Fatal("interrupted probe was retried before its interval")
	}
}

func TestCanceledCallerDoesNotCancelTheRefresh(t *testing.T) {
	fake := newFakeToolchainHost(10 * time.Millisecond)
	probes := newToolchainProbes(fake.host())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	probes.toolchains(ctx, []string{"Go"})
	probes.wg.Wait()
	if got := probes.toolchains(t.Context(), []string{"Go"}); !slices.Equal(got, []string{"go 1.26.6"}) {
		t.Fatalf("a finished prompt's context cut the refresh short: %v", got)
	}
}

func TestToolchainResultsAreKeyedByResolvedPath(t *testing.T) {
	fake := newFakeToolchainHost(0)
	probes := newToolchainProbes(fake.host())
	probes.toolchains(t.Context(), []string{"Go"})
	probes.wg.Wait()
	if got := probes.toolchains(t.Context(), []string{"Go"}); !slices.Equal(got, []string{"go 1.26.6"}) {
		t.Fatalf("toolchains = %v", got)
	}
	fake.path.Store("/other/bin")
	if got := probes.toolchains(t.Context(), []string{"Go"}); len(got) != 0 {
		t.Fatalf("a different PATH reused another PATH's toolchains: %v", got)
	}
	probes.wg.Wait()
	if got := probes.toolchains(t.Context(), []string{"Go"}); len(got) != 0 {
		t.Fatalf("toolchains outside the resolved PATH were reported: %v", got)
	}
}

func TestLookPathInSearchesOnlyTheGivenPath(t *testing.T) {
	dir := t.TempDir()
	tool := filepath.Join(dir, "painted-wolf-probe-tool")
	testutil.FailErr(t, "write tool", os.WriteFile(tool, []byte("#!/bin/sh\n"), 0o755))
	if found, err := exec.LookPathIn("painted-wolf-probe-tool", "relative:"+dir); err != nil || found != tool {
		t.Fatalf("LookPathIn = %q, %v", found, err)
	}
	if _, err := exec.LookPathIn("painted-wolf-probe-tool", t.TempDir()); err == nil {
		t.Fatal("found a tool outside the given PATH")
	}
}
