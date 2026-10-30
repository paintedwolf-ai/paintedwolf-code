package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/gitlease"
)

// BisectState is a bisect session as Git records it: BISECT_* files in the
// worktree's Git directory and refs under refs/bisect, so the host and a
// user's own git bisect read and continue the same session.
type BisectState struct {
	Active bool
	// Start is the branch name or commit the session returns to.
	Start string
	// New and Old are the terms for the bad and good states.
	New, Old string
	// Names is the quoted pathspec record, including its leading "--".
	Names string
	Log   string
	Bad   string
	Good  []string
	Skip  []string
}

// BisectStep is the next action of a session.
type BisectStep struct {
	// Waiting names the missing verdicts before a search can begin.
	Waiting string
	// First is the first new commit once it is determined.
	First string
	// Candidates are the possible first new commits when only skipped
	// commits remain.
	Candidates []string
	// Next is the commit to test, with Git's remaining count and steps.
	Next             string
	Remaining, Steps int
}

var bisectFiles = []string{"BISECT_START", "BISECT_TERMS", "BISECT_NAMES", "BISECT_LOG", "BISECT_EXPECTED_REV", "BISECT_ANCESTORS_OK", "BISECT_RUN", "BISECT_FIRST_PARENT", "BISECT_HEAD"}

func bisectPath(ctx context.Context, dir, name string) (string, error) {
	out, code, err := gitexec.Run(ctx, dir, []string{"rev-parse", "--path-format=absolute", "--git-path", name}, hermeticOpts(0))
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("locate %s: %s", name, bytes.TrimSpace(out))
	}
	return filepath.Clean(strings.TrimSpace(string(out))), nil
}

func readBisectFile(ctx context.Context, dir, name string) (string, bool, error) {
	path, err := bisectPath(ctx, dir, name)
	if err != nil {
		return "", false, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	return string(data), err == nil, err
}

// ReadBisect reads the session state; Active is false outside a session.
func ReadBisect(ctx context.Context, dir string) (BisectState, error) {
	state := BisectState{New: "bad", Old: "good"}
	start, ok, err := readBisectFile(ctx, dir, "BISECT_START")
	if err != nil || !ok {
		return state, err
	}
	state.Active, state.Start = true, strings.TrimSpace(start)
	if terms, ok, err := readBisectFile(ctx, dir, "BISECT_TERMS"); err != nil {
		return state, err
	} else if ok {
		if lines := strings.Split(strings.TrimSpace(terms), "\n"); len(lines) == 2 {
			state.New, state.Old = lines[0], lines[1]
		}
	}
	if state.Names, _, err = readBisectFile(ctx, dir, "BISECT_NAMES"); err != nil {
		return state, err
	}
	state.Names = strings.TrimSuffix(state.Names, "\n")
	if state.Log, _, err = readBisectFile(ctx, dir, "BISECT_LOG"); err != nil {
		return state, err
	}
	err = bisectLines(ctx, dir, []string{"for-each-ref", "--format=%(refname) %(objectname)", "refs/bisect/"}, func(line string) error {
		name, oid, _ := strings.Cut(line, " ")
		switch {
		case name == "refs/bisect/"+state.New:
			state.Bad = oid
		case strings.HasPrefix(name, "refs/bisect/"+state.Old+"-"):
			state.Good = append(state.Good, oid)
		case strings.HasPrefix(name, "refs/bisect/skip-"):
			state.Skip = append(state.Skip, oid)
		}
		return nil
	})
	return state, err
}

// WriteBisect records the session files and refs under the repository lease.
// Log text is appended; an empty Start keeps the existing file.
func WriteBisect(ctx context.Context, dir string, files map[string]string, appendLog string, refs map[string]string) error {
	release, err := gitlease.Repository(ctx, dir)
	if err != nil {
		return err
	}
	defer release()
	for name, content := range files {
		path, err := bisectPath(ctx, dir, name)
		if err != nil {
			return err
		}
		if _, err := fseffect.Replace(fseffect.ReplaceRequest{Location: fseffect.Location{Root: filepath.Dir(path), Rel: filepath.Base(path)}, Source: strings.NewReader(content), Mode: 0o644, PreserveMode: true}); err != nil {
			return err
		}
	}
	if appendLog != "" {
		path, err := bisectPath(ctx, dir, "BISECT_LOG")
		if err != nil {
			return err
		}
		f, err := fseffect.OpenWrite(fseffect.Location{Root: filepath.Dir(path), Rel: filepath.Base(path)}, true, 0o644)
		if err != nil {
			return err
		}
		_, err = f.WriteString(appendLog)
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
	}
	for ref, oid := range refs {
		if _, err := restoreGit(ctx, dir, hermeticOpts(0), "update-ref", "--no-deref", ref, oid); err != nil {
			return err
		}
	}
	return nil
}

// ClearBisect removes every session file and ref.
func ClearBisect(ctx context.Context, dir string) error {
	release, err := gitlease.Repository(ctx, dir)
	if err != nil {
		return err
	}
	defer release()
	var refs []string
	if err := bisectLines(ctx, dir, []string{"for-each-ref", "--format=%(refname)", "refs/bisect/"}, func(line string) error {
		refs = append(refs, line)
		return nil
	}); err != nil {
		return err
	}
	for _, ref := range refs {
		if _, err := restoreGit(ctx, dir, hermeticOpts(0), "update-ref", "--no-deref", "-d", ref); err != nil {
			return err
		}
	}
	for _, name := range bisectFiles {
		path, err := bisectPath(ctx, dir, name)
		if err != nil {
			return err
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// NextBisect chooses the next commit with Git's own bisection order. A
// skipped commit is passed over for the next best candidate.
func NextBisect(ctx context.Context, dir string, state BisectState) (BisectStep, error) {
	switch {
	case state.Bad == "" && len(state.Good) == 0:
		return BisectStep{Waiting: "status: waiting for both " + state.Old + " and " + state.New + " commits"}, nil
	case state.Bad == "":
		count := strconv.Itoa(len(state.Good)) + " " + state.Old + " commit"
		if len(state.Good) != 1 {
			count += "s"
		}
		return BisectStep{Waiting: "status: waiting for " + state.New + " commit, " + count + " known"}, nil
	case len(state.Good) == 0:
		return BisectStep{Waiting: "status: waiting for " + state.Old + " commit(s), " + state.New + " commit known"}, nil
	}
	revs := append([]string{state.Bad, "--not"}, state.Good...)
	revs = append(revs, bisectPathspec(state.Names)...)
	var step BisectStep
	out, code, err := gitexec.Run(ctx, dir, append([]string{"rev-list", "--bisect-vars"}, revs...), hermeticOpts(0))
	if err != nil {
		return step, err
	}
	if code != 0 {
		return step, fmt.Errorf("compute bisection: %s", bytes.TrimSpace(out))
	}
	vars := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			vars[key] = strings.Trim(value, "'")
		}
	}
	step.Next = vars["bisect_rev"]
	// Git reports the commits left if the tested one turns out old.
	step.Remaining, _ = strconv.Atoi(vars["bisect_good"])
	step.Steps, _ = strconv.Atoi(vars["bisect_steps"])
	if step.Next == "" || step.Next == state.Bad {
		if all, _ := strconv.Atoi(vars["bisect_all"]); all <= 1 {
			return BisectStep{First: state.Bad}, nil
		}
	}
	skipped := map[string]bool{}
	for _, oid := range state.Skip {
		skipped[oid] = true
	}
	if !skipped[step.Next] {
		return step, nil
	}
	var order []string
	if err := bisectLines(ctx, dir, append([]string{"rev-list", "--bisect-all"}, revs...), func(line string) error {
		oid, _, _ := strings.Cut(line, " ")
		order = append(order, oid)
		return nil
	}); err != nil {
		return step, err
	}
	for _, oid := range order {
		if oid != state.Bad && !skipped[oid] {
			// Git counts what remains if this commit turns out old: the
			// candidates it does not reach.
			reach := append([]string{"rev-list", "--count", oid, "--not"}, state.Good...)
			out, err := restoreGit(ctx, dir, hermeticOpts(0), append(reach, bisectPathspec(state.Names)...)...)
			if err != nil {
				return step, err
			}
			reaches, _ := strconv.Atoi(strings.TrimSpace(string(out)))
			all, _ := strconv.Atoi(vars["bisect_all"])
			step.Next, step.Remaining = oid, all-reaches-1
			return step, nil
		}
	}
	step.Next = ""
	for _, oid := range order {
		if oid == state.Bad || skipped[oid] {
			step.Candidates = append(step.Candidates, oid)
		}
	}
	return step, nil
}

// bisectLines yields each nonempty line of a Git listing.
func bisectLines(ctx context.Context, dir string, args []string, yield func(string) error) error {
	out, err := restoreGit(ctx, dir, hermeticOpts(0), args...)
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			if err := yield(line); err != nil {
				return err
			}
		}
	}
	return nil
}

// bisectPathspec decodes the single-quoted words BISECT_NAMES records, where
// a quote inside a word is written as '\”.
func bisectPathspec(names string) []string {
	var words []string
	var word strings.Builder
	quoted, inWord := false, false
	for i := 0; i < len(names); i++ {
		c := names[i]
		switch {
		case quoted && c == '\'':
			quoted = false
		case quoted:
			word.WriteByte(c)
		case c == '\'':
			quoted, inWord = true, true
		case c == '\\' && i+1 < len(names):
			i++
			word.WriteByte(names[i])
		case c == ' ' && inWord:
			words = append(words, word.String())
			word.Reset()
			inWord = false
		}
	}
	if inWord {
		words = append(words, word.String())
	}
	return words
}

// QuoteBisectWords quotes words the way bisect records them in its files.
func QuoteBisectWords(words []string) string {
	quoted := make([]string, len(words))
	for i, word := range words {
		quoted[i] = "'" + strings.ReplaceAll(word, "'", `'\''`) + "'"
	}
	return strings.Join(quoted, " ")
}

// BisectStart names what a session returns to: the current branch, or the
// commit HEAD is detached at.
func BisectStart(ctx context.Context, dir string) (string, error) {
	out, code, err := gitexec.Run(ctx, dir, []string{"symbolic-ref", "-q", "--short", "HEAD"}, hermeticOpts(0))
	if err != nil {
		return "", err
	}
	if code == 0 {
		return strings.TrimSpace(string(out)), nil
	}
	return resolveCommit(ctx, dir, "HEAD")
}

// CommitSubject is a commit's subject line as bisect prints it.
func CommitSubject(ctx context.Context, dir, oid string) (string, error) {
	out, err := restoreGit(ctx, dir, hermeticOpts(0), "log", "-1", "--format=%s", oid, "--")
	return strings.TrimSuffix(string(out), "\n"), err
}

// ResolveBisectCommit resolves a bisect operand to its commit.
func ResolveBisectCommit(ctx context.Context, dir, rev string) (string, error) {
	return ResolveOptionalObject(ctx, dir, rev+"^{commit}")
}
