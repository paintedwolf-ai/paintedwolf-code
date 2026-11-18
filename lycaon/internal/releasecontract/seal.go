// Package releasecontract checks a candidate tree against the contracts a
// published release fixed, and seals released workflows into their archives.
package releasecontract

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/gitexec"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

const sumsFile = "SHA256SUMS"

// Seal writes the archive of a workflow as the tagged release shipped it, or
// with checkOnly verifies that the committed archive matches.
func Seal(ctx context.Context, repoRoot, pack, workflowID, tag string, checkOnly bool) (string, error) {
	release := gitRelease{ctx: ctx, repoRoot: repoRoot, tag: strings.TrimSpace(tag)}
	packRel := path.Join("lycaon", "config", "packs", strings.Trim(pack, "/"))
	return seal(release, packRel, strings.Trim(workflowID, "/"), filepath.Join(repoRoot, filepath.FromSlash(packRel)), checkOnly)
}

// releaseSource reads one release's tree.
type releaseSource interface {
	show(rel string) ([]byte, error)
	list(dir string) ([]string, error)
	paths(dir string) ([]string, error)
}

// seal writes or verifies the archive of workflowID under packDir.
func seal(release releaseSource, packRel, workflowID, packDir string, checkOnly bool) (string, error) {
	files, version, err := sealedFiles(release, packRel, workflowID)
	if err != nil {
		return "", err
	}
	archiveRel := filepath.Join(extpacks.ArchiveKindRoot, workflowID, version)
	archiveDir := filepath.Join(packDir, archiveRel)
	if checkOnly {
		if err := verifyArchive(archiveDir, files); err != nil {
			return "", err
		}
		return fmt.Sprintf("%s matches the release (%d files)", archiveDir, len(files)-1), nil
	}
	for rel, data := range files {
		if _, err := fseffect.Replace(fseffect.ReplaceRequest{
			Location: fseffect.Location{Root: packDir, Rel: filepath.Join(archiveRel, filepath.FromSlash(rel))},
			Source:   bytes.NewReader(data),
			Mode:     0o644,
			DirMode:  0o755,
		}); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("sealed %s@%s into %s (%d files)", workflowID, version, archiveDir, len(files)-1), nil
}

// sealedFiles reads the sealed set from the release, keyed by archive-relative
// path, with its SHA256SUMS ledger.
func sealedFiles(release releaseSource, packRel, workflowID string) (map[string][]byte, string, error) {
	manifestBytes, err := release.show(path.Join(packRel, "workflows", workflowID, "workflow.yaml"))
	if err != nil {
		return nil, "", err
	}
	m, err := workflowdef.ParseManifestYAML(manifestBytes)
	if err != nil {
		return nil, "", fmt.Errorf("released manifest: %w", err)
	}
	files := map[string][]byte{"workflow.yaml": manifestBytes}
	for _, inject := range m.Injects {
		if render := strings.TrimSpace(inject.Render); render != "" {
			rel := path.Join("guidance", render+".md")
			if files[rel], err = releasedGuidance(release, rel); err != nil {
				return nil, "", fmt.Errorf("inject %q: %w", render, err)
			}
		}
	}
	feedbackDir := path.Join("guidance", extpacks.GuidanceGateFeedbackDir)
	names, err := release.list(path.Join(packRel, feedbackDir))
	if err != nil {
		return nil, "", err
	}
	for _, name := range names {
		if !strings.HasSuffix(name, ".yaml") {
			continue
		}
		rel := path.Join(feedbackDir, name)
		if files[rel], err = release.show(path.Join(packRel, rel)); err != nil {
			return nil, "", err
		}
	}
	files[sumsFile] = checksums(files)
	return files, m.Version, nil
}

func checksums(files map[string][]byte) []byte {
	paths := make([]string, 0, len(files))
	for rel := range files {
		paths = append(paths, rel)
	}
	sort.Strings(paths)
	var out bytes.Buffer
	for _, rel := range paths {
		sum := sha256.Sum256(files[rel])
		fmt.Fprintf(&out, "%s  %s\n", hex.EncodeToString(sum[:]), rel)
	}
	return out.Bytes()
}

// verifyArchive reads through an os.Root so a release path cannot name a file
// outside the archive.
func verifyArchive(archiveDir string, files map[string][]byte) error {
	root, err := os.OpenRoot(archiveDir)
	if err != nil {
		return fmt.Errorf("archive %s: %w", archiveDir, err)
	}
	defer func() { _ = root.Close() }()
	if err := fs.WalkDir(root.FS(), ".", func(rel string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || isIgnoredPackFile(entry.Name()) {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("archive %s: %s is not a regular file", archiveDir, rel)
		}
		if _, ok := files[filepath.ToSlash(rel)]; !ok {
			return fmt.Errorf("archive %s: unexpected file %s", archiveDir, rel)
		}
		return nil
	}); err != nil {
		return err
	}
	for rel, want := range files {
		got, err := root.ReadFile(filepath.FromSlash(rel))
		if err != nil {
			return fmt.Errorf("archive %s: %w", archiveDir, err)
		}
		if !bytes.Equal(got, want) {
			return fmt.Errorf("archive %s: %s differs from the release", archiveDir, rel)
		}
	}
	return nil
}

// gitRelease reads a tagged tree through the bundled git.
type gitRelease struct {
	ctx      context.Context
	repoRoot string
	tag      string
}

func (g gitRelease) show(rel string) ([]byte, error) {
	return g.run("show", g.tag+":"+rel)
}

func (g gitRelease) list(dir string) ([]string, error) {
	out, err := g.run("ls-tree", "--name-only", "-z", g.tag, "--", dir+"/")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel != "" {
			names = append(names, path.Base(rel))
		}
	}
	return names, nil
}

func (g gitRelease) run(args ...string) ([]byte, error) {
	out, code, err := gitexec.Run(g.ctx, g.repoRoot, args, gitexec.Opts{})
	if err == nil && code != 0 {
		err = errors.New(string(bytes.TrimSpace(out)))
	}
	if err != nil {
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}
