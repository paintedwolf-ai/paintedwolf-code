package search

import (
	"errors"

	"github.com/lycaon/lycaon/internal/commandprogram"
)

var errQuietMatch = errors.New("quiet match")

// searchOperands visits each operand: "-" (or none, without recursion) is
// standard input, a directory is walked when recursion is on.
func (r *grepRun) searchOperands() (finalErr error) {
	inv := r.inv
	r.walker = newWalker(inv)
	defer r.walker.close()
	operands := r.operands
	implicit := false
	if len(operands) == 0 {
		// rg searches an attached stdin, as it does a pipe; with no producer it
		// walks the current directory. grep reads stdin unless recursive.
		piped := r.rg && r.mode != modeFileList && !commandprogram.IsNullInput(inv.input)
		if !r.recursive || piped {
			return r.searchStdin()
		}
		operands, implicit = commandprogram.Fields{{Value: "."}}, true
	}
	multiple := len(operands) > 1
	if r.mode != modeFileList && (multiple || r.recursive) {
		var err error
		r.files, err = newGrepFiles(r, grepWorkerCount())
		if err != nil {
			return err
		}
		defer func() { finalErr = r.files.finish(finalErr) }()
	}
	for _, operand := range operands {
		if r.stopped {
			return nil
		}
		if operand.Value == "-" {
			if err := r.flushFiles(); err != nil {
				return err
			}
			if err := r.searchStdin(); err != nil {
				return err
			}
			continue
		}
		err := r.searchOperand(operand, implicit, multiple)
		if errors.Is(err, errQuietMatch) {
			r.stopped = true
			return nil
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *grepRun) searchStdin() error {
	if r.mode == modeFileList {
		return nil
	}
	label := "(standard input)"
	if r.json != nil {
		label = "<stdin>"
	}
	if named, ok := r.inv.value("--label"); ok {
		label = named
	}
	var err error
	if r.tabs {
		err = r.searchAlignedStdin()
	} else {
		err = r.searchStream(label, r.inv.input, r.inv.owners.streamBytes(), 0, -1, nil, r.forcedNames(false))
	}
	if errors.Is(err, errQuietMatch) {
		r.stopped = true
		return nil
	}
	return err
}

func (r *grepRun) forcedNames(byDefault bool) bool {
	if r.names != nil {
		return *r.names
	}
	return byDefault
}

func (r *grepRun) searchOperand(field commandprogram.Field, implicit, multiple bool) error {
	inv := r.inv
	operand := field.Value
	if reader, ok := field.ProcessSubstitution().OpenReader(); ok {
		if err := r.flushFiles(); err != nil {
			_ = reader.Close()
			return err
		}
		if r.mode == modeFileList {
			_ = reader.Close()
			return inv.out.text(operand, "\n")
		}
		data, err := inv.readAll(reader)
		_ = reader.Close()
		if err != nil {
			return err
		}
		return r.searchContent(operand, data, nil, r.forcedNames(multiple))
	}
	e, err := r.walker.operand(operand, true)
	if err != nil {
		if reason, ok := ordinary(err); ok {
			if err := r.flushFiles(); err != nil {
				return err
			}
			inv.fail(r.quietErrors, "%s: %s", operand, reason)
			return nil
		}
		return err
	}

	if !e.dir() {
		if !r.selection.commandLineFile(operand) {
			return nil
		}
		return r.searchFile(e, r.forcedNames(multiple), false)
	}
	if !r.recursive {
		if r.skipDirectories {
			return nil
		}
		if err := r.flushFiles(); err != nil {
			return err
		}
		inv.fail(r.quietErrors, "%s: Is a directory", operand)
		return nil
	}
	if !r.selection.commandLineDir(operand) {
		return nil
	}
	if implicit {
		e.display = ""
	}
	return r.walk(e, map[[2]uint64]bool{})
}

// walk visits one directory's children depth first in byte order.
func (r *grepRun) walk(dir entry, ancestors map[[2]uint64]bool) error {
	inv := r.inv
	if id, ok := identity(dir.stat); ok {
		if ancestors[id] {
			if err := r.flushFiles(); err != nil {
				return err
			}
			inv.warn(r.quietErrors, "%s: warning: recursive directory loop", displayOr(dir.display))
			return nil
		}
		ancestors[id] = true
		defer delete(ancestors, id)
	}
	children, release, err := r.walker.children(dir, r.follow)
	defer release()
	if err != nil {
		if reason, ok := ordinary(err); ok {
			if err := r.flushFiles(); err != nil {
				return err
			}
			inv.fail(r.quietErrors, "%s: %s", displayOr(dir.display), reason)
			return nil
		}
		return err
	}
	for _, child := range children {
		if r.stopped {
			return nil
		}
		if _, err := r.inv.out.w.Write(nil); err != nil {
			return err
		}
		if child.link && !r.follow {
			continue
		}
		admitted, err := r.selection.admit(child)
		if err != nil {
			return err
		}
		if !admitted {
			continue
		}
		switch {
		case child.dir():
			if err := r.walk(child, ancestors); err != nil {
				return err
			}
		case child.file():
			if err := r.searchFile(child, r.forcedNames(true), true); err != nil {
				return err
			}
		}
	}
	return nil
}

func displayOr(display string) string {
	if display == "" {
		return "."
	}
	return display
}

// searchFile pins and searches one regular file, applying recursive binary policy.
func (r *grepRun) searchFile(e entry, names, walked bool) error {
	if r.files != nil {
		return r.files.submit(e, names, walked)
	}
	return r.searchFileLive(e, names, walked)
}

func (r *grepRun) searchFileLive(e entry, names, walked bool) error {
	if r.mode == modeFileList {
		return r.inv.out.text(e.display, "\n")
	}
	checkBinary := r.rg && walked && r.selection.unrestricted < 3 && r.binaryMode != "text"
	if r.index.canPrune(e) {
		return r.searchNoMatch(e.display, names)
	}
	streamed, pin, err := r.inv.openWalkedPinnedStream(e, r.walker)
	if err != nil {
		if reason, ok := ordinary(err); ok {
			r.inv.fail(r.quietErrors, "%s: %s", e.display, reason)
			return nil
		}
		return err
	}
	r.index.stats.captured++
	if streamed.ContentLearned {
		r.index.stats.learned++
	}
	defer func() { _ = streamed.Reader.Close() }()
	if checkBinary {
		if streamed.FirstNullOffset >= 0 {
			return nil
		}
	}
	return r.searchStream(e.display, streamed.Reader, 0, int(streamed.Revision.ContentBytes), streamed.FirstNullOffset, pin, names)
}
