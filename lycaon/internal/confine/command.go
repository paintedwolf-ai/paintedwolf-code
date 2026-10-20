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
	pins ...*ProcessPins,
) (*exec.Cmd, func(), error) {
	if IsHelperInvocation(os.Args) {
		return nil, func() {}, ErrNestedHelper
	}
	if len(pins) > 1 || (len(pins) == 1 && pins[0] == nil) {
		return nil, func() {}, fmt.Errorf("launch accepts one nonnil pin set")
	}
	if len(pins) == 1 {
		if err := pins[0].limits.checkCommand(name, args, nil); err != nil {
			return nil, func() {}, err
		}
	}
	if c.HostExecution && len(pins) == 0 {
		return exec.CommandContext(ctx, name, args...), func() {}, nil
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
	closePins := func() {}
	if len(pins) == 1 {
		closePins, err = attachPinnedLaunch(ctx, cmd, pins[0], name, args, !c.HostExecution)
		if err != nil {
			_ = pr.Close()
			_ = pw.Close()
			closeReports()
			return nil, func() {}, err
		}
	}
	go func() {
		defer func() { _ = pw.Close() }()
		_, _ = io.WriteString(pw, profile)
	}()
	// Releasing the host's own end lets the marker report end-of-file once the
	// last descendant exits.
	cleanup := func() {
		_ = pr.Close()
		closePins()
		closeReports()
		c.lineage.Started()
	}
	return cmd, cleanup, nil
}
