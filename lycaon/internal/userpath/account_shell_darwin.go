//go:build darwin && cgo

package userpath

/*
#include <pwd.h>
#include <unistd.h>
#include <stdlib.h>
#include <string.h>

static char *get_user_shell(uid_t uid) {
	struct passwd pwd;
	struct passwd *result = NULL;
	char buf[1024];
	int err = getpwuid_r(uid, &pwd, buf, sizeof(buf), &result);
	if (err != 0 || result == NULL || pwd.pw_shell == NULL) {
		return NULL;
	}
	return strdup(pwd.pw_shell);
}
*/
import "C"

import (
	"context"
	"errors"
	"os"
	"unsafe"
)

// defaultAccountShell reads the uid's account shell natively via getpwuid_r.
func defaultAccountShell(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	cShell := C.get_user_shell(C.uid_t(os.Getuid()))
	if cShell == nil {
		return "", errors.New("directory services returned no login shell")
	}
	defer C.free(unsafe.Pointer(cShell))
	shell := C.GoString(cShell)
	if shell == "" {
		return "", errors.New("directory services returned empty login shell")
	}
	return shell, nil
}
