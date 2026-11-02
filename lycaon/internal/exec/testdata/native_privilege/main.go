//go:build linux && cgo

package main

/*
#include <sys/auxv.h>
static const char *native_execfn(void) {
    return (const char *)getauxval(AT_EXECFN);
}
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/lycaon/lycaon/internal/osprocess"
	"golang.org/x/sys/unix"
)

type nativePrivilegeFacts struct {
	Credentials               osprocess.ProcessSecurity
	Arguments                 []string
	Directory, KernelFilename string
}

func main() {
	if os.Getenv("PW_NATIVE_PRIVILEGE_PAYLOAD") != "1" || len(os.Args) != 4 {
		os.Exit(2)
	}
	security, err := osprocess.ProcessCredentials(os.Getpid())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(3)
	}
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var native [2]unix.CapUserData
	if unix.Capget(&header, &native[0]) != nil || security.Linux == nil || security.Linux.Effective != (uint64(native[0].Effective)|uint64(native[1].Effective)<<32) {
		os.Exit(9)
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(4)
	}
	if os.Getenv("PW_NATIVE_AUXV_NONDUMPABLE") == "1" {
		if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(10)
		}
	}
	filename := C.native_execfn()
	if filename == nil {
		fmt.Fprintln(os.Stderr, "native AT_EXECFN absent")
		os.Exit(5)
	}
	if err := os.WriteFile(os.Args[3], []byte("native privileged effect\n"), 0644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(6)
	}
	if err := os.Chmod(os.Args[3], 0644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(8)
	}
	facts := nativePrivilegeFacts{security, os.Args, cwd, C.GoString(filename)}
	if err := json.NewEncoder(os.Stdout).Encode(facts); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(7)
	}
}
