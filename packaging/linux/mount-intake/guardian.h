/* A second fixed role of the authenticated transition image. This operation
 * preserves PID and protected child lineage while ending initial-userns
 * authority before executing the caller guardian. */
#define PW_GUARD_ROLES 16
struct pw_guard_header {
    unsigned char magic[8]; unsigned int version, count;
    struct pw_identity roles[PW_GUARD_ROLES];
    unsigned int uid, gid, groups, reserved;
};
_Static_assert(sizeof(struct pw_guard_header) == 416, "fixed guardian ABI");

static void pw_guard_untraced(void) {
    int fd = (int)PW_CALL(__NR_openat,-100,"/proc/thread-self/status",PW_CLOEXEC,0,0,0);
    pw_require(fd >= 0);
    unsigned char buffer[4096];
    long size = PW_CALL(__NR_read,fd,buffer,sizeof(buffer),0,0,0);
    pw_require(size > 0);
    pw_require(PW_CALL(__NR_close,fd,0,0,0,0,0) == 0);
    const unsigned char key[] = "TracerPid:";
    int matches = 0;
    for (long start = 0; start < size;) {
        long end = start;
        while (end < size && buffer[end] != '\n') end++;
        if (end == size) break;
        int equal = end-start >= 10;
        for (int i = 0; i < 10 && equal; i++) if (buffer[start+i] != key[i]) equal = 0;
        if (equal) {
            long cursor = start+10;
            while (cursor < end && (buffer[cursor] == ' ' || buffer[cursor] == '\t')) cursor++;
            pw_require(cursor+1 == end && buffer[cursor] == '0');
            matches++;
        }
        start = end+1;
    }
    pw_require(matches == 1);
}

__attribute__((noreturn)) static void pw_guard_transition(void) {
    unsigned int ids[3];
    pw_require(PW_CALL(__NR_getresuid,&ids[0],&ids[1],&ids[2],0,0,0) == 0 && ids[0] == 0 && ids[1] == 0 && ids[2] == 0);
    pw_require(PW_CALL(__NR_getresgid,&ids[0],&ids[1],&ids[2],0,0,0) == 0 && ids[0] == 0 && ids[1] == 0 && ids[2] == 0);
    pw_require(PW_CALL(__NR_prctl,4,0,0,0,0,0) == 0);
    pw_guard_untraced();
    pw_sealed(10);
    struct pw_guard_header frame;
    pw_require(PW_CALL(__NR_pread64,10,&frame,sizeof(frame),0,0,0) == sizeof(frame));
    const unsigned char magic[8] = {'P','W','G','U','A','R','D','1'};
    for (int i = 0; i < 8; i++) pw_require(frame.magic[i] == magic[i]);
    pw_require(frame.version == 1 && frame.count == PW_GUARD_ROLES && frame.reserved == 0);
    pw_require(frame.uid > 0 && frame.uid != ~0U && frame.gid != ~0U && frame.groups <= 65536);
    pw_require(pw_stat(10).stx_size == sizeof(frame) + 4ULL*frame.groups);
    for (int i = 0; i < PW_GUARD_ROLES; i++) {
        int fd = i < 7 ? i+3 : i+4;
        struct statx actual = pw_stat(fd);
        struct pw_identity expected = frame.roles[i];
        pw_require(expected.fd == (unsigned int)fd && expected.reserved == 0);
        pw_require(expected.device == ((unsigned long long)actual.stx_dev_major<<32 | actual.stx_dev_minor) && expected.inode == actual.stx_ino);
    }
    pw_sealed(5);
    pw_sealed(11);
    pw_regular(pw_stat(8));
    pw_require((pw_stat(8).stx_mode & 0111) != 0);
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
    unsigned int groups[65536];
    if (frame.groups) pw_require(PW_CALL(__NR_pread64,10,groups,4ULL*frame.groups,sizeof(frame),0,0) == 4L*frame.groups);
    for (unsigned int i = 0; i < frame.groups; i++) pw_require(groups[i] != ~0U);
    pw_require(PW_CALL(__NR_close_range,0,2,0,0,0,0) == 0);
    pw_require(PW_CALL(__NR_close_range,20,~0U,0,0,0,0) == 0);
    pw_require(PW_CALL(__NR_setns,4,PW_NEWNS,0,0,0,0) == 0);
    pw_require(PW_CALL(__NR_setns,3,PW_NEWUSER,0,0,0,0) == 0);
    pw_identity_map("/proc/thread-self/uid_map");
    pw_identity_map("/proc/thread-self/gid_map");
    /* Drop the complete bounding set before losing SETPCAP. */
    for (int cap = 0; cap < 64; cap++) {
        long value = PW_CALL(__NR_prctl,23,cap,0,0,0,0);
        if (value == -22) break;
        pw_require(value == 0 || value == 1);
        pw_require(PW_CALL(__NR_prctl,24,cap,0,0,0,0) == 0);
    }
    pw_require(PW_CALL(__NR_prctl,23,64,0,0,0,0) == -22);
    pw_require(PW_CALL(__NR_prctl,47,4,0,0,0,0) == 0);
    pw_require(PW_CALL(__NR_prctl,8,0,0,0,0,0) == 0);
    pw_require(PW_CALL(__NR_setgroups,frame.groups,groups,0,0,0,0) == 0);
    pw_require(PW_CALL(__NR_setresgid,frame.gid,frame.gid,frame.gid,0,0,0) == 0);
    pw_require(PW_CALL(__NR_setresuid,frame.uid,frame.uid,frame.uid,0,0,0) == 0);
    struct { unsigned int version; int pid; } header = {0x20080522,0};
    unsigned int capabilities[6] = {0,0,0,0,0,0};
    pw_require(PW_CALL(__NR_capset,&header,capabilities,0,0,0,0) == 0);
    pw_require(PW_CALL(__NR_prctl,38,1,0,0,0,0) == 0);
    pw_require(PW_CALL(__NR_prctl,4,0,0,0,0,0) == 0);
    pw_require(PW_CALL(__NR_getresuid,&ids[0],&ids[1],&ids[2],0,0,0) == 0 && ids[0] == frame.uid && ids[1] == frame.uid && ids[2] == frame.uid);
    pw_require(PW_CALL(__NR_getresgid,&ids[0],&ids[1],&ids[2],0,0,0) == 0 && ids[0] == frame.gid && ids[1] == frame.gid && ids[2] == frame.gid);
    pw_require(PW_CALL(__NR_prctl,39,0,0,0,0,0) == 1 && PW_CALL(__NR_prctl,3,0,0,0,0,0) == 0);
    pw_require(PW_CALL(__NR_close,3,0,0,0,0,0) == 0);
    pw_require(PW_CALL(__NR_close,4,0,0,0,0,0) == 0);
    pw_require(PW_CALL(__NR_close,9,0,0,0,0,0) == 0);
    /* The readonly proof remains for exact post-exec namespace attestation. */
    pw_require(PW_CALL(__NR_fcntl,8,2,1,0,0,0) == 0);
    unsigned long mask = 0;
    pw_require(PW_CALL(__NR_rt_sigprocmask,2,&mask,0,8,0,0) == 0);
    const char *arguments[] = {"pw-launch-entry","--protected-guardian",0};
    const char *environment[] = {0};
    PW_CALL(__NR_execveat,8,"",arguments,environment,PW_AT_EMPTY_PATH,0);
    pw_refuse();
}
