package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/sandbox"
)

// CopyTreeExact copies one complete tree without pruning or deleting the source.
// The destination must not exist.
func CopyTreeExact(ctx context.Context, source, destination string) error {
	if _, err := os.Lstat(destination); err == nil {
		return fmt.Errorf("destination already exists: %s", destination)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Mkdir(destination, 0o700); err != nil {
		return err
	}
	sourceRoot, err := os.OpenRoot(source)
	if err != nil {
		_ = os.Remove(destination)
		return err
	}
	defer func() { _ = sourceRoot.Close() }()
	destinationRoot, err := os.OpenRoot(destination)
	if err != nil {
		_ = os.Remove(destination)
		return err
	}
	defer func() { _ = destinationRoot.Close() }()
	if err := copyTreeExact(ctx, source, sourceRoot, destinationRoot); err != nil {
		_ = destinationRoot.Close()
		_ = os.RemoveAll(destination)
		return err
	}
	return nil
}

func copyTreeExact(ctx context.Context, sourcePath string, source, destination *os.Root) error {
	type directoryMode struct {
		path string
		mode os.FileMode
	}
	rootInfo, err := source.Stat(".")
	if err != nil {
		return err
	}
	directories := []directoryMode{{path: ".", mode: rootInfo.Mode().Perm()}}
	err = sandbox.SurveyWalk(ctx, sourcePath, sandbox.SurveyOptions{
		IncludeHidden: true, IncludeEngineOverlay: true, IncludeVCSMetadata: true,
	}, func(entry sandbox.SurveyEntry) (sandbox.SurveyAction, error) {
		rel := filepath.FromSlash(entry.Rel)
		info, err := entry.DirEntry.Info()
		if err != nil {
			return sandbox.SurveyContinue, err
		}
		switch {
		case entry.IsDir:
			if err := destination.Mkdir(rel, 0o700); err != nil {
				return sandbox.SurveyContinue, err
			}
			directories = append(directories, directoryMode{path: rel, mode: info.Mode().Perm()})
			return sandbox.SurveyContinue, nil
		case info.Mode()&os.ModeSymlink != 0:
			link, err := source.Readlink(rel)
			if err != nil {
				return sandbox.SurveyContinue, err
			}
			return sandbox.SurveyContinue, destination.Symlink(link, rel)
		case info.Mode().IsRegular():
			return sandbox.SurveyContinue, copyRegularFileExact(source, destination, rel, info.Mode().Perm())
		default:
			return sandbox.SurveyContinue, fmt.Errorf("unsupported file type: %s", rel)
		}
	})
	if err != nil {
		return err
	}
	// Apply directory modes after their children are written.
	for i := len(directories) - 1; i >= 0; i-- {
		if err := destination.Chmod(directories[i].path, directories[i].mode); err != nil {
			return err
		}
	}
	return nil
}

func copyRegularFileExact(source, destination *os.Root, path string, mode os.FileMode) error {
	input, err := source.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	output, err := destination.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	return destination.Chmod(path, mode)
}

// TreeSHA256 hashes paths, types, permissions, symlink targets, and file bytes.
func TreeSHA256(ctx context.Context, root string) (string, error) {
	rootDir, err := os.OpenRoot(root)
	if err != nil {
		return "", err
	}
	defer func() { _ = rootDir.Close() }()
	h := sha256.New()
	rootInfo, err := rootDir.Stat(".")
	if err != nil {
		return "", err
	}
	writeTreeEntryHeader(h, ".", rootInfo)
	err = sandbox.SurveyWalk(ctx, root, sandbox.SurveyOptions{
		IncludeHidden: true, IncludeEngineOverlay: true, IncludeVCSMetadata: true,
	}, func(entry sandbox.SurveyEntry) (sandbox.SurveyAction, error) {
		rel := filepath.FromSlash(entry.Rel)
		info, err := entry.DirEntry.Info()
		if err != nil {
			return sandbox.SurveyContinue, err
		}
		writeTreeEntryHeader(h, rel, info)
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := rootDir.Readlink(rel)
			if err != nil {
				return sandbox.SurveyContinue, err
			}
			_, _ = io.WriteString(h, target)
		case info.Mode().IsRegular():
			file, err := rootDir.Open(rel)
			if err != nil {
				return sandbox.SurveyContinue, err
			}
			_, copyErr := io.Copy(h, file)
			closeErr := file.Close()
			if copyErr != nil {
				return sandbox.SurveyContinue, copyErr
			}
			if closeErr != nil {
				return sandbox.SurveyContinue, closeErr
			}
		}
		_, _ = h.Write([]byte{0})
		return sandbox.SurveyContinue, nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeTreeEntryHeader(w io.Writer, rel string, info os.FileInfo) {
	_, _ = fmt.Fprintf(w, "%s\x00%s\x00%o\x00", filepath.ToSlash(rel), info.Mode().Type(), info.Mode().Perm())
}
