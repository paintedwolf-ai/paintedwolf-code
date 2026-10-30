#include <linux/stat.h>
#include <linux/filter.h>
#include <linux/seccomp.h>
#include <linux/audit.h>
#include "syscall.h"

static int text(const char *a, const char *b) {
    for (int n = 0;; n++) {
        if (a[n] != b[n]) return 0;
        if (!a[n]) return 1;
    }
}

static unsigned int number(const char *s) {
    unsigned long n = 0;
    pw_require(*s != 0);
    for (; *s; s++) {
        pw_require(*s >= '0' && *s <= '9' && n <= 429496729UL);
        n = n * 10 + *s - '0';
    }
    pw_require(n < 4294967295UL);
    return n;
}

static void put32(unsigned char *r, int offset, unsigned int n) {
    for (int i = 0; i < 4; i++) r[offset + i] = (unsigned char)(n >> (8 * i));
}

static void put64(unsigned char *r, int offset, unsigned long long n) {
    for (int i = 0; i < 8; i++) r[offset + i] = (unsigned char)(n >> (8 * i));
}

static void end(void) {
    PW_CALL(__NR_exit_group, 0, 0, 0, 0, 0, 0);
    pw_refuse();
}

static void namespace_factory(unsigned int uid, unsigned int gid) {
    pw_require(PW_CALL(__NR_geteuid, 0, 0, 0, 0, 0, 0) == 0);
    if (uid) {
        pw_require(PW_CALL(__NR_prctl, 8, 1, 0, 0, 0, 0) == 0);
        pw_require(PW_CALL(__NR_setgroups, 0, 0, 0, 0, 0, 0) == 0);
        pw_require(PW_CALL(__NR_setresgid, gid, gid, gid, 0, 0, 0) == 0);
        pw_require(PW_CALL(__NR_setresuid, uid, uid, uid, 0, 0, 0) == 0);
        struct { unsigned int version; int pid; } header = {0x20080522, 0};
        unsigned int caps[6] = {1U << 21, 1U << 21, 0, 0, 0, 0};
        pw_require(PW_CALL(__NR_capset, &header, caps, 0, 0, 0, 0) == 0);
    }
    pw_require(PW_CALL(__NR_unshare, 0x10000000, 0, 0, 0, 0, 0) == 0);
    unsigned char ready = 1;
    pw_require(PW_CALL(__NR_write, 3, &ready, 1, 0, 0, 0) == 1);
    pw_require(PW_CALL(__NR_read, 3, &ready, 1, 0, 0, 0) == 1 && ready == 2);
    end();
}

static void trace_target(const char *mode, unsigned int pid) {
    pw_require(pid > 1);
    long attached = PW_CALL(__NR_ptrace, 16, pid, 0, 0, 0, 0);
    if (text(mode, "--trace-refuse")) {
        pw_require(attached == -1);
        end();
    }
    pw_require(attached == 0);
    int status = 0;
    pw_require(PW_CALL(__NR_wait4, pid, &status, 2, 0, 0, 0) == pid && (status & 255) == 127);
    pw_require(PW_CALL(__NR_ptrace, 17, pid, 0, 0, 0, 0) == 0);
    end();
}

static void identity_report(unsigned char *report) {
    put32(report, 8, (unsigned int)PW_CALL(__NR_getpid, 0, 0, 0, 0, 0, 0));
    unsigned int ids[3];
    pw_require(PW_CALL(__NR_getresuid, &ids[0], &ids[1], &ids[2], 0, 0, 0) == 0);
    for (int i = 0; i < 3; i++) put32(report, 12 + 4 * i, ids[i]);
    pw_require(PW_CALL(__NR_getresgid, &ids[0], &ids[1], &ids[2], 0, 0, 0) == 0);
    for (int i = 0; i < 3; i++) put32(report, 24 + 4 * i, ids[i]);
    struct { unsigned int version; int pid; } header = {0x20080522, 0};
    unsigned int caps[6];
    pw_require(PW_CALL(__NR_capget, &header, caps, 0, 0, 0, 0) == 0);
    for (int i = 0; i < 6; i++) put32(report, 36 + 4 * i, caps[i]);
    put32(report, 60, (unsigned int)PW_CALL(__NR_prctl, 39, 0, 0, 0, 0, 0));
    unsigned int groups[8];
    long count = PW_CALL(__NR_getgroups, 8, groups, 0, 0, 0, 0);
    pw_require(count >= 0 && count <= 8);
    put32(report, 96, (unsigned int)count);
    for (int i = 0; i < count; i++) put32(report, 100 + 4 * i, groups[i]);
    unsigned long long bound = 0;
    for (int cap = 0; cap < 64; cap++) {
        long value = PW_CALL(__NR_prctl, 23, cap, 0, 0, 0, 0);
        if (value == -22) break;
        pw_require(value == 0 || value == 1);
        if (value) bound |= 1ULL << cap;
    }
    put64(report, 144, bound);
}

static void descriptor_report(unsigned char *report) {
    unsigned int mask[4] = {0, 0, 0, 0};
    for (int fd = 0; fd < 128; fd++) {
        if (PW_CALL(__NR_fcntl, fd, 1, 0, 0, 0, 0) >= 0) mask[fd / 32] |= 1U << (fd % 32);
    }
    for (int i = 0; i < 4; i++) put32(report, 80 + 4 * i, mask[i]);
    int ns = (int)PW_CALL(__NR_openat, -100, "/proc/thread-self/ns/user", 0x80000, 0, 0, 0);
    pw_require(ns >= 0);
    unsigned int owner = ~0U;
    pw_require(PW_CALL(__NR_ioctl, ns, 0xb704, &owner, 0, 0, 0) == 0);
    put32(report, 132, owner);
    struct statx facts;
    pw_require(PW_CALL(__NR_statx, ns, "", 0x1100, STATX_BASIC_STATS, &facts, 0) == 0);
    put64(report, 136, facts.stx_ino);
    pw_require(PW_CALL(__NR_close, ns, 0, 0, 0, 0, 0) == 0);
}

// The fixed test prelude changes only the final guardian entry syscall.
// It installs no host policy and does not change no_new_privs or system state.
static void bootstrap_fault(unsigned int uid, unsigned int gid, const char *mode, const char **environment) {
    pw_require(uid > 0 && PW_CALL(__NR_geteuid, 0, 0, 0, 0, 0, 0) == 0);
    int deny = text(mode, "deny");
    pw_require(deny || text(mode, "allow"));
#if defined(__x86_64__)
    const unsigned int architecture = AUDIT_ARCH_X86_64;
#elif defined(__aarch64__)
    const unsigned int architecture = AUDIT_ARCH_AARCH64;
#else
#error Unsupported fixed test architecture
#endif
    struct sock_filter code[] = {
        BPF_STMT(BPF_LD | BPF_W | BPF_ABS, 4),
        BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, architecture, 1, 0),
        BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_KILL_PROCESS),
        BPF_STMT(BPF_LD | BPF_W | BPF_ABS, 0),
        BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, __NR_execveat, 0, 3),
        BPF_STMT(BPF_LD | BPF_W | BPF_ABS, 16),
        BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, 8, 0, 1),
        BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_ALLOW),
        BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_ALLOW),
    };
    if (deny) code[7].k = SECCOMP_RET_ERRNO | 1;
    struct sock_fprog program = {sizeof(code) / sizeof(code[0]), code};
    pw_require(PW_CALL(__NR_prctl, 22, 2, &program, 0, 0, 0) == 0);
    unsigned char armed = deny ? 2 : 1;
    pw_require(PW_CALL(__NR_write, 4, &armed, 1, 0, 0, 0) == 1);
    pw_require(PW_CALL(__NR_close, 4, 0, 0, 0, 0, 0) == 0);
    pw_require(PW_CALL(__NR_setgroups, 1, &gid, 0, 0, 0, 0) == 0);
    pw_require(PW_CALL(__NR_setresgid, gid, gid, gid, 0, 0, 0) == 0);
    pw_require(PW_CALL(__NR_setresuid, uid, 0, 0, 0, 0, 0) == 0);
    const char *arguments[] = {"pw-launch", 0};
    // The root driver supplies only the caller-owned HOME and fixed PATH.
    PW_CALL(__NR_execveat, 3, "", arguments, environment, 0x1000, 0);
    pw_refuse();
}

__attribute__((noreturn)) void pw_mount_transition(const unsigned long *stack) {
    const char **args = (const char **)(stack + 1);
    if (stack[0] == 5 && text(args[1], "--bootstrap-fault")) {
        bootstrap_fault(number(args[2]), number(args[3]), args[4], args + stack[0] + 1);
    }
    if (stack[0] == 4 && text(args[1], "--namespace-factory")) {
        namespace_factory(number(args[2]), number(args[3]));
    }
    if (stack[0] == 3 && (text(args[1], "--trace-allow") || text(args[1], "--trace-refuse"))) {
        trace_target(args[1], number(args[2]));
    }
    pw_require(stack[0] == 2 && (text(args[1], "--protected-guardian") || text(args[1], "--ordinary-target")));
    unsigned char report[160];
    for (int i = 0; i < 160; i++) *((volatile unsigned char *)report + i) = 0;
    const char magic[8] = {'P', 'W', 'G', 'C', 'P', 'R', '0', '1'};
    for (int i = 0; i < 8; i++) report[i] = magic[i];
    identity_report(report);
    put32(report, 68, (unsigned int)stack[0]);
    put32(report, 72, text(args[0], "pw-launch-entry"));
    put32(report, 76, stack[stack[0] + 2] == 0);
    descriptor_report(report);
    // Challenge the namespace boundary even with permissive ptrace settings.
    pw_require(PW_CALL(__NR_prctl, 4, 1, 0, 0, 0, 0) == 0);
    pw_require(PW_CALL(__NR_prctl, 0x59616d61, -1, 0, 0, 0, 0) == 0);
    put32(report, 64, 1);
    pw_require(PW_CALL(__NR_write, 12, report, sizeof(report), 0, 0, 0) == sizeof(report));
    unsigned char release = 0;
    pw_require(PW_CALL(__NR_read, 12, &release, 1, 0, 0, 0) == 1 && release == 2);
    pw_require(PW_CALL(__NR_prctl, 4, 0, 0, 0, 0, 0) == 0);
    put32(report, 64, (unsigned int)PW_CALL(__NR_prctl, 3, 0, 0, 0, 0, 0));
    pw_require(PW_CALL(__NR_write, 12, report, sizeof(report), 0, 0, 0) == sizeof(report));
    end();
}
