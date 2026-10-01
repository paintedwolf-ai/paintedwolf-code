package git

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/gitargv"
	"github.com/lycaon/lycaon/internal/gitexec"
)

// RevisionKind names how a typed revision was read.
type RevisionKind string

const (
	RevisionCommit RevisionKind = "commit"
	RevisionBranch RevisionKind = "branch"
	RevisionRange  RevisionKind = "range"
)

// MaxRevisionSpecBytes bounds a typed revision; longer text is never a revision.
const MaxRevisionSpecBytes = 256

// RevisionComparison is a typed revision resolved to two immutable commits.
type RevisionComparison struct {
	Spec  string
	Kind  RevisionKind
	Label string
	// Before is empty when After is a root commit, which compares with the empty tree.
	Before string
	After  string
	// Subject is set when the comparison is one commit against its parent.
	Subject string
}

// ErrRevisionSpecRejected marks text that can never be read as a revision.
var ErrRevisionSpecRejected = errors.New("not a revision")

// ValidRevisionSpec admits one token Git can only read as a revision, never as an option.
func ValidRevisionSpec(spec string) bool {
	if spec == "" || len(spec) > MaxRevisionSpecBytes || !utf8.ValidString(spec) {
		return false
	}
	for _, r := range spec {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	left, right, _, isRange := splitRevisionRange(spec)
	if !isRange {
		return !strings.HasPrefix(spec, "-")
	}
	if left == "" && right == "" {
		return false
	}
	for _, side := range []string{left, right} {
		if strings.HasPrefix(side, "-") || strings.Contains(side, "..") {
			return false
		}
	}
	return true
}

// splitRevisionRange reads Git's range notation; a ref name never contains "..".
func splitRevisionRange(spec string) (left, right, sep string, ok bool) {
	for _, sep := range []string{"...", ".."} {
		if left, right, found := strings.Cut(spec, sep); found {
			return left, right, sep, true
		}
	}
	return "", "", "", false
}

// ResolveRevision reads spec against the repository at projectDir. A commit is
// compared with its first parent; a local branch other than the checked-out one
// with HEAD from their merge base; A..B compares A with B and A...B the merge base
// with B. found is false when the spec names nothing here.
func (m *Manager) ResolveRevision(ctx context.Context, projectDir, spec string) (RevisionComparison, bool, error) {
	if !ValidRevisionSpec(spec) {
		return RevisionComparison{}, false, ErrRevisionSpecRejected
	}
	dir, err := repositoryProjectDir(projectDir)
	if err != nil {
		return RevisionComparison{}, false, err
	}
	if left, right, sep, isRange := splitRevisionRange(spec); isRange {
		return m.resolveRevisionRange(ctx, dir, spec, left, right, sep)
	}
	tip, isBranch, err := localBranchTip(ctx, dir, spec)
	if err != nil {
		return RevisionComparison{}, false, err
	}
	if isBranch {
		return m.resolveBranchRevision(ctx, dir, spec, tip)
	}
	return m.resolveCommitRevision(ctx, dir, spec)
}

func (m *Manager) resolveCommitRevision(ctx context.Context, dir, spec string) (RevisionComparison, bool, error) {
	oid, found, err := revisionCommit(ctx, dir, spec)
	if err != nil || !found {
		return RevisionComparison{}, false, err
	}
	out, err := m.commitAgainstParent(ctx, dir, oid)
	if err != nil {
		return RevisionComparison{}, false, err
	}
	out.Spec, out.Kind = spec, RevisionCommit
	out.Label = strings.TrimSpace(shortObjectID(oid) + " " + out.Subject)
	return out, true, nil
}

// The checked-out branch has nothing beyond HEAD, so its tip commit stands for it.
func (m *Manager) resolveBranchRevision(ctx context.Context, dir, branch, tip string) (RevisionComparison, bool, error) {
	current, err := currentBranchRef(ctx, dir)
	if err != nil {
		return RevisionComparison{}, false, err
	}
	if current == "refs/heads/"+branch {
		out, err := m.commitAgainstParent(ctx, dir, tip)
		if err != nil {
			return RevisionComparison{}, false, err
		}
		out.Spec, out.Kind, out.Label = branch, RevisionBranch, branch+" at "+shortObjectID(tip)
		return out, true, nil
	}
	head, found, err := revisionCommit(ctx, dir, "HEAD")
	if err != nil || !found {
		return RevisionComparison{}, false, err
	}
	base, found, err := mergeBase(ctx, dir, tip, head)
	if err != nil || !found {
		return RevisionComparison{}, false, err
	}
	return RevisionComparison{Spec: branch, Kind: RevisionBranch, Label: branch + "...HEAD", Before: base, After: head}, true, nil
}

func (m *Manager) resolveRevisionRange(ctx context.Context, dir, spec, left, right, sep string) (RevisionComparison, bool, error) {
	// Git reads an omitted side as HEAD.
	if left == "" {
		left = "HEAD"
	}
	if right == "" {
		right = "HEAD"
	}
	before, found, err := revisionCommit(ctx, dir, left)
	if err != nil || !found {
		return RevisionComparison{}, false, err
	}
	after, found, err := revisionCommit(ctx, dir, right)
	if err != nil || !found {
		return RevisionComparison{}, false, err
	}
	if sep == "..." {
		before, found, err = mergeBase(ctx, dir, before, after)
		if err != nil || !found {
			return RevisionComparison{}, false, err
		}
	}
	return RevisionComparison{Spec: spec, Kind: RevisionRange, Label: left + sep + right, Before: before, After: after}, true, nil
}

func (m *Manager) commitAgainstParent(ctx context.Context, dir, oid string) (RevisionComparison, error) {
	details, err := m.CommitDetails(ctx, dir, oid)
	if err != nil {
		return RevisionComparison{}, err
	}
	out := RevisionComparison{After: oid, Subject: commitSubject(details.Message)}
	if len(details.Parents) > 0 {
		out.Before = details.Parents[0]
	}
	return out, nil
}

// revisionCommit peels rev to a commit; a name that resolves to nothing is not an error.
func revisionCommit(ctx context.Context, dir, rev string) (string, bool, error) {
	out, code, err := gitexec.Run(ctx, dir, []string{"--no-replace-objects", "rev-parse", "--verify", "--quiet", gitargv.EndOfOptions, rev + "^{commit}"}, hermeticOpts(0))
	if err != nil {
		return "", false, err
	}
	if code != 0 {
		return "", false, nil
	}
	oid := strings.TrimSpace(string(out))
	if !validObjectID(oid) {
		return "", false, fmt.Errorf("invalid commit object identity")
	}
	return oid, true, nil
}

// localBranchTip answers only for an exact refs/heads name, never Git's ref search order.
func localBranchTip(ctx context.Context, dir, name string) (string, bool, error) {
	if !branchNameShape(name) {
		return "", false, nil
	}
	ref := "refs/heads/" + name
	out, code, err := gitexec.Run(ctx, dir, []string{"for-each-ref", "--format=%(objectname) %(refname)", ref}, hermeticOpts(0))
	if err != nil {
		return "", false, err
	}
	if code != 0 {
		return "", false, fmt.Errorf("git for-each-ref exited %d", code)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		oid, refname, ok := strings.Cut(line, " ")
		if ok && refname == ref && validObjectID(oid) {
			// A branch can name a non-commit object; only commits compare.
			return revisionCommit(ctx, dir, ref)
		}
	}
	return "", false, nil
}

// branchNameShape applies git check-ref-format's rules to a branch name.
func branchNameShape(name string) bool {
	if name == "" || name == "@" || strings.HasPrefix(name, "-") || strings.HasPrefix(name, "/") ||
		strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".") ||
		strings.Contains(name, "..") || strings.Contains(name, "//") || strings.Contains(name, "@{") ||
		strings.ContainsAny(name, "~^:?*[\\") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	for _, r := range name {
		if r <= ' ' || r == 0x7f {
			return false
		}
	}
	return true
}

// currentBranchRef is empty for a detached HEAD.
func currentBranchRef(ctx context.Context, dir string) (string, error) {
	out, code, err := gitexec.Run(ctx, dir, []string{"symbolic-ref", "--quiet", "HEAD"}, hermeticOpts(0))
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", nil
	}
	return strings.TrimSpace(string(out)), nil
}

// mergeBase is not found for unrelated histories.
func mergeBase(ctx context.Context, dir, a, b string) (string, bool, error) {
	out, code, err := gitexec.Run(ctx, dir, []string{"--no-replace-objects", "merge-base", gitargv.EndOfOptions, a, b}, hermeticOpts(0))
	if err != nil {
		return "", false, err
	}
	if code == 1 {
		return "", false, nil
	}
	if code != 0 {
		return "", false, fmt.Errorf("git merge-base exited %d", code)
	}
	oid := strings.TrimSpace(string(out))
	if !validObjectID(oid) {
		return "", false, fmt.Errorf("invalid merge base identity")
	}
	return oid, true, nil
}

func commitSubject(message string) string {
	subject, _, _ := strings.Cut(strings.TrimSpace(message), "\n")
	return strings.TrimSpace(subject)
}

func shortObjectID(oid string) string {
	if len(oid) > 7 {
		return oid[:7]
	}
	return oid
}
