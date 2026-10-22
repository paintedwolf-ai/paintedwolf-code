package launchverify

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func openControlledRoot(dir, anchor string, owner uint32) (*os.Root, error) {
	before, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	beforeFacts, err := controlledDir(before, owner)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	after, err := root.Stat(".")
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	afterFacts, err := controlledDir(after, owner)
	if err != nil || !sameInode(beforeFacts, afterFacts) {
		_ = root.Close()
		return nil, errors.New("directory changed while opening")
	}
	return root, nil
}

func readControlledFile(root *os.Root, name string, maxBytes int64, owner uint32) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	before, err := controlledFile(info, owner)
	if err != nil {
		return nil, err
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	after, err := controlledFile(opened, owner)
	if err != nil || !sameInode(before, after) {
		return nil, errors.New("file changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > maxBytes {
		return nil, errors.New("controlled file exceeds size bound")
	}
	return raw, nil
}

func controlledAncestors(path, anchor string, owner uint32) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || !filepath.IsAbs(anchor) || filepath.Clean(anchor) != anchor {
		return errors.New("unclean path")
	}
	rel, err := filepath.Rel(anchor, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return errors.New("path outside trust anchor")
	}
	for cur := path; ; cur = filepath.Dir(cur) {
		info, err := os.Lstat(cur)
		if err != nil {
			return err
		}
		if _, err := controlledDir(info, owner); err != nil {
			return fmt.Errorf("%s: %w", cur, err)
		}
		if cur == anchor {
			return nil
		}
	}
}

func controlledDir(info fs.FileInfo, owner uint32) (fileFacts, error) {
	facts, err := factsOf(info)
	if err != nil {
		return fileFacts{}, err
	}
	if !facts.mode.IsDir() || facts.mode&(fs.ModeSymlink|fs.ModeSticky|fs.ModeSetuid|fs.ModeSetgid) != 0 || facts.mode.Perm()&0o022 != 0 || facts.uid != owner {
		return fileFacts{}, errors.New("uncontrolled directory")
	}
	return facts, nil
}

func controlledFile(info fs.FileInfo, owner uint32) (fileFacts, error) {
	facts, err := factsOf(info)
	if err != nil {
		return fileFacts{}, err
	}
	if !facts.mode.IsRegular() || facts.mode&(fs.ModeSymlink|fs.ModeSticky|fs.ModeSetuid|fs.ModeSetgid) != 0 || facts.mode.Perm()&0o022 != 0 || facts.uid != owner {
		return fileFacts{}, errors.New("uncontrolled file")
	}
	return facts, nil
}

func controlledLockFile(info fs.FileInfo, owner uint32) (fileFacts, error) {
	facts, err := controlledFile(info, owner)
	if err != nil {
		return fileFacts{}, err
	}
	if facts.mode.Perm() != 0o600 {
		return fileFacts{}, errors.New("unsafe generation lock permissions")
	}
	return facts, nil
}
