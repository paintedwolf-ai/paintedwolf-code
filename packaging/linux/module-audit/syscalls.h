#ifndef PW_MODULE_SYSCALLS_H
#define PW_MODULE_SYSCALLS_H
#include <stdint.h>
#include <stddef.h>
#include <linux/stat.h>
#include <linux/fcntl.h>
#if defined(__x86_64__)
#define PW_READ 0
#define PW_WRITE 1
#define PW_CLOSE 3
#define PW_PREAD 17
#define PW_FCNTL 72
#define PW_EXIT 231
#define PW_OPENAT 257
#define PW_READLINKAT 267
#define PW_STATX 332
static inline long pw_call(long n,long a,long b,long c,long d,long e,long f) {
 register long r10 __asm__("r10")=d,r8 __asm__("r8")=e,r9 __asm__("r9")=f;
 long value;
 __asm__ volatile("syscall":"=a"(value):"a"(n),"D"(a),"S"(b),"d"(c),"r"(r10),"r"(r8),"r"(r9):"rcx","r11","memory");
 return value;
}
#elif defined(__aarch64__)
#define PW_READ 63
#define PW_WRITE 64
#define PW_CLOSE 57
#define PW_PREAD 67
#define PW_FCNTL 25
#define PW_EXIT 94
#define PW_OPENAT 56
#define PW_READLINKAT 78
#define PW_STATX 291
static inline long pw_call(long n,long a,long b,long c,long d,long e,long f) {
 register long x8 __asm__("x8")=n,x0 __asm__("x0")=a,x1 __asm__("x1")=b,x2 __asm__("x2")=c;
 register long x3 __asm__("x3")=d,x4 __asm__("x4")=e,x5 __asm__("x5")=f;
 __asm__ volatile("svc 0":"+r"(x0):"r"(x8),"r"(x1),"r"(x2),"r"(x3),"r"(x4),"r"(x5):"memory");
 return x0;
}
#else
#error Unsupported Linux module auditor architecture
#endif
static inline long pw_stat(int fd,struct statx *facts) {
 return pw_call(PW_STATX,fd,(long)"",AT_EMPTY_PATH|AT_SYMLINK_NOFOLLOW,STATX_BASIC_STATS,(long)facts,0);
}
static inline void pw_close(int fd) {pw_call(PW_CLOSE,fd,0,0,0,0,0);}
__attribute__((noreturn)) static void pw_refuse(void) {
 static const char message[]="Painted Wolf Code refused an untrusted native module.\n";
 pw_call(PW_WRITE,2,(long)message,sizeof(message)-1,0,0,0);
 pw_call(PW_EXIT,125,0,0,0,0,0);
 __builtin_unreachable();
}
static inline size_t pw_length(const char *s,size_t limit) {
 size_t n=0;while(n<limit&&s[n])++n;return n;
}
static inline int pw_equal(const char *a,const char *b,size_t n) {
 for(size_t i=0;i<n;++i) { if(a[i]!=b[i])return 0; }
 return 1;
}
#endif
