package commandconformance

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLiveSelectionDefaultsAndExplicitChoices(t *testing.T) {
	selection, ok := parseLiveSelection(nil, io.Discard)
	if !ok || selection.seeds != nil || selection.programs != 16 || selection.generatedOnly || selection.validate {
		t.Fatalf("default selection = %+v, %v", selection, ok)
	}
	if got := selection.environment(); !slices.Equal(got, []string{"PW_CONFORMANCE_SEEDS=", "PW_CONFORMANCE_PROGRAMS=16"}) {
		t.Fatalf("default environment = %q", got)
	}
	if got := selection.digestArguments(); !slices.Contains(got, "^TestLive(Reference|Generated)Differential$") {
		t.Fatalf("default run = %q", got)
	}
	selection, ok = parseLiveSelection([]string{"--generated-only", "--seed", "0", "--seed", "18446744073709551615", "--programs", "64"}, io.Discard)
	if !ok || !slices.Equal(selection.seeds, []uint64{0, 18446744073709551615}) || selection.programs != 64 || !selection.generatedOnly {
		t.Fatalf("explicit selection = %+v, %v", selection, ok)
	}
	if got := selection.environment(); !slices.Equal(got, []string{"PW_CONFORMANCE_SEEDS=0,18446744073709551615", "PW_CONFORMANCE_PROGRAMS=64"}) {
		t.Fatalf("explicit environment = %q", got)
	}
	want := []string{"scripts/go-test-digest.sh", "--full", "--tags", "referenceuserland", "--name", "test:conformance:live",
		"--", "./test/wiring/conformance/...", "-run", "^TestLiveGeneratedDifferential$", "-count=1"}
	if got := selection.digestArguments(); !slices.Equal(got, want) {
		t.Fatalf("generated-only run = %q", got)
	}
}

func TestLiveSelectionRefusesInvalidOrUnboundedChoices(t *testing.T) {
	tooMany := []string{}
	for seed := range 65 {
		tooMany = append(tooMany, "--seed", strconv.Itoa(seed))
	}
	for _, args := range [][]string{
		{"--seed", "-1"}, {"--seed", "18446744073709551616"}, {"--seed", "1", "--seed", "1"},
		{"--programs", "0"}, {"--programs", "65"}, {"--unknown"}, {"extra"}, tooMany,
	} {
		var stderr bytes.Buffer
		never := func(context.Context, liveSelection, io.Writer, io.Writer) int {
			t.Fatalf("args %q launched the digest", args)
			return 0
		}
		if got := runLive(t.Context(), append([]string{"--validate"}, args...), io.Discard, &stderr, never); got != exitUsage || stderr.Len() == 0 {
			t.Errorf("args %q: status %d, stderr %q", args, got, stderr.String())
		}
	}
}

func TestLiveValidatesWithoutLaunchingAndRunsFromTheModuleRoot(t *testing.T) {
	launched := 0
	launch := func(_ context.Context, selection liveSelection, _, _ io.Writer) int {
		launched++
		if !slices.Equal(selection.seeds, []uint64{7}) {
			t.Fatalf("launched selection = %+v", selection)
		}
		return 3
	}
	captureFixtureRoot(t, "{}")
	if got := runLive(t.Context(), []string{"--validate", "--seed", "7"}, io.Discard, io.Discard, launch); got != 0 || launched != 0 {
		t.Fatalf("validate: status %d, launched %d", got, launched)
	}
	if got := Command(t.Context(), []string{"live", "--validate"}, io.Discard, io.Discard); got != 0 {
		t.Fatalf("conformance live --validate = %d", got)
	}
	if got := runLive(t.Context(), []string{"--seed", "7"}, io.Discard, io.Discard, launch); got != 3 || launched != 1 {
		t.Fatalf("run: status %d, launched %d", got, launched)
	}
	t.Chdir(t.TempDir())
	var stderr bytes.Buffer
	if got := runLive(t.Context(), []string{"--seed", "7"}, io.Discard, &stderr, launch); got != exitUsage || launched != 1 || !strings.Contains(stderr.String(), "module root") {
		t.Fatalf("outside the module root: status %d, launched %d, stderr %q", got, launched, stderr.String())
	}
}

// The launch runs the repository's digest script from the parent of the module
// root, so a fixture script there records what it received.
func TestLiveLaunchRunsTheDigestWithTheSelection(t *testing.T) {
	repository := t.TempDir()
	module := filepath.Join(repository, "lycaon")
	testutil.FailErr(t, "create module root", os.MkdirAll(module, 0o755))
	testutil.FailErr(t, "create scripts", os.MkdirAll(filepath.Join(repository, "scripts"), 0o755))
	script := "printf '%s\\n' \"$PW_CONFORMANCE_SEEDS\" \"$PW_CONFORMANCE_PROGRAMS\" \"$@\" > \"$(pwd)/received\"\nexit 4\n"
	testutil.FailErr(t, "write digest fixture", os.WriteFile(filepath.Join(repository, "scripts", "go-test-digest.sh"), []byte(script), 0o755))
	t.Chdir(module)
	selection := liveSelection{seeds: []uint64{7, 9}, programs: 3, generatedOnly: true}
	if got := launchLiveDigest(t.Context(), selection, io.Discard, io.Discard); got != 4 {
		t.Fatalf("launch status = %d, want the digest's 4", got)
	}
	received, err := os.ReadFile(filepath.Join(repository, "received"))
	testutil.FailErr(t, "read received selection", err)
	want := strings.Join(append([]string{"7,9", "3"}, selection.digestArguments()[1:]...), "\n") + "\n"
	if string(received) != want {
		t.Fatalf("digest received %q, want %q", received, want)
	}

	testutil.FailErr(t, "succeed", os.WriteFile(filepath.Join(repository, "scripts", "go-test-digest.sh"), []byte("exit 0\n"), 0o755))
	if got := launchLiveDigest(t.Context(), selection, io.Discard, io.Discard); got != 0 {
		t.Fatalf("passing digest status = %d", got)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	var stderr bytes.Buffer
	if got := launchLiveDigest(cancelled, selection, io.Discard, &stderr); got != exitUnverified || !strings.Contains(stderr.String(), "unverified") {
		t.Fatalf("digest that could not start: status %d, stderr %q", got, stderr.String())
	}
}
