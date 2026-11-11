package checkpoint

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/gitexec"
)

// Publishing a worker's Git objects relies on one destination fact: the objects reachable from its
// ref tips at the start of publication are present and intact. Those tips are the boundary. Only the
// objects the worker's tips reach and the boundary does not are packed, from the checkpoint's own
// objects and the destination's, and Git reads them back into the destination, recomputing every
// object's identity. Connectivity is then proved from the worker's tips down to the boundary and no
// further, so the work follows the worker's new objects rather than the history beneath them.
//
// A boundary object that is missing or damaged fails publication with ErrCheckpointGitBoundary before
// any ref moves. Damage elsewhere in the destination's history is outside this claim.

// ErrCheckpointGitBoundary is a publication the destination's established history cannot support.
var ErrCheckpointGitBoundary = errors.New("checkpoint: destination history does not support the worker's commits")

// gitPublication is one root's transfer: the worker's tips and where their objects may come from.
type gitPublication struct {
	repoRoot, commonGitDir string
	// sourceObjectDirs hold the objects the checkpoint and its stacked parents added.
	sourceObjectDirs []string
	tips             []string
}

// GitPublishStats counts one publication's transfer.
type GitPublishStats struct {
	Boundary int
	Objects  int
}

// publishGitObjects moves the objects pub.tips add over the destination's boundary into the
// destination and proves them connected to it.
func publishGitObjects(ctx context.Context, pub gitPublication) (GitPublishStats, error) {
	destObjects := filepath.Join(pub.commonGitDir, "objects")
	boundary, err := destinationTips(ctx, pub.repoRoot, pub.commonGitDir)
	if err != nil {
		return GitPublishStats{}, err
	}
	stats := GitPublishStats{Boundary: len(boundary)}
	stage, err := os.MkdirTemp(pub.commonGitDir, "lycaon-publish-")
	if err != nil {
		return stats, err
	}
	defer func() { _ = os.RemoveAll(stage) }()
	source, err := borrowingRepository(stage, append(append([]string(nil), pub.sourceObjectDirs...), destObjects))
	if err != nil {
		return stats, err
	}
	var revisions bytes.Buffer
	for _, tip := range pub.tips {
		fmt.Fprintln(&revisions, tip)
	}
	for _, tip := range boundary {
		fmt.Fprintf(&revisions, "^%s\n", tip)
	}
	var packed bytes.Buffer
	diag, code, err := gitexec.RunTo(ctx, pub.repoRoot, []string{"--git-dir=" + source, "pack-objects", "--revs", "--quiet", filepath.Join(stage, "pack")},
		gitexec.Opts{Profile: gitexec.ProfileHermetic, Input: revisions.Bytes()}, &packed)
	if err != nil || code != 0 {
		return stats, errors.Join(err, fmt.Errorf("%w: pack the worker's objects (exit %d): %s", ErrCheckpointGitBoundary, code, strings.TrimSpace(string(diag))))
	}
	hash := strings.TrimSpace(packed.String())
	if hash != "" {
		objects, err := admitPack(ctx, pub.repoRoot, pub.commonGitDir, filepath.Join(stage, "pack-"+hash+".pack"))
		if err != nil {
			return stats, err
		}
		stats.Objects = objects
	}
	return stats, proveConnected(ctx, pub.repoRoot, pub.commonGitDir, pub.tips, boundary)
}

// destinationTips is the destination's boundary: every ref tip and a detached HEAD.
func destinationTips(ctx context.Context, repoRoot, commonGitDir string) ([]string, error) {
	out, code, err := gitexec.Run(ctx, repoRoot, []string{"--git-dir=" + commonGitDir, "for-each-ref", "--format=%(objectname)"},
		gitexec.Opts{Profile: gitexec.ProfileHermetic})
	if err != nil || code != 0 {
		return nil, errors.Join(err, fmt.Errorf("checkpoint: list destination refs (exit %d): %s", code, strings.TrimSpace(string(out))))
	}
	seen := map[string]bool{}
	var tips []string
	add := func(oid string) {
		if oid = strings.ToLower(strings.TrimSpace(oid)); isHexOID(oid) && !isAllZeroOID(oid) && !seen[oid] {
			seen[oid] = true
			tips = append(tips, oid)
		}
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		add(line)
	}
	if head, code, err := gitexec.Run(ctx, repoRoot, []string{"--git-dir=" + commonGitDir, "rev-parse", "--verify", "--quiet", "HEAD"},
		gitexec.Opts{Profile: gitexec.ProfileHermetic}); err == nil && code == 0 {
		add(string(head))
	}
	return tips, nil
}

// borrowingRepository is a scratch bare repository that reads objects from dirs and holds none.
func borrowingRepository(stage string, dirs []string) (string, error) {
	gitDir := filepath.Join(stage, "source.git")
	for _, dir := range []string{"objects/info", "objects/pack", "refs"} {
		if err := os.MkdirAll(filepath.Join(gitDir, dir), 0o700); err != nil {
			return "", err
		}
	}
	var alternates strings.Builder
	for _, dir := range dirs {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			alternates.WriteString(dir + "\n")
		}
	}
	files := map[string]string{
		"HEAD":                    "ref: refs/heads/main\n",
		"config":                  "[core]\n\trepositoryformatversion = 0\n\tbare = true\n",
		"objects/info/alternates": alternates.String(),
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(gitDir, name), []byte(content), 0o600); err != nil {
			return "", err
		}
	}
	return gitDir, nil
}

// admitPack has the destination's Git index the pack, which recomputes every object's identity and
// checks each object's form, then publishes the pack with its index last so a reader never sees a
// pack without one.
func admitPack(ctx context.Context, repoRoot, commonGitDir, pack string) (int, error) {
	index := strings.TrimSuffix(pack, ".pack") + ".verified.idx"
	out, code, err := gitexec.Run(ctx, repoRoot, []string{"--git-dir=" + commonGitDir, "index-pack", "--strict", "-o", index, pack},
		gitexec.Opts{Profile: gitexec.ProfileHermetic})
	if err != nil || code != 0 {
		return 0, errors.Join(err, fmt.Errorf("%w: verify the worker's objects (exit %d): %s", ErrCheckpointGitBoundary, code, strings.TrimSpace(string(out))))
	}
	objects, err := packObjectCount(pack)
	if err != nil {
		return 0, err
	}
	packDir := filepath.Join(commonGitDir, "objects", "pack")
	if err := os.MkdirAll(packDir, 0o755); err != nil {
		return 0, err
	}
	base := filepath.Join(packDir, strings.TrimSuffix(filepath.Base(pack), ".pack"))
	if err := os.Rename(pack, base+".pack"); err != nil {
		return 0, err
	}
	if err := syncDir(packDir); err != nil {
		return 0, err
	}
	if err := os.Rename(index, base+".idx"); err != nil {
		return 0, err
	}
	return objects, syncDir(packDir)
}

// packObjectCount reads the object count from a pack's header.
func packObjectCount(pack string) (int, error) {
	f, err := os.Open(pack)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	var header [12]byte
	if _, err := f.ReadAt(header[:], 0); err != nil {
		return 0, err
	}
	if string(header[:4]) != "PACK" {
		return 0, fmt.Errorf("checkpoint: %s is not a pack", pack)
	}
	return int(uint32(header[8])<<24 | uint32(header[9])<<16 | uint32(header[10])<<8 | uint32(header[11])), nil
}

// proveConnected walks every object the tips reach down to the boundary, which fails on any object
// the destination lacks, including a missing boundary object.
func proveConnected(ctx context.Context, repoRoot, commonGitDir string, tips, boundary []string) error {
	args := []string{"--git-dir=" + commonGitDir, "rev-list", "--objects", "--quiet", "--stdin"}
	var revisions bytes.Buffer
	for _, tip := range tips {
		fmt.Fprintln(&revisions, tip)
	}
	for _, tip := range boundary {
		fmt.Fprintf(&revisions, "^%s\n", tip)
	}
	out, code, err := gitexec.Run(ctx, repoRoot, args, gitexec.Opts{Profile: gitexec.ProfileHermetic, Input: revisions.Bytes()})
	if err != nil || code != 0 {
		return errors.Join(err, fmt.Errorf("%w: connect the worker's commits (exit %d): %s", ErrCheckpointGitBoundary, code, strings.TrimSpace(string(out))))
	}
	return nil
}
