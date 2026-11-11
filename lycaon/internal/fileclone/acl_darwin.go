//go:build darwin

package fileclone

/*
#include <sys/types.h>
#include <sys/acl.h>
#include <errno.h>
#include <stdlib.h>

static int lycaon_copy_acl(const char *src, const char *dst) {
	acl_t acl = acl_get_link_np(src, ACL_TYPE_EXTENDED);
	if (acl == NULL) {
		// No ACL, or a volume that cannot hold one.
		return (errno == ENOENT || errno == ENOTSUP || errno == ENOTTY) ? 0 : -1;
	}
	acl_entry_t entry;
	if (acl_get_entry(acl, ACL_FIRST_ENTRY, &entry) != 0) {
		acl_free(acl);
		return 0;
	}
	int rc = acl_set_link_np(dst, ACL_TYPE_EXTENDED, acl);
	int saved = errno;
	acl_free(acl);
	errno = saved;
	return rc;
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// CopyACL gives dst the extended ACL src has, if any, without following a symlink at either path.
// A clone and a copy both leave ACLs behind.
func CopyACL(src, dst string) error {
	cs, cd := C.CString(src), C.CString(dst)
	defer C.free(unsafe.Pointer(cs))
	defer C.free(unsafe.Pointer(cd))
	if rc, err := C.lycaon_copy_acl(cs, cd); rc != 0 {
		return fmt.Errorf("fileclone: copy ACL from %s: %w", src, err)
	}
	return nil
}
