//! No allocation or library calls may precede the root-bound loader handshake.
use core::arch::asm;
use core::mem::MaybeUninit;

#[cfg(target_arch = "x86_64")]
unsafe fn call(n: usize, a: usize, b: usize, c: usize, d: usize, e: usize) -> isize {
    let result: isize;
    asm!("syscall", inlateout("rax") n => result, in("rdi") a, in("rsi") b,
        in("rdx") c, in("r10") d, in("r8") e, lateout("rcx") _, lateout("r11") _,
        options(nostack));
    result
}
#[cfg(target_arch = "aarch64")]
unsafe fn call(n: usize, a: usize, b: usize, c: usize, d: usize, e: usize) -> isize {
    let result: isize;
    asm!("svc 0", in("x8") n, inlateout("x0") a => result, in("x1") b,
        in("x2") c, in("x3") d, in("x4") e, options(nostack));
    result
}
#[cfg(target_arch = "x86_64")]
const NUMBERS: [usize; 6] = [0, 3, 17, 72, 231, 332];
#[cfg(target_arch = "aarch64")]
const NUMBERS: [usize; 6] = [63, 57, 67, 25, 94, 291];

unsafe fn refuse() -> ! {
    call(NUMBERS[4], 125, 0, 0, 0, 0);
    core::hint::unreachable_unchecked()
}
unsafe extern "C" fn gate() {
    let mut facts = MaybeUninit::<[u8; 256]>::uninit();
    let mut header = MaybeUninit::<[u8; 48]>::uninit();
    let mut response = MaybeUninit::<[u8; 40]>::uninit();
    let seals = call(NUMBERS[3], 7, 1034, 0, 0, 0);
    if call(NUMBERS[5], 7, c"".as_ptr() as usize, 0x1100, 0x7ff,
        facts.as_mut_ptr() as usize) != 0
        || seals < 0 || seals & 15 != 15
        || call(NUMBERS[2], 7, header.as_mut_ptr() as usize, 48, 0, 0) != 48
        || call(NUMBERS[0], 9, response.as_mut_ptr() as usize, 40, 0, 0) != 40
    { refuse() }
    let facts = facts.as_ptr().cast::<u8>();
    let header = header.as_ptr().cast::<u8>();
    let response = response.as_ptr().cast::<u8>();
    // Volatile byte access avoids libc memcpy/memcmp before their constructors.
    for i in 20..24 {
        if facts.add(i).read_volatile() != 0 { refuse() }
    }
    if facts.add(28).read_volatile() & 0x92 != 0
        || facts.add(29).read_volatile() & 0xf0 != 0x80
    { refuse() }
    for i in 0..8 {
        if header.add(i).read_volatile() != b"PWMOD02\0".as_ptr().add(i).read_volatile()
            || response.add(i).read_volatile() != b"PWAUD01\0".as_ptr().add(i).read_volatile()
        { refuse() }
    }
    for i in 0..32 {
        if header.add(16 + i).read_volatile() != response.add(8 + i).read_volatile() { refuse() }
    }
    for fd in [7, 9, 10] { call(NUMBERS[1], fd, 0, 0, 0, 0); }
}

// glibc invokes executable preinit before ordinary shared-library constructors.
#[used]
#[link_section = ".preinit_array"]
static PREINIT: unsafe extern "C" fn() = gate;
