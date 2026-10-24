#if defined(__x86_64__)
.text
.globl _start
.type _start,@function
_start:
    movq %rsp, %rdi
    andq $-16, %rsp
    xorl %ebp, %ebp
    call pw_mount_transition
    ud2
#elif defined(__aarch64__)
.text
.globl _start
.type _start,%function
_start:
    mov x0, sp
    mov x29, xzr
    mov x30, xzr
    bl pw_mount_transition
    brk #0
#else
#error unsupported Linux mount intake architecture
#endif
.section .note.GNU-stack,"",%progbits
