//go:build darwin

package confine

/*
#cgo LDFLAGS: -framework Security -framework CoreFoundation -lbsm

#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <sys/types.h>
#include <sys/socket.h>
#include <sys/un.h>
#include <sys/proc_info.h>
#include <libproc.h>
#include <bsm/libbsm.h>
#include <CoreFoundation/CoreFoundation.h>
#include <Security/Security.h>

// Direct declarations keep compiler attributes out of the binding.
extern int  sandbox_init(const char *profile, uint64_t flags, char **errorbuf);
extern void sandbox_free_error(char *errorbuf);

// A null operation reports current sandbox membership.
extern int sandbox_check(pid_t pid, const char *operation, int type, ...);

static int confine_is_sandboxed(void) {
    return sandbox_check(getpid(), 0, 0);
}

#ifndef LOCAL_PEERTOKEN
#define LOCAL_PEERTOKEN 0x006
#endif

typedef struct {
    uid_t    uid;
    pid_t    pid;
    int      pidversion;
    uint64_t so_pcb;
    uint64_t peer_pcb;
    char     signing_id[256];
    char     team_id[64];
    char     cdhash[64];
    char     bound_path[256];
} confine_socket_peer_t;

static void confine_cfstring_copy(CFStringRef s, char *dst, size_t cap) {
    dst[0] = '\0';
    if (s == NULL || cap == 0) {
        return;
    }
    if (!CFStringGetCString(s, dst, (CFIndex)cap, kCFStringEncodingUTF8)) {
        dst[0] = '\0';
    }
}

static void confine_cfdata_hex(CFDataRef d, char *dst, size_t cap) {
    dst[0] = '\0';
    if (d == NULL || cap < 3) {
        return;
    }
    const UInt8 *bytes = CFDataGetBytePtr(d);
    CFIndex len = CFDataGetLength(d);
    if (bytes == NULL || len <= 0) {
        return;
    }
    static const char hex[] = "0123456789abcdef";
    size_t out_i = 0;
    for (CFIndex i = 0; i < len && out_i + 2 < cap; i++) {
        dst[out_i++] = hex[(bytes[i] >> 4) & 0xF];
        dst[out_i++] = hex[bytes[i] & 0xF];
    }
    dst[out_i] = '\0';
}

static void confine_fill_signing_from_code(SecCodeRef guest, confine_socket_peer_t *out) {
    if (guest == NULL) {
        return;
    }
    SecStaticCodeRef staticCode = NULL;
    if (SecCodeCopyStaticCode(guest, kSecCSDefaultFlags, &staticCode) == errSecSuccess && staticCode != NULL) {
        CFDictionaryRef info = NULL;
        if (SecCodeCopySigningInformation(staticCode, kSecCSSigningInformation, &info) == errSecSuccess && info != NULL) {
            CFStringRef ident = (CFStringRef)CFDictionaryGetValue(info, kSecCodeInfoIdentifier);
            if (ident != NULL && CFGetTypeID(ident) == CFStringGetTypeID()) {
                confine_cfstring_copy(ident, out->signing_id, sizeof(out->signing_id));
            }
            CFStringRef team = (CFStringRef)CFDictionaryGetValue(info, kSecCodeInfoTeamIdentifier);
            if (team != NULL && CFGetTypeID(team) == CFStringGetTypeID()) {
                confine_cfstring_copy(team, out->team_id, sizeof(out->team_id));
            }
            CFDataRef unique = (CFDataRef)CFDictionaryGetValue(info, kSecCodeInfoUnique);
            if (unique != NULL && CFGetTypeID(unique) == CFDataGetTypeID()) {
                confine_cfdata_hex(unique, out->cdhash, sizeof(out->cdhash));
            }
            CFRelease(info);
        }
        CFRelease(staticCode);
    }
}

static void confine_pid_signing_info(pid_t pid, confine_socket_peer_t *out) {
    SInt32 p = (SInt32)pid;
    CFNumberRef pidNum = CFNumberCreate(kCFAllocatorDefault, kCFNumberSInt32Type, &p);
    if (pidNum == NULL) {
        return;
    }
    const void *keys[1] = { kSecGuestAttributePid };
    const void *vals[1] = { pidNum };
    CFDictionaryRef attrs = CFDictionaryCreate(
        kCFAllocatorDefault, keys, vals, 1,
        &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
    if (attrs != NULL) {
        SecCodeRef guest = NULL;
        if (SecCodeCopyGuestWithAttributes(NULL, attrs, kSecCSDefaultFlags, &guest) == errSecSuccess && guest != NULL) {
            confine_fill_signing_from_code(guest, out);
            CFRelease(guest);
        }
        CFRelease(attrs);
    }
    CFRelease(pidNum);
}

static int confine_connected_socket_peer(int fd, confine_socket_peer_t *out) {
    memset(out, 0, sizeof(*out));
    audit_token_t token;
    socklen_t tlen = sizeof(token);
    if (getsockopt(fd, SOL_LOCAL, LOCAL_PEERTOKEN, &token, &tlen) != 0 || tlen != sizeof(token)) {
        return -1;
    }
    out->uid        = audit_token_to_euid(token);
    out->pid        = audit_token_to_pid(token);
    out->pidversion = audit_token_to_pidversion(token);

    CFDataRef tokenData = CFDataCreate(kCFAllocatorDefault, (const UInt8 *)&token, sizeof(token));
    if (tokenData != NULL) {
        const void *keys[1] = { kSecGuestAttributeAudit };
        const void *vals[1] = { tokenData };
        CFDictionaryRef attrs = CFDictionaryCreate(
            kCFAllocatorDefault, keys, vals, 1,
            &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
        if (attrs != NULL) {
            SecCodeRef guest = NULL;
            if (SecCodeCopyGuestWithAttributes(NULL, attrs, kSecCSDefaultFlags, &guest) == errSecSuccess && guest != NULL) {
                confine_fill_signing_from_code(guest, out);
                CFRelease(guest);
            }
            CFRelease(attrs);
        }
        CFRelease(tokenData);
    }
    if (out->signing_id[0] == '\0' && out->cdhash[0] == '\0' && out->pid > 0) {
        confine_pid_signing_info(out->pid, out);
    }

    struct socket_fdinfo si;
    memset(&si, 0, sizeof(si));
    int n = proc_pidfdinfo(getpid(), fd, PROC_PIDFDSOCKETINFO, &si, sizeof(si));
    if (n == sizeof(si) && si.psi.soi_family == AF_UNIX) {
        out->so_pcb   = (uint64_t)si.psi.soi_pcb;
        out->peer_pcb = (uint64_t)si.psi.soi_proto.pri_un.unsi_conn_pcb;
        const char *caddr = si.psi.soi_proto.pri_un.unsi_caddr.ua_sun.sun_path;
        if (caddr[0] != '\0') {
            strncpy(out->bound_path, caddr, sizeof(out->bound_path) - 1);
        }
    }
    return 0;
}

static int confine_pid_has_socket(
    pid_t pid,
    const char *path1,
    const char *path2,
    uint64_t want_pcb,
    uint64_t skip_pcb,
    confine_socket_peer_t *out
) {
    int bufsize = proc_pidinfo(pid, PROC_PIDLISTFDS, 0, NULL, 0);
    if (bufsize <= 0) {
        return 0;
    }
    struct proc_fdinfo *fds = (struct proc_fdinfo *)malloc((size_t)bufsize);
    if (fds == NULL) {
        return 0;
    }
    int used = proc_pidinfo(pid, PROC_PIDLISTFDS, 0, fds, bufsize);
    if (used <= 0) {
        free(fds);
        return 0;
    }
    int count = used / (int)sizeof(struct proc_fdinfo);
    int found = 0;
    for (int i = 0; i < count; i++) {
        if (fds[i].proc_fdtype != PROX_FDTYPE_SOCKET) {
            continue;
        }
        struct socket_fdinfo si;
        memset(&si, 0, sizeof(si));
        int n = proc_pidfdinfo(pid, fds[i].proc_fd, PROC_PIDFDSOCKETINFO, &si, sizeof(si));
        if (n != (int)sizeof(si) || si.psi.soi_family != AF_UNIX) {
            continue;
        }
        uint64_t pcb = (uint64_t)si.psi.soi_pcb;
        if (skip_pcb != 0 && pcb == skip_pcb) {
            continue;
        }
        const char *sun = si.psi.soi_proto.pri_un.unsi_addr.ua_sun.sun_path;
        int match = 0;
        if (want_pcb != 0 && pcb == want_pcb) {
            match = 1;
        } else if (want_pcb == 0 && si.psi.soi_qlimit > 0 && sun[0] != '\0') {
            if ((path1 != NULL && path1[0] != '\0' && strcmp(sun, path1) == 0) ||
                (path2 != NULL && path2[0] != '\0' && strcmp(sun, path2) == 0)) {
                match = 1;
            }
        }
        if (match) {
            memset(out, 0, sizeof(*out));
            struct proc_bsdshortinfo bsd;
            if (proc_pidinfo(pid, PROC_PIDT_SHORTBSDINFO, 0, &bsd, sizeof(bsd)) == (int)sizeof(bsd)) {
                out->uid = bsd.pbsi_uid;
            } else {
                out->uid = geteuid();
            }
            out->pid    = pid;
            out->so_pcb = pcb;
            strncpy(out->bound_path, sun, sizeof(out->bound_path) - 1);
            confine_pid_signing_info(pid, out);
            found = 1;
            break;
        }
    }
    free(fds);
    return found;
}

static int confine_list_pids(pid_t *buf, int cap) {
    return proc_listallpids(buf, cap * (int)sizeof(pid_t));
}
*/
import "C"

import (
	"fmt"
	"net"
	"os"
	"strings"
	"syscall"
	"unsafe"

	"github.com/lycaon/lycaon/internal/fspath"
)

// Available reports whether per-command confinement is enforced.
func Available() bool { return true }

// The profile applies to the current process.
func applySeatbelt(profile string) error {
	cProfile := C.CString(profile)
	defer C.free(unsafe.Pointer(cProfile))

	var errBuf *C.char
	//nolint:gocritic // dupSubExpr matches the cgo call expansion, not this source
	if rc := C.sandbox_init(cProfile, 0, &errBuf); rc != 0 {
		msg := "sandbox_init failed"
		if errBuf != nil {
			msg = C.GoString(errBuf)
			C.sandbox_free_error(errBuf)
		}
		return fmt.Errorf("seatbelt: %s", msg)
	}
	return nil
}

// processSandboxed reads current kernel membership.
func processSandboxed() (member bool, ok bool) {
	rc := C.confine_is_sandboxed()
	if rc < 0 {
		return false, false
	}
	return rc > 0, true
}

// attestConfined requires positive kernel membership.
func attestConfined() error {
	member, ok := processSandboxed()
	if !ok || !member {
		return fmt.Errorf("seatbelt: could not confirm this process is sandboxed " +
			"(sandbox_check) — refusing to run unconfined")
	}
	return nil
}

func socketHardLinked(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && st.Nlink > 1
}

func socketDevIno(fi os.FileInfo) (uint64, uint64) {
	st, _ := fi.Sys().(*syscall.Stat_t)
	return uint64(st.Dev), st.Ino
}

func peerToIdentity(p C.confine_socket_peer_t, dev, ino uint64) SocketListenerIdentity {
	return SocketListenerIdentity{
		UID:         uint32(p.uid),
		PID:         int32(p.pid),
		PIDVersion:  uint32(p.pidversion),
		SigningID:   C.GoString(&p.signing_id[0]),
		TeamID:      C.GoString(&p.team_id[0]),
		CDHash:      C.GoString(&p.cdhash[0]),
		Dev:         dev,
		Ino:         ino,
		BoundPath:   C.GoString(&p.bound_path[0]),
		ListenerPCB: uint64(p.so_pcb),
	}
}

func findSocketListenerByProc(approvedPath, resolvedPath string, dev, ino uint64) (SocketListenerIdentity, bool) {
	cPath1 := C.CString(approvedPath)
	defer C.free(unsafe.Pointer(cPath1))
	cPath2 := C.CString(resolvedPath)
	defer C.free(unsafe.Pointer(cPath2))

	var peer C.confine_socket_peer_t
	selfPID := C.pid_t(os.Getpid())
	if C.confine_pid_has_socket(selfPID, cPath1, cPath2, 0, 0, &peer) != 0 {
		return peerToIdentity(peer, dev, ino), true
	}

	const maxPIDs = 4096
	pids := make([]C.pid_t, maxPIDs)
	n := min(int(C.confine_list_pids(&pids[0], C.int(maxPIDs))), maxPIDs)
	for i := 0; i < n; i++ {
		if pid := pids[i]; pid > 0 && pid != selfPID && C.confine_pid_has_socket(pid, cPath1, cPath2, 0, 0, &peer) != 0 {
			return peerToIdentity(peer, dev, ino), true
		}
	}
	return SocketListenerIdentity{}, false
}

func inspectSocketListener(approvedPath, resolvedPath string, fi os.FileInfo) (SocketListenerIdentity, error) {
	dev, ino := socketDevIno(fi)
	if id, ok := findSocketListenerByProc(approvedPath, resolvedPath, dev, ino); ok {
		return id, nil
	}
	return SocketListenerIdentity{Dev: dev, Ino: ino, BoundPath: resolvedPath}, nil
}

func listenerIdentityMatches(pinned, current SocketListenerIdentity) bool {
	if pinned.IsZero() {
		return true
	}
	if current.UID != pinned.UID || pinned.SigningID != current.SigningID || pinned.TeamID != current.TeamID {
		return false
	}
	if pinned.TeamID == "" && pinned.CDHash != "" && current.CDHash != "" && pinned.CDHash != current.CDHash {
		return false
	}
	return true
}

func boundPathMatches(g SocketGrant, pinned SocketListenerIdentity, boundPath string) bool {
	return boundPath != "" && (boundPath == g.ApprovedPath || boundPath == g.ResolvedPath || (pinned.BoundPath != "" && boundPath == pinned.BoundPath))
}

func revalidateSocketListener(approvedPath, resolvedPath string, fi os.FileInfo, pinned SocketListenerIdentity) (SocketListenerIdentity, error) {
	if pinned.IsZero() {
		return inspectSocketListener(approvedPath, resolvedPath, fi)
	}
	dev, ino := socketDevIno(fi)
	current, ok := findSocketListenerByProc(approvedPath, resolvedPath, dev, ino)
	if !ok || !listenerIdentityMatches(pinned, current) {
		return SocketListenerIdentity{}, ErrSocketListenerChanged
	}
	return current, nil
}

func verifyConnectedSocketPeer(conn *net.UnixConn, fi os.FileInfo, g SocketGrant) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var peer C.confine_socket_peer_t
	var rc C.int = -1
	_ = raw.Control(func(fd uintptr) {
		rc = C.confine_connected_socket_peer(C.int(fd), &peer)
	})
	dev, ino := socketDevIno(fi)
	current := peerToIdentity(peer, dev, ino)
	pinned := effectiveSocketListener(g)
	if rc != 0 || !boundPathMatches(g, pinned, current.BoundPath) || !listenerIdentityMatches(pinned, current) {
		return ErrSocketListenerChanged
	}
	storePinnedSocketListener(g.ApprovedPath, g.ResolvedPath, current)
	return nil
}

func preDialSocketCheck(g SocketGrant) error {
	if fspath.CanonicalPath(g.ApprovedPath) != g.ResolvedPath {
		return ErrSocketListenerChanged
	}
	if g.ApprovedPath == g.ResolvedPath {
		if afi, err := os.Lstat(g.ApprovedPath); err != nil || afi.Mode()&os.ModeSymlink != 0 {
			return ErrSocketListenerChanged
		}
	}
	fi, err := os.Lstat(g.ResolvedPath)
	if err != nil || fi.Mode()&os.ModeSocket == 0 || socketHardLinked(fi) {
		return ErrSocketListenerChanged
	}
	return nil
}

func applySocketRelaysToExec(profile string, target, env []string) ([]string, []string) {
	var pairs [][3]string
	for _, line := range strings.Split(profile, "\n") {
		if rest, ok := strings.CutPrefix(line, socketRelayHeaderPrefix); ok {
			if p := strings.SplitN(rest, "\t", 3); len(p) == 3 && p[2] != "" {
				pairs = append(pairs, [3]string{p[0], p[1], p[2]})
			}
		}
	}
	outTarget := append([]string(nil), target...)
	for i := 1; i < len(outTarget); i++ {
		for _, m := range pairs {
			if outTarget[i] == m[0] || outTarget[i] == m[1] || outTarget[i] == strings.TrimPrefix(m[1], "/private") {
				outTarget[i] = m[2]
				break
			}
		}
	}
	return outTarget, env
}
