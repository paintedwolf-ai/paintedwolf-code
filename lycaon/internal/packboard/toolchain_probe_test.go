package packboard

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
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
		{"python3", "Python 3.14.0\n", "python3 3.14.0"},
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
	slow     string
	queries  []string
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
		version: func(ctx context.Context, binary string, _ []string) (string, error) {
			n := f.running.Add(1)
			defer f.running.Add(-1)
			for {
				peak := f.peak.Load()
				if n <= peak || f.peak.CompareAndSwap(peak, n) {
					break
				}
			}
			name := filepath.Base(binary)
			f.mu.Lock()
			f.queries = append(f.queries, name)
			f.mu.Unlock()
			delay := f.delay
			if name == f.slow {
				delay = time.Hour
			}
			if delay > 0 {
				select {
				case <-time.After(delay):
				case <-ctx.Done():
					return "", ctx.Err()
				}
			}
			return f.versions[name], nil
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
	synctest.Test(t, func(t *testing.T) {
		fake := newFakeToolchainHost(200 * time.Millisecond)
		probes := newToolchainProbes(fake.host())
		if got := probes.toolchains(t.Context(), []string{"TypeScript", "Go"}); len(got) != 0 {
			t.Fatalf("first read returned %v before any probe completed", got)
		}
		probes.mu.Lock()
		settledDuringFirstRead := !probes.results[pathHash("/resolved/bin")+":go,javascript,typescript"].inflight
		probes.mu.Unlock()
		if settledDuringFirstRead {
			t.Fatal("first read waited for probes instead of returning immediately")
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
	})
}

func TestToolchainProbeKeepsAnswersAndRetriesOnlyUnknowns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := newFakeToolchainHost(time.Second)
		fake.slow = "gradle"
		var focus []string
		for _, tc := range knownToolchains {
			fake.versions[tc.binary] = "v1.2.3"
			focus = append(focus, tc.language)
		}
		probes := newToolchainProbes(fake.host())
		probes.toolchains(t.Context(), focus)
		probes.wg.Wait()
		if got := probes.toolchains(t.Context(), focus); len(got) != len(knownToolchains)-1 || slices.Contains(got, "gradle 1.2.3") {
			t.Fatalf("versions = %v; want every answer except the interrupted query", got)
		}
		probes.wg.Wait()
		if got := len(fake.queries); got != len(knownToolchains) {
			t.Fatalf("first round ran %d queries", got)
		}

		for _, delay := range []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute} {
			fake.queries = nil
			time.Sleep(delay - time.Nanosecond)
			probes.toolchains(t.Context(), focus)
			probes.wg.Wait()
			if len(fake.queries) != 0 {
				t.Fatalf("retried before %v: %v", delay, fake.queries)
			}
			time.Sleep(time.Nanosecond)
			probes.toolchains(t.Context(), focus)
			probes.wg.Wait()
			if !slices.Equal(fake.queries, []string{"gradle"}) {
				t.Fatalf("queries at %v = %v, want gradle only", delay, fake.queries)
			}
		}

		fake.slow = ""
		time.Sleep(8 * time.Minute)
		probes.toolchains(t.Context(), focus)
		probes.wg.Wait()
		if got := probes.toolchains(t.Context(), focus); !slices.Contains(got, "gradle 1.2.3") {
			t.Fatalf("successful retry missing from %v", got)
		}
	})
}

func TestToolchainRetryBackoffIsCapped(t *testing.T) {
	probes := newToolchainProbes(toolchainHost{
		lookPath: func(name, _ string) (string, error) { return name, nil },
		version:  func(context.Context, string, []string) (string, error) { return "", exec.ErrTimeout },
	})
	answer := probes.probe(t.Context(), "", knownToolchains[0], toolchainResultTTL)
	if answer.backoff != toolchainResultTTL {
		t.Fatalf("backoff = %v, want %v", answer.backoff, toolchainResultTTL)
	}
}

func TestCanceledCallerDoesNotCancelTheRefresh(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := newFakeToolchainHost(10 * time.Millisecond)
		probes := newToolchainProbes(fake.host())
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		probes.toolchains(ctx, []string{"Go"})
		probes.wg.Wait()
		if got := probes.toolchains(t.Context(), []string{"Go"}); !slices.Equal(got, []string{"go 1.26.6"}) {
			t.Fatalf("a finished prompt's context cut the refresh short: %v", got)
		}
	})
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
