package confine

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strconv"

	"github.com/lycaon/lycaon/internal/fseffect"
)

// Command builds the process for the selected confinement boundary.
func Command(
	ctx context.Context,
	self, name string,
	args []string,
	c Confinement,
) (*exec.Cmd, func(), error) {
	if IsHelperInvocation(os.Args) {
		return nil, func() {}, ErrNestedHelper
	}
	extra := c.Process.arguments()
	if c.HostExecution && c.lineage == nil && len(extra) == 0 {
		return exec.CommandContext(ctx, name, args...), func() {}, nil
	}
	var profile string
	var err error
	if !c.HostExecution {
		c, err = validateProfileInputs(c)
		if err != nil {
			return nil, func() {}, err
		}
		if err := preparePackageCacheDataRoots(c, fseffect.MkdirAll); err != nil {
			return nil, func() {}, err
		}
		profile, err = renderProfile(c)
		if err != nil {
			return nil, func() {}, err
		}
	}
	// An inherited pipe keeps the profile outside writable pathnames.
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, func() {}, err
	}
	helperArgs := append([]string{helperFlag, profileFDFlag, strconv.Itoa(helperProfileFD)}, extra...)
	if c.HostExecution {
		helperArgs = append(helperArgs, pinHostExecutionFlag)
	}
	// ExtraFiles[0] lands on helperProfileFD in the child.
	files := []*os.File{pr}
	if marker := c.lineage.ChildFile(); marker != nil {
		helperArgs = append(helperArgs, lineageFDFlag, strconv.Itoa(helperLineageFD))
		files = append(files, marker)
	}
	reports, closeReports := refusalReport(c)
	if reports != nil {
		// A nil entry leaves the marker's descriptor closed when there is none.
		for len(files) < helperRefusalFD-helperProfileFD {
			files = append(files, nil)
		}
		files = append(files, reports)
		helperArgs = append(helperArgs, refusalFDFlag, strconv.Itoa(helperRefusalFD))
	}
	helperArgs = append(append(helperArgs, "--", name), args...)
	cmd := exec.CommandContext(ctx, self, helperArgs...)
	cmd.ExtraFiles = files
	go func() {
		defer func() { _ = pw.Close() }()
		_, _ = io.WriteString(pw, profile)
	}()
	// Releasing the host's own end lets the marker report end-of-file once the
	// last descendant exits.
	cleanup := func() {
		_ = pr.Close()
		closeReports()
		c.lineage.Started()
	}
	return cmd, cleanup, nil
}
