package extpacks

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/egressclass"
	"github.com/lycaon/lycaon/internal/gitargv"
	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/gitlease"
)

type InstallOptions struct {
	Source          string // https://…, file:///…, path:rel-or-abs
	Version         string // SemVer constraint; default * for released Git packages
	Ref             string // exact Git ref; mutually exclusive with Version
	ProjectDir      string // base for relative path: sources
	ScannersEnabled ScannerRequirementChecker
}

type PackageResolution struct {
	PackID           string   `json:"pack_id"`
	Version          string   `json:"version"`
	ResolvedRevision string   `json:"resolved_revision,omitempty"`
	Integrity        string   `json:"integrity"`
	ExtensionAPI     string   `json:"extension_api"`
	Kind             PackKind `json:"kind"`
}

func packRequirementWarnings(ctx context.Context, root string, opts InstallOptions) []string {
	man, err := LoadManifest(root)
	if err != nil {
		return nil
	}
	var warnings []string
	for _, scannerID := range man.RequiresScanners {
		scannerID = strings.TrimSpace(scannerID)
		if scannerID == "" {
			continue
		}
		met := opts.ScannersEnabled != nil && opts.ScannersEnabled.ScannerEnabled(ctx, "", scannerID)
		if !met {
			warnings = append(warnings, fmt.Sprintf("requires_scanners %q not enabled and runnable", scannerID))
		}
	}
	return warnings
}

func materializeGitSource(ctx context.Context, source, ref string) (staging string, err error) {
	staging, err = os.MkdirTemp("", "lycaon-pack-*")
	if err != nil {
		return "", err
	}
	cleanup := func() { _ = os.RemoveAll(staging) }

	switch {
	case strings.HasPrefix(source, "file://"):
		p, err := fileSourcePath(source)
		if err != nil {
			cleanup()
			return "", err
		}
		info, err := os.Stat(p)
		if err != nil {
			cleanup()
			return "", fmt.Errorf("file source: %w", err)
		}
		if !info.IsDir() {
			cleanup()
			return "", fmt.Errorf("file source must be a directory")
		}
		if _, err := os.Stat(filepath.Join(p, ".git")); err != nil {
			cleanup()
			return "", fmt.Errorf("file source without .git must install as a linked path")
		}
		if err := gitClone(ctx, p, staging, ref); err != nil {
			cleanup()
			return "", err
		}
		return staging, nil

	default:
		if err := gitClone(ctx, source, staging, ref); err != nil {
			cleanup()
			return "", err
		}
		return staging, nil
	}
}

// resolveLinkedLocalSource returns an absolute author folder for path: and non-git file:// sources.
func resolveLinkedLocalSource(source, projectDir string) (abs string, linked bool, err error) {
	switch {
	case strings.HasPrefix(source, "path:"):
		rel := strings.TrimSpace(strings.TrimPrefix(source, "path:"))
		srcPath := rel
		if !filepath.IsAbs(srcPath) {
			base := projectDir
			if base == "" {
				base, _ = os.Getwd()
			}
			srcPath = filepath.Join(base, rel)
		}
		info, err := os.Stat(srcPath)
		if err != nil || !info.IsDir() {
			return "", false, fmt.Errorf("path source %q: directory required", srcPath)
		}
		abs, err = filepath.Abs(srcPath)
		if err != nil {
			return "", false, err
		}
		return abs, true, nil

	case strings.HasPrefix(source, "file://"):
		p, err := fileSourcePath(source)
		if err != nil {
			return "", false, err
		}
		info, err := os.Stat(p)
		if err != nil {
			return "", false, fmt.Errorf("file source: %w", err)
		}
		if !info.IsDir() {
			return "", false, fmt.Errorf("file source must be a directory")
		}
		if _, err := os.Stat(filepath.Join(p, ".git")); err == nil {
			return "", false, nil // git clone path
		}
		abs, err = filepath.Abs(p)
		if err != nil {
			return "", false, err
		}
		return abs, true, nil

	default:
		return "", false, nil
	}
}

func refuseLinkIntoManagedExtensionCaches(absSource string) error {
	cacheRoot, err := CacheRoot()
	if err != nil {
		return err
	}
	metaRoot, err := MetaPackCacheRoot()
	if err != nil {
		return err
	}
	src, err := filepath.EvalSymlinks(filepath.Clean(absSource))
	if err != nil {
		return fmt.Errorf("path link: resolve source: %w", err)
	}
	managed := []struct {
		root    string
		message string
	}{
		{root: cacheRoot, message: "path link: refuse linking a folder inside the extensions cache"},
		{root: metaRoot, message: "path link: refuse linking a folder inside the extensions meta-pack cache"},
	}
	for _, candidate := range managed {
		root, absErr := filepath.Abs(candidate.root)
		if absErr != nil {
			return absErr
		}
		root, resolveErr := resolveSymlinksWithMissingLeaf(filepath.Clean(root))
		if resolveErr != nil {
			return fmt.Errorf("path link: resolve managed cache: %w", resolveErr)
		}
		sep := string(filepath.Separator)
		if src == root || strings.HasPrefix(src+sep, root+sep) {
			return errors.New(candidate.message)
		}
	}
	return nil
}

// resolveSymlinksWithMissingLeaf resolves the nearest existing ancestor.
func resolveSymlinksWithMissingLeaf(p string) (string, error) {
	if rp, err := filepath.EvalSymlinks(p); err == nil {
		return filepath.Clean(rp), nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	dir := filepath.Dir(p)
	if dir == p {
		return "", fmt.Errorf("no existing ancestor for %s", p)
	}
	rp, err := resolveSymlinksWithMissingLeaf(dir)
	if err != nil {
		return "", err
	}
	return filepath.Join(rp, filepath.Base(p)), nil
}

// gitClone leases local sources and the destination before cloning.
func gitClone(ctx context.Context, source, dest, ref string) error {
	egressclass.RequireTransport(egressclass.ExtensionPackGit, egressclass.GitCLI)
	if err := gitargv.ValidateCloneURL(source); err != nil {
		return fmt.Errorf("extensions install: %w", err)
	}
	if err := gitargv.ValidateRefArg(ref); err != nil {
		return fmt.Errorf("extensions install: %w", err)
	}
	// Lease source before destination.
	releaseSource, err := gitlease.CloneSource(ctx, source)
	if err != nil {
		return err
	}
	defer releaseSource()
	releaseDest, err := gitlease.Path(ctx, dest)
	if err != nil {
		return err
	}
	defer releaseDest()
	ctx, cancel := gitNetworkContext(ctx)
	defer cancel()
	parent := filepath.Dir(dest)
	args := []string{"clone", "--depth", "1"}
	if ref != "" && ref != "HEAD" {
		args = append(args, "--branch", ref)
	}
	args = append(args, "--", source, dest)
	if out, code, err := gitexec.Run(ctx, parent, args, gitexec.Opts{
		Profile:   gitexec.ProfileNetwork,
		RemoteURL: source,
		Timeout:   10 * time.Minute,
	}); err != nil {
		return fmt.Errorf("git clone: %w: %s", err, strings.TrimSpace(string(out)))
	} else if code != 0 {
		// Commit refs require a full clone.
		_ = os.RemoveAll(dest)
		out, code, err := gitexec.Run(ctx, parent, []string{"clone", "--", source, dest}, gitexec.Opts{
			Profile:   gitexec.ProfileNetwork,
			RemoteURL: source,
			Timeout:   10 * time.Minute,
		})
		if err != nil {
			return fmt.Errorf("git clone: %w: %s", err, strings.TrimSpace(string(out)))
		}
		if code != 0 {
			return fmt.Errorf("git clone: exit %d: %s", code, strings.TrimSpace(string(out)))
		}
		if ref != "" && ref != "HEAD" {
			out, code, err := gitexec.Run(ctx, dest, []string{"checkout", ref}, gitexec.Opts{
				Profile: gitexec.ProfileNetwork,
				Timeout: 10 * time.Minute,
			})
			if err != nil {
				return fmt.Errorf("git checkout %s: %w: %s", ref, err, strings.TrimSpace(string(out)))
			}
			if code != 0 {
				return fmt.Errorf("git checkout %s: exit %d: %s", ref, code, strings.TrimSpace(string(out)))
			}
		}
	}
	return nil
}

func gitRevParse(ctx context.Context, dir, rev string) (string, error) {
	out, code, err := gitexec.Run(ctx, dir, []string{"rev-parse", rev}, gitexec.Opts{
		Profile: gitexec.ProfileHermetic,
	})
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("git rev-parse: exit %d: %s", code, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0o700)
		}
		if rel == ".git" || strings.HasPrefix(filepath.ToSlash(rel), ".git/") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("copy package tree: %s is not a regular file", rel)
		}
		return copyFile(path, target)
	})
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	mode := os.FileMode(0o600)
	if info.Mode()&0o100 != 0 {
		mode |= 0o100
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	_, err = io.Copy(out, in)
	return err
}

func fileSourcePath(source string) (string, error) {
	parsed, err := url.Parse(source)
	if err != nil {
		return "", fmt.Errorf("file source: %w", err)
	}
	if parsed.Scheme != "file" || (parsed.Host != "" && parsed.Host != "localhost") {
		return "", fmt.Errorf("file source must be a local file URL")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("file source must not include a query or fragment")
	}
	path, err := url.PathUnescape(parsed.EscapedPath())
	if err != nil {
		return "", fmt.Errorf("file source: %w", err)
	}
	if path == "" {
		return "", fmt.Errorf("file source path required")
	}
	if runtime.GOOS == "windows" && len(path) >= 3 && path[0] == '/' && path[2] == ':' && isASCIILetter(path[1]) {
		path = path[1:]
	}
	return filepath.FromSlash(path), nil
}

func isASCIILetter(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}

func boolPtr(v bool) *bool { return &v }
