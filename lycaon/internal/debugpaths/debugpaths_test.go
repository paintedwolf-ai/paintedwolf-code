package debugpaths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCatalogIsClosedAndConsistent(t *testing.T) {
	seen := map[string]Kind{}
	for _, f := range catalog {
		if f.Name == "" {
			t.Fatalf("kind %q has no filename", f.Kind)
		}
		if prev, dup := seen[f.Name]; dup {
			t.Fatalf("%s is claimed by both %q and %q", f.Name, prev, f.Kind)
		}
		seen[f.Name] = f.Kind
		if f.FileEnv == "" && f.SiblingOf == "" {
			t.Fatalf("kind %q has neither a redirect variable nor a sibling to follow", f.Kind)
		}
		if f.SiblingOf != "" {
			if _, ok := Entry(f.SiblingOf); !ok {
				t.Fatalf("kind %q follows unknown sibling %q", f.Kind, f.SiblingOf)
			}
			if f.EnableEnv != "" {
				t.Fatalf("kind %q follows a sibling and also carries its own switch", f.Kind)
			}
		}
	}
}

func TestFilePathDefaultsToTheSessionDir(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	clearSwitches(t)
	session := t.TempDir()
	t.Setenv(SessionDirEnv, session)

	if got, want := FilePath(KindLLM), filepath.Join(session, "llm-requests.jsonl"); got != want {
		t.Fatalf("FilePath(KindLLM) = %s, want %s", got, want)
	}
	if got, want := FilePath(KindSessions), filepath.Join(session, "sessions.jsonl"); got != want {
		t.Fatalf("FilePath(KindSessions) = %s, want %s", got, want)
	}
}

// A process with no session dir and no redirect has nowhere to write: the
// catalog never invents a flat location under the config root.
func TestFilePathIsEmptyWithoutASession(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	clearSwitches(t)

	if got := FilePath(KindTool); got != "" {
		t.Fatalf("FilePath(KindTool) = %q, want empty", got)
	}
}

func TestFilePathPrefersOverride(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	clearSwitches(t)
	t.Setenv(SessionDirEnv, t.TempDir())

	redirect := filepath.Join(t.TempDir(), "sse-events.jsonl")
	t.Setenv("LYCAON_SSE_DEBUG_FILE", redirect)

	if got := FilePath(KindSSE); got != redirect {
		t.Fatalf("FilePath(KindSSE) = %s, want the redirect %s", got, redirect)
	}
}

// The manifest and the prompt-cache rows have no variable of their own: the
// writer puts them next to llm-requests.jsonl, so a redirect has to carry them.
func TestSiblingsFollowTheLLMRedirect(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	clearSwitches(t)

	sessionDir := t.TempDir()
	t.Setenv("LYCAON_LLM_DEBUG_FILE", filepath.Join(sessionDir, "llm-requests.jsonl"))

	for kind, want := range map[Kind]string{
		KindSessions:    filepath.Join(sessionDir, "sessions.jsonl"),
		KindPromptCache: filepath.Join(sessionDir, "prompt-cache-observability.jsonl"),
	} {
		if got := FilePath(kind); got != want {
			t.Errorf("FilePath(%q) = %s, want %s", kind, got, want)
		}
	}
}

func TestOverrideIsEmptyWithoutRedirect(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	clearSwitches(t)

	if got := Override(KindSidecarLog); got != "" {
		t.Fatalf("Override(KindSidecarLog) = %q, want empty", got)
	}
	if got := Override(KindSessions); got != "" {
		t.Fatalf("Override(KindSessions) = %q, want empty (no variable of its own)", got)
	}

	t.Setenv("LYCAON_LOG_FILE", "  /tmp/sidecar.log  ")
	if got, want := Override(KindSidecarLog), "/tmp/sidecar.log"; got != want {
		t.Fatalf("Override(KindSidecarLog) = %q, want %q trimmed", got, want)
	}
}

// Each capture turns on by its own switch or the main one; the siblings of
// the LLM capture follow it, and the sidecar log has only the main switch.
func TestEnabledFollowsSwitchesAndSiblings(t *testing.T) {
	clearSwitches(t)
	if AnyEnabled() {
		t.Fatal("nothing enabled, yet AnyEnabled reports a capture")
	}
	t.Setenv("LYCAON_LLM_DEBUG", "1")
	if !Enabled(KindLLM) || !Enabled(KindSessions) || !Enabled(KindPromptCache) {
		t.Fatal("the LLM switch must enable the LLM capture and its siblings")
	}
	if Enabled(KindHTTP) || Enabled(KindSidecarLog) {
		t.Fatal("the LLM switch enabled an unrelated capture")
	}
	if !AnyEnabled() {
		t.Fatal("AnyEnabled must report the LLM capture")
	}
	t.Setenv("LYCAON_LLM_DEBUG", "")
	t.Setenv(FullDebugEnv, "1")
	for _, f := range catalog {
		if !Enabled(f.Kind) {
			t.Fatalf("full debug did not enable %q", f.Kind)
		}
	}
}

// The engine mints one stamped session dir per run when any capture is on,
// publishes it for child processes, and points `latest` at it.
func TestEnsureSessionDirMintsOnceAndPointsLatest(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", root)
	clearSwitches(t)

	dir, err := EnsureSessionDir()
	testutil.FailErr(t, "ensure with nothing enabled", err)
	if dir != "" {
		t.Fatalf("minted a session dir with no capture enabled: %s", dir)
	}

	t.Setenv("LYCAON_TOOL_DEBUG", "1")
	dir, err = EnsureSessionDir()
	testutil.FailErr(t, "ensure session dir", err)
	if !strings.HasPrefix(dir, SessionsRoot()+string(filepath.Separator)) {
		t.Fatalf("session dir %s is not under %s", dir, SessionsRoot())
	}
	if _, err := ParseSessionStamp(filepath.Base(dir)); err != nil {
		t.Fatalf("session dir name does not parse as a stamp: %v", err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("session dir not created: %v", err)
	}
	if ActiveSessionDir() != dir {
		t.Fatalf("session dir not published: %q", ActiveSessionDir())
	}
	if got, want := FilePath(KindTool), filepath.Join(dir, "tool-invocations.jsonl"); got != want {
		t.Fatalf("FilePath(KindTool) = %s, want %s", got, want)
	}
	latest, err := filepath.EvalSymlinks(LatestLink())
	testutil.FailErr(t, "resolve latest", err)
	if want, _ := filepath.EvalSymlinks(dir); latest != want {
		t.Fatalf("latest -> %s, want %s", latest, want)
	}
	again, err := EnsureSessionDir()
	testutil.FailErr(t, "ensure again", err)
	if again != dir {
		t.Fatalf("second ensure minted %s, want the existing %s", again, dir)
	}
}

// A dev script that minted the directory ahead of launch wins over minting.
func TestEnsureSessionDirHonorsAnExportedDir(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	clearSwitches(t)
	exported := t.TempDir()
	t.Setenv(SessionDirEnv, exported)
	t.Setenv(FullDebugEnv, "1")

	dir, err := EnsureSessionDir()
	testutil.FailErr(t, "ensure session dir", err)
	if dir != exported {
		t.Fatalf("ensure returned %s, want the exported %s", dir, exported)
	}
}

// Two engines started within one second on one config root get distinct dirs.
func TestMintSessionDirAvoidsCollisions(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	at := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	first, err := mintSessionDir(at)
	testutil.FailErr(t, "mint first", err)
	second, err := mintSessionDir(at)
	testutil.FailErr(t, "mint second", err)
	if first == second {
		t.Fatalf("both mints produced %s", first)
	}
	for _, dir := range []string{first, second} {
		stamp, err := ParseSessionStamp(filepath.Base(dir))
		testutil.FailErr(t, "parse stamp", err)
		if !stamp.Equal(at) {
			t.Fatalf("%s parses to %s, want %s", dir, stamp, at)
		}
	}
}

func TestLayoutUnderRoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", root)

	debug := filepath.Join(root, "debug")
	for name, got := range map[string]string{
		"DebugRoot":    DebugRoot(),
		"SessionsRoot": SessionsRoot(),
		"LatestLink":   LatestLink(),
		"SessionDir":   SessionDir("20260807T120000Z"),
	} {
		want := map[string]string{
			"DebugRoot":    debug,
			"SessionsRoot": filepath.Join(debug, "sessions"),
			"LatestLink":   filepath.Join(debug, "latest"),
			"SessionDir":   filepath.Join(debug, "sessions", "20260807T120000Z"),
		}[name]
		if got != want {
			t.Errorf("%s() = %s, want %s", name, got, want)
		}
	}
	if got, want := DebugRootUnder("/somewhere"), filepath.Join("/somewhere", "debug"); got != want {
		t.Errorf("DebugRootUnder = %s, want %s", got, want)
	}
}

// An unresolvable config root must still put writer and reader in the same place.
func TestRootDegradesToSharedTempDir(t *testing.T) {
	clearSwitches(t)

	// A config root under an existing regular file can never be created.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("not a dir"), 0o600); err != nil {
		testutil.FailErr(t, "write blocker file", err)
	}
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(blocker, "config"))

	fallback := filepath.Join(os.TempDir(), tempFallbackDir)
	if got := Root(); got != fallback {
		t.Fatalf("Root() = %s, want temp fallback %s", got, fallback)
	}
	if got, want := SessionsRoot(), filepath.Join(fallback, "debug", "sessions"); got != want {
		t.Fatalf("SessionsRoot() = %s, want %s", got, want)
	}
}

func TestUnknownKindResolvesToNothing(t *testing.T) {
	if got := FilePath(Kind("nope")); got != "" {
		t.Fatalf("FilePath(unknown) = %q, want empty", got)
	}
	if got := Name(Kind("nope")); got != "" {
		t.Fatalf("Name(unknown) = %q, want empty", got)
	}
	if Enabled(Kind("nope")) {
		t.Fatal("unknown kind reported enabled")
	}
}

// clearSwitches drops every switch, redirect, and session variable so a
// developer's own exports cannot decide what these tests observe.
func clearSwitches(t *testing.T) {
	t.Helper()
	for _, f := range catalog {
		if f.FileEnv != "" {
			t.Setenv(f.FileEnv, "")
		}
		if f.EnableEnv != "" {
			t.Setenv(f.EnableEnv, "")
		}
	}
	t.Setenv(FullDebugEnv, "")
	t.Setenv(SessionDirEnv, "")
}
