package commandconformance

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
)

// liveSelection is a bounded live differential run: the generated seeds and
// program count the live tests read from the environment, and whether the
// corpus comparison runs too.
type liveSelection struct {
	seeds         []uint64
	programs      int
	generatedOnly bool
	validate      bool
}

type seedList []uint64

func (s *seedList) String() string { return fmt.Sprint([]uint64(*s)) }

func (s *seedList) Set(value string) error {
	seed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return errors.New("a seed must be an unsigned 64-bit integer")
	}
	if slices.Contains(*s, seed) {
		return fmt.Errorf("seed %d is selected twice", seed)
	}
	*s = append(*s, seed)
	return nil
}

// parseLiveSelection reports its own errors; false means a usage error.
func parseLiveSelection(args []string, stderr io.Writer) (liveSelection, bool) {
	var selection liveSelection
	flags := flag.NewFlagSet("live", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Var((*seedList)(&selection.seeds), "seed", "generated seed to compare (repeatable)")
	flags.IntVar(&selection.programs, "programs", GeneratedProgramsPerSeed, "generated programs per seed")
	flags.BoolVar(&selection.generatedOnly, "generated-only", false, "compare generated programs only")
	flags.BoolVar(&selection.validate, "validate", false, "check the selection without running it")
	if err := flags.Parse(args); err != nil {
		return liveSelection{}, false
	}
	var problem string
	switch {
	case flags.NArg() > 0:
		problem = fmt.Sprintf("unexpected argument %q", flags.Arg(0))
	case len(selection.seeds) > MaxGeneratedSeeds:
		problem = fmt.Sprintf("select at most %d distinct seeds", MaxGeneratedSeeds)
	case selection.programs < 1 || selection.programs > MaxGeneratedPrograms:
		problem = fmt.Sprintf("program count must be between 1 and %d", MaxGeneratedPrograms)
	default:
		return selection, true
	}
	_, _ = fmt.Fprintln(stderr, "conformance:", problem)
	return liveSelection{}, false
}

// environment carries the selection to the live tests; no seeds keeps their
// default seeds.
func (s liveSelection) environment() []string {
	seeds := make([]string, len(s.seeds))
	for i, seed := range s.seeds {
		seeds[i] = strconv.FormatUint(seed, 10)
	}
	return []string{"PW_CONFORMANCE_SEEDS=" + strings.Join(seeds, ","), "PW_CONFORMANCE_PROGRAMS=" + strconv.Itoa(s.programs)}
}

// digestArguments run the selected live tests through the repository's Go test
// digest, with the reference userland tests compiled in.
func (s liveSelection) digestArguments() []string {
	pattern := "^TestLive(Reference|Generated)Differential$"
	if s.generatedOnly {
		pattern = "^TestLiveGeneratedDifferential$"
	}
	return []string{"scripts/go-test-digest.sh", "--full", "--tags", "referenceuserland",
		"--name", "test:conformance:live", "--", "./test/wiring/conformance/...",
		"-run", pattern, "-count=1"}
}

// liveLauncher runs the digest from the repository root and returns its exit status.
type liveLauncher func(ctx context.Context, selection liveSelection, stdout, stderr io.Writer) int

func launchLiveDigest(ctx context.Context, selection liveSelection, stdout, stderr io.Writer) int {
	cmd := exec.CommandContext(ctx, "bash", selection.digestArguments()...)
	cmd.Dir = ".."
	cmd.Env = append(os.Environ(), selection.environment()...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "conformance: unverified:", err)
		return exitUnverified
	}
	return 0
}

func runLive(ctx context.Context, args []string, stdout, stderr io.Writer, launch liveLauncher) int {
	selection, ok := parseLiveSelection(args, stderr)
	if !ok {
		return exitUsage
	}
	if selection.validate {
		return 0
	}
	if _, err := os.Stat(SnapshotsPath); err != nil {
		_, _ = fmt.Fprintln(stderr, "conformance: live runs from the Go module root:", err)
		return exitUsage
	}
	return launch(ctx, selection, stdout, stderr)
}
