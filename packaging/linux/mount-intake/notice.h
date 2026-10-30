/* One fixed diagnostic after the caller identity has replaced every root
 * credential. No protected descriptor or caller-supplied text survives. */
struct pw_notice_header {
    unsigned char magic[8];
    unsigned int version, uid, gid, groups;
};
_Static_assert(sizeof(struct pw_notice_header) == 24, "fixed notice ABI");

__attribute__((noreturn)) static void pw_setup_notice(void) {
    pw_sealed(20);
    struct pw_notice_header frame;
    pw_require(PW_CALL(__NR_pread64,20,&frame,sizeof(frame),0,0,0) == sizeof(frame));
    const unsigned char magic[8] = {'P','W','N','O','T','I','C','1'};
    for (int i = 0; i < 8; i++) pw_require(frame.magic[i] == magic[i]);
    pw_require(frame.version == 1 && frame.uid > 0 && frame.uid != ~0U && frame.gid != ~0U && frame.groups <= 65536);
    pw_require(pw_stat(20).stx_size == sizeof(frame) + 4ULL*frame.groups);
    int origin = (int)PW_CALL(__NR_openat,-100,"/proc/thread-self/ns/user",PW_CLOEXEC,0,0,0);
    pw_require(origin >= 0 && pw_stat(origin).stx_ino == PW_INIT_USER_INO);
    pw_require(PW_CALL(__NR_close,origin,0,0,0,0,0) == 0);
    unsigned int ids[3];
    pw_require(PW_CALL(__NR_getresgid,&ids[0],&ids[1],&ids[2],0,0,0) == 0 && ids[0] == 0 && ids[1] == 0 && ids[2] == 0);
    unsigned int groups[65536];
    if (frame.groups) pw_require(PW_CALL(__NR_pread64,20,groups,4ULL*frame.groups,sizeof(frame),0,0) == 4L*frame.groups);
    for (unsigned int i = 0; i < frame.groups; i++) pw_require(groups[i] != ~0U);
    pw_require(PW_CALL(__NR_close_range,3,~0U,0,0,0,0) == 0);
    pw_require(PW_CALL(__NR_close_range,0,1,0,0,0,0) == 0);
    for (int cap = 0; cap < 64; cap++) {
        long value = PW_CALL(__NR_prctl,23,cap,0,0,0,0);
        if (value == -22) break;
        pw_require(value == 0 || value == 1);
        pw_require(PW_CALL(__NR_prctl,24,cap,0,0,0,0) == 0);
    }
    pw_require(PW_CALL(__NR_prctl,23,64,0,0,0,0) == -22);
    pw_require(PW_CALL(__NR_prctl,47,4,0,0,0,0) == 0);
    pw_require(PW_CALL(__NR_prctl,8,0,0,0,0,0) == 0);
    pw_require(PW_CALL(__NR_prctl,38,1,0,0,0,0) == 0);
    pw_require(PW_CALL(__NR_setgroups,frame.groups,groups,0,0,0,0) == 0);
    pw_require(PW_CALL(__NR_setresgid,frame.gid,frame.gid,frame.gid,0,0,0) == 0);
    pw_require(PW_CALL(__NR_setresuid,frame.uid,frame.uid,frame.uid,0,0,0) == 0);
    struct { unsigned int version; int pid; } header = {0x20080522,0};
    unsigned int capabilities[6] = {0,0,0,0,0,0};
    pw_require(PW_CALL(__NR_capset,&header,capabilities,0,0,0,0) == 0);
    pw_require(PW_CALL(__NR_prctl,4,0,0,0,0,0) == 0);
    pw_require(PW_CALL(__NR_getresuid,&ids[0],&ids[1],&ids[2],0,0,0) == 0 && ids[0] == frame.uid && ids[1] == frame.uid && ids[2] == frame.uid);
    pw_require(PW_CALL(__NR_getresgid,&ids[0],&ids[1],&ids[2],0,0,0) == 0 && ids[0] == frame.gid && ids[1] == frame.gid && ids[2] == frame.gid);
    const char message[] = "Painted Wolf Code: protected setup stopped. Waiting for its processes to exit.\n";
    PW_CALL(__NR_write,2,message,sizeof(message)-1,0,0,0);
    PW_CALL(__NR_exit_group,1,0,0,0,0,0);
    pw_refuse();
}
