#ifndef PW_MOUNT_INTAKE_SYSCALL_H
#define PW_MOUNT_INTAKE_SYSCALL_H
#include <asm/unistd.h>

/* This fixed transition has no C runtime, signal handler or allocator. */
static inline long pw_syscall(long number, long a, long b, long c, long d, long e, long f) {
#if defined(__x86_64__)
    register long r10 __asm__("r10") = d;
    register long r8 __asm__("r8") = e;
    register long r9 __asm__("r9") = f;
    long result;
    __asm__ volatile("syscall" : "=a"(result) : "a"(number), "D"(a), "S"(b), "d"(c), "r"(r10), "r"(r8), "r"(r9) : "rcx", "r11", "memory");
    return result;
#elif defined(__aarch64__)
    register long x0 __asm__("x0") = a;
    register long x1 __asm__("x1") = b;
    register long x2 __asm__("x2") = c;
    register long x3 __asm__("x3") = d;
    register long x4 __asm__("x4") = e;
    register long x5 __asm__("x5") = f;
    register long x8 __asm__("x8") = number;
    __asm__ volatile("svc 0" : "+r"(x0) : "r"(x1), "r"(x2), "r"(x3), "r"(x4), "r"(x5), "r"(x8) : "memory");
    return x0;
#else
#error unsupported Linux mount intake architecture
#endif
}

__attribute__((noreturn)) static void pw_refuse(void) {
    pw_syscall(__NR_exit_group, 125, 0, 0, 0, 0, 0);
    __builtin_trap();
}
static void pw_require(int condition) { if (!condition) pw_refuse(); }
#define PW_CALL(number,a,b,c,d,e,f) pw_syscall(number,(long)(a),(long)(b),(long)(c),(long)(d),(long)(e),(long)(f))
#endif
