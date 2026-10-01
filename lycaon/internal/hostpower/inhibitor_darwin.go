//go:build darwin && cgo

package hostpower

/*
#cgo LDFLAGS: -framework IOKit -framework CoreFoundation
#include <CoreFoundation/CoreFoundation.h>
#include <IOKit/pwr_mgt/IOPMLib.h>

static int create_sleep_assertion(IOPMAssertionID *assertion_id) {
	CFStringRef reasonForActivity = CFSTR("Painted Wolf Code active work");
	IOReturn success = IOPMAssertionCreateWithName(
		kIOPMAssertionTypePreventUserIdleSystemSleep,
		kIOPMAssertionLevelOn,
		reasonForActivity,
		assertion_id
	);
	return (int)success;
}

static int release_sleep_assertion(IOPMAssertionID assertion_id) {
	IOReturn success = IOPMAssertionRelease(assertion_id);
	return (int)success;
}
*/
import "C"

import (
	"fmt"
	"sync"
)

type platformInhibitor struct{}

func (platformInhibitor) Supported() bool { return true }

func (platformInhibitor) Acquire() (lease, error) {
	var assertionID C.IOPMAssertionID
	ret := C.create_sleep_assertion(&assertionID)
	if ret != C.kIOReturnSuccess {
		return nil, fmt.Errorf("start idle-sleep assertion: IOReturn %d", int(ret))
	}
	return &iokitLease{
		id:   assertionID,
		done: make(chan error, 1),
	}, nil
}

type iokitLease struct {
	id   C.IOPMAssertionID
	done chan error
	once sync.Once
}

func (l *iokitLease) Done() <-chan error { return l.done }

func (l *iokitLease) Release() error {
	var err error
	l.once.Do(func() {
		ret := C.release_sleep_assertion(l.id)
		if ret != C.kIOReturnSuccess {
			err = fmt.Errorf("release idle-sleep assertion: IOReturn %d", int(ret))
		}
		close(l.done)
	})
	return err
}
