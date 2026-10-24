#include <linux/stat.h>
#include "syscall.h"

#define PW_NEWUSER 0x10000000
#define PW_NEWNS 0x00020000
#define PW_NS_USERNS 0xb701
#define PW_NS_PARENT 0xb702
#define PW_NS_TYPE 0xb703
#define PW_NS_OWNER 0xb704
#define PW_INIT_USER_INO 0xeffffffdULL
#define PW_SEALS 15
#define PW_GET_SEALS 1034
#define PW_AT_EMPTY_PATH 0x1000
#define PW_CLOEXEC 0x80000

/* Fixed descriptors, selected and authenticated by the short-lived stage:
 * 3 U_view, 4 source mount namespace, 5 original entry frame, 6 lifetime,
 * 7 private intake socket, 8 selected Go entry, 9 captured initial userns,
 * 10 sealed transition identity. No argument selects a path or operation. */
struct pw_identity { unsigned int fd, reserved; unsigned long long device, inode; };
struct pw_frame { unsigned char magic[8]; unsigned int version, count; struct pw_identity roles[7]; };
_Static_assert(sizeof(long) == 8 && sizeof(struct pw_frame) == 184, "fixed transition ABI");

static struct statx pw_stat(int fd) {
    struct statx result;
    pw_require(PW_CALL(__NR_statx,fd,"",PW_AT_EMPTY_PATH|0x100,STATX_BASIC_STATS,&result,0) == 0);
    pw_require((result.stx_mask & (STATX_TYPE|STATX_MODE|STATX_UID|STATX_GID|STATX_INO|STATX_SIZE)) == (STATX_TYPE|STATX_MODE|STATX_UID|STATX_GID|STATX_INO|STATX_SIZE));
    return result;
}

static int pw_same(struct statx a, struct statx b) {
    return a.stx_ino == b.stx_ino && a.stx_dev_major == b.stx_dev_major && a.stx_dev_minor == b.stx_dev_minor;
}

static void pw_regular(struct statx stat) {
    pw_require((stat.stx_mode & 0170000) == 0100000 && stat.stx_uid == 0 && (stat.stx_mode & 06022) == 0);
}

static void pw_sealed(int fd) {
    pw_regular(pw_stat(fd));
    pw_require(PW_CALL(__NR_fcntl,fd,PW_GET_SEALS,0,0,0,0) == PW_SEALS);
}

static void pw_signals(void) {
    unsigned long mask = ~0UL;
    unsigned long action[4] = {0,0,0,0};
    pw_require(PW_CALL(__NR_rt_sigprocmask,2,&mask,0,8,0,0) == 0);
    for (int signal = 1; signal <= 64; signal++) {
        if (signal == 9 || signal == 19) continue;
        pw_require(PW_CALL(__NR_rt_sigaction,signal,action,0,8,0,0) == 0);
    }
}

static void pw_authenticate(void) {
    unsigned int ids[3];
    pw_require(PW_CALL(__NR_getresuid,&ids[0],&ids[1],&ids[2],0,0,0) == 0 && ids[0] == 0 && ids[1] == 0 && ids[2] == 0);
    pw_require(PW_CALL(__NR_getresgid,&ids[0],&ids[1],&ids[2],0,0,0) == 0 && ids[0] == 0 && ids[1] == 0 && ids[2] == 0);
    pw_require(PW_CALL(__NR_prctl,4,0,0,0,0,0) == 0);
    pw_sealed(10);
    struct pw_frame frame;
    pw_require(pw_stat(10).stx_size == sizeof(frame));
    pw_require(PW_CALL(__NR_pread64,10,&frame,sizeof(frame),0,0,0) == sizeof(frame));
    const unsigned char magic[8] = {'P','W','M','I','N','0','1',0};
    for (int i = 0; i < 8; i++) pw_require(frame.magic[i] == magic[i]);
    pw_require(frame.version == 1 && frame.count == 7);
    for (int i = 0; i < 7; i++) {
        struct statx actual = pw_stat(i+3);
        struct pw_identity expected = frame.roles[i];
        pw_require(expected.fd == (unsigned int)i+3 && expected.reserved == 0);
        pw_require(expected.device == ((unsigned long long)actual.stx_dev_major<<32 | actual.stx_dev_minor) && expected.inode == actual.stx_ino);
    }
    pw_sealed(5);
    pw_require(pw_stat(5).stx_size > 0 && pw_stat(5).stx_size <= (128U<<10));
    pw_regular(pw_stat(8));
    pw_require((pw_stat(8).stx_mode & 0111) != 0);
    pw_require((pw_stat(6).stx_mode & 0170000) == 0010000);
    pw_require((pw_stat(7).stx_mode & 0170000) == 0140000);
    int socket_type = 0;
    unsigned int socket_size = sizeof(socket_type);
    pw_require(PW_CALL(__NR_getsockopt,7,1,3,&socket_type,&socket_size,0) == 0 && socket_size == sizeof(socket_type) && socket_type == 5);
    struct { int pid; unsigned int uid, gid; } peer;
    socket_size = sizeof(peer);
    pw_require(PW_CALL(__NR_getsockopt,7,1,17,&peer,&socket_size,0) == 0 && socket_size == sizeof(peer) && peer.uid == 0 && peer.pid > 0);
    unsigned int owner = ~0U;
    pw_require(PW_CALL(__NR_ioctl,3,PW_NS_TYPE,0,0,0,0) == PW_NEWUSER);
    pw_require(PW_CALL(__NR_ioctl,3,PW_NS_OWNER,&owner,0,0,0) == 0 && owner == 0);
    pw_require(PW_CALL(__NR_ioctl,9,PW_NS_TYPE,0,0,0,0) == PW_NEWUSER && pw_stat(9).stx_ino == PW_INIT_USER_INO);
    int current = (int)PW_CALL(__NR_openat,-100,"/proc/thread-self/ns/user",PW_CLOEXEC,0,0,0);
    pw_require(current >= 0 && pw_same(pw_stat(current),pw_stat(9)));
    pw_require(PW_CALL(__NR_close,current,0,0,0,0,0) == 0);
    int parent = (int)PW_CALL(__NR_ioctl,3,PW_NS_PARENT,0,0,0,0);
    pw_require(parent >= 0 && pw_same(pw_stat(parent),pw_stat(9)) && !pw_same(pw_stat(3),pw_stat(9)));
    pw_require(PW_CALL(__NR_close,parent,0,0,0,0,0) == 0);
    pw_require(PW_CALL(__NR_ioctl,4,PW_NS_TYPE,0,0,0,0) == PW_NEWNS);
    int source_owner = (int)PW_CALL(__NR_ioctl,4,PW_NS_USERNS,0,0,0,0);
    pw_require(source_owner >= 0 && pw_same(pw_stat(source_owner),pw_stat(9)));
    pw_require(PW_CALL(__NR_close,source_owner,0,0,0,0,0) == 0);
}

static void pw_identity_map(const char *name) {
    int fd = (int)PW_CALL(__NR_openat,-100,name,PW_CLOEXEC,0,0,0);
    pw_require(fd >= 0);
    unsigned char buffer[128];
    long count = PW_CALL(__NR_read,fd,buffer,sizeof(buffer),0,0,0);
    pw_require(count > 0 && count < sizeof(buffer));
    unsigned char extra;
    pw_require(PW_CALL(__NR_read,fd,&extra,1,0,0,0) == 0);
    pw_require(PW_CALL(__NR_close,fd,0,0,0,0,0) == 0);
    unsigned long expected[3] = {0,0,4294967295UL};
    long cursor = 0;
    for (int field = 0; field < 3; field++) {
        while (cursor < count && (buffer[cursor] == ' ' || buffer[cursor] == '\t' || buffer[cursor] == '\n')) cursor++;
        pw_require(cursor < count && buffer[cursor] >= '0' && buffer[cursor] <= '9');
        unsigned long value = 0;
        while (cursor < count && buffer[cursor] >= '0' && buffer[cursor] <= '9') {
            pw_require(value <= 429496729UL);
            value = value*10 + buffer[cursor++] - '0';
        }
        pw_require(value == expected[field]);
        if (field < 2) pw_require(cursor < count && (buffer[cursor] == ' ' || buffer[cursor] == '\t' || buffer[cursor] == '\n'));
    }
    while (cursor < count && (buffer[cursor] == ' ' || buffer[cursor] == '\t' || buffer[cursor] == '\n')) cursor++;
    pw_require(cursor == count);
}

#include "guardian.h"
#include "notice.h"

__attribute__((noreturn)) void pw_mount_transition(const unsigned long *stack) {
    pw_signals();
    pw_require(stack[0] == 1 && stack[1] != 0 && stack[2] == 0 && stack[3] == 0);
    /* A distinct root-sealed high-FD contract selects the same-PID
     * guardian transition. Low-FD intake never inherits these slots. */
    unsigned int ids[3];
    pw_require(PW_CALL(__NR_getresuid,&ids[0],&ids[1],&ids[2],0,0,0) == 0 && ids[0] == 0 && ids[1] == 0 && ids[2] == 0);
    long notice_seals = PW_CALL(__NR_fcntl,20,PW_GET_SEALS,0,0,0,0);
    if (notice_seals != -9) pw_setup_notice();
    long guardian_seals = PW_CALL(__NR_fcntl,85,PW_GET_SEALS,0,0,0,0);
    if (guardian_seals != -9) {
        pw_sealed(85);
        for (int i = 0; i < PW_GUARD_ROLES; i++) {
            int target = i < 7 ? i+3 : i+4;
            pw_require(PW_CALL(__NR_dup3,target+61,target,0,0,0,0) == target);
        }
        pw_require(PW_CALL(__NR_dup3,85,10,0,0,0,0) == 10);
        pw_require(PW_CALL(__NR_close_range,20,~0U,0,0,0,0) == 0);
        pw_guard_transition();
    }
    pw_authenticate();
    pw_require(PW_CALL(__NR_close_range,0,2,0,0,0,0) == 0);
    pw_require(PW_CALL(__NR_close_range,11,~0U,0,0,0,0) == 0);
    pw_require(PW_CALL(__NR_setns,4,PW_NEWNS,0,0,0,0) == 0);
    pw_require(PW_CALL(__NR_setns,3,PW_NEWUSER,0,0,0,0) == 0);
    pw_identity_map("/proc/thread-self/uid_map");
    pw_identity_map("/proc/thread-self/gid_map");
    pw_require(PW_CALL(__NR_unshare,PW_NEWNS,0,0,0,0,0) == 0);
    pw_require(PW_CALL(__NR_mount,"","/","",(1U<<14)|(1U<<19),0,0) == 0);
    for (int source = 5; source <= 8; source++) pw_require(PW_CALL(__NR_dup3,source,source-2,0,0,0,0) == source-2);
    pw_require(PW_CALL(__NR_close_range,7,~0U,0,0,0,0) == 0);
    unsigned long mask = 0;
    pw_require(PW_CALL(__NR_rt_sigprocmask,2,&mask,0,8,0,0) == 0);
    const char *arguments[] = {"pw-launch-entry","--mount-intake",0};
    const char *environment[] = {0};
    PW_CALL(__NR_execveat,6,"",arguments,environment,PW_AT_EMPTY_PATH,0);
    pw_refuse();
}
