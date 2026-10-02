#ifndef PW_PRESENCE_LAUNCHER_DARWIN_H
#define PW_PRESENCE_LAUNCHER_DARWIN_H

#include <stddef.h>

// pw_presence_verify_parent checks that this process's parent carries the
// named code identifier and is signed by the same team as this process.
// Returns 0 on success; otherwise writes a reason into reason.
int pw_presence_verify_parent(const char *identifier, char *reason, size_t reason_size);

#endif
