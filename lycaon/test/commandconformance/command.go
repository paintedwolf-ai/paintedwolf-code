package commandconformance

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"

	"github.com/lycaon/lycaon/test/referenceuserland"
)

const usage = `usage:
  conformance capture [-git-seed] [case...]   run the cases in their reference userlands and store the snapshots
  conformance live [-validate] [-generated-only] [-seed N]... [-programs N]
                                              compare the corpus and generated programs with the live reference userlands`

// Exit statuses: a failed run, a usage error, or a run that could not start.
const (
	exitFailed     = 1
	exitUsage      = 2
	exitUnverified = 2
)

// imageRunner runs the cases in the verified images of the runner's architecture.
type imageRunner interface {
	References
	Manifest() *referenceuserland.Manifest
	Architecture() string
	Close() error
}

type openRunner func(context.Context) (imageRunner, error)

// Command runs the conformance command line and returns the process exit status.
func Command(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "live" {
		return runLive(ctx, args[1:], stdout, stderr, launchLiveDigest)
	}
	return runCommand(ctx, args, stdout, stderr, func(ctx context.Context) (imageRunner, error) {
		r, err := referenceuserland.NewRunner(ctx)
		if err != nil {
			return nil, err
		}
		return r, nil
	})
}

func runCommand(ctx context.Context, args []string, stdout, stderr io.Writer, open openRunner) int {
	if len(args) == 0 || args[0] != "capture" {
		_, _ = fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	flags := flag.NewFlagSet("capture", flag.ContinueOnError)
	flags.SetOutput(stderr)
	gitSeed := flags.Bool("git-seed", false, "record the Git seed repository again before capturing")
	if err := flags.Parse(args[1:]); err != nil {
		return exitUsage
	}
	cases, err := selectCases(Cases(), flags.Args())
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return exitUsage
	}
	if _, err := os.Stat(SnapshotsPath); err != nil {
		_, _ = fmt.Fprintln(stderr, "conformance: capture runs from the Go module root:", err)
		return exitUsage
	}
	runner, err := open(ctx)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "conformance: unverified:", err)
		return exitUnverified
	}
	defer func() { _ = runner.Close() }()
	if err := capture(ctx, runner, cases, *gitSeed, stdout); err != nil {
		_, _ = fmt.Fprintln(stderr, "conformance:", err)
		return exitFailed
	}
	return 0
}

func selectCases(all []Case, ids []string) ([]Case, error) {
	if err := Validate(all); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return all, nil
	}
	var out []Case
	for _, id := range ids {
		i := slices.IndexFunc(all, func(c Case) bool { return c.ID == id })
		if i < 0 {
			return nil, fmt.Errorf("unknown case %q", id)
		}
		out = append(out, all[i])
	}
	return out, nil
}

// capture stores new snapshots for cases and keeps the others the corpus still has.
// Snapshots from other images are discarded, so they are captured again in full.
func capture(ctx context.Context, r imageRunner, cases []Case, gitSeed bool, out io.Writer) error {
	if gitSeed {
		files, err := CaptureGitSeed(ctx, r)
		if err != nil {
			return err
		}
		if err := writeJSON(GitSeedPath, files); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(out, "recorded the Git seed: %d entries\n", len(files))
	}
	seed, err := readGitSeed()
	if err != nil {
		return err
	}
	stored, err := readSnapshots()
	if err != nil {
		return err
	}
	images := Images(r.Manifest())
	if !imagesEqual(stored.Userlands, images) {
		if len(cases) != len(Cases()) {
			return errors.New("the snapshots come from other images; capture every case")
		}
		stored = &Snapshots{Userlands: images, Cases: map[string]map[referenceuserland.ID]Snapshot{}}
	}
	captured, err := Capture(ctx, r, cases, seed)
	if err != nil {
		return err
	}
	maps.Copy(stored.Cases, captured)
	stored.Retain(Cases())
	data, err := stored.Encode()
	if err != nil {
		return err
	}
	if err := os.WriteFile(SnapshotsPath, data, 0o644); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "captured %d cases in %s; %d cases stored\n", len(captured), r.Architecture(), len(stored.Cases))
	return nil
}

// readGitSeed and readSnapshots read the files on disk, which a capture rewrites,
// rather than the copies built into the package.
func readGitSeed() (GitSeed, error) {
	data, err := os.ReadFile(GitSeedPath)
	if err != nil {
		return nil, err
	}
	return decodeGitSeed(data)
}

func readSnapshots() (*Snapshots, error) {
	data, err := os.ReadFile(SnapshotsPath)
	if err != nil {
		return nil, err
	}
	return decodeSnapshots(data)
}

func writeJSON(name string, v any) error {
	data, err := encodeJSON(v, "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(name, data, 0o644)
}
