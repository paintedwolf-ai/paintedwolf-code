package confine

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
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
	var profile string
	var err error
	if !c.HostExecution {
		profile, err = BuildProfile(c)
		if err != nil {
			return nil, func() {}, err
		}
	}
	// An inherited pipe keeps the profile outside writable pathnames.
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, func() {}, err
	}
	helperArgs := []string{helperFlag, profileFDFlag, strconv.Itoa(helperProfileFD)}
	if c.HostExecution {
		helperArgs = append(helperArgs, pinHostExecutionFlag)
	}
	// ExtraFiles[0] lands on helperProfileFD in the child.
	extra := []*os.File{pr}
	if marker := c.lineage.ChildFile(); marker != nil {
		helperArgs = append(helperArgs, lineageFDFlag, strconv.Itoa(helperLineageFD))
		extra = append(extra, marker)
	}
	reports, closeReports := refusalReport(c)
	if reports != nil {
		// A nil entry leaves the marker's descriptor closed when there is none.
		for len(extra) < helperRefusalFD-helperProfileFD {
			extra = append(extra, nil)
		}
		extra = append(extra, reports)
		helperArgs = append(helperArgs, refusalFDFlag, strconv.Itoa(helperRefusalFD))
	}
	helperArgs = append(append(helperArgs, "--", name), args...)
	cmd := exec.CommandContext(ctx, self, helperArgs...)
	cmd.ExtraFiles = extra
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
