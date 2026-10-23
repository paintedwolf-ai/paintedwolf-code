#![no_std]

use core::{arch::asm, panic::PanicInfo, ptr, slice};
use sha2::{Digest, Sha256};

#[cfg(not(all(target_os = "linux", any(target_arch = "x86_64", target_arch = "aarch64"))))]
compile_error!("The module digest adapter requires Linux x86_64 or aarch64");

const MAX_BYTES: u64 = u64::MAX / 8;

#[cfg(target_arch = "x86_64")]
unsafe fn pread(fd: i32, buffer: *mut u8, count: usize, offset: u64) -> isize {
    let result: isize;
    asm!("syscall", inlateout("rax") 17isize => result,
        in("rdi") fd as isize, in("rsi") buffer, in("rdx") count,
        in("r10") offset, lateout("rcx") _, lateout("r11") _, options(nostack));
    result
}

#[cfg(target_arch = "aarch64")]
unsafe fn pread(fd: i32, buffer: *mut u8, count: usize, offset: u64) -> isize {
    let result: isize;
    asm!("svc 0", in("x8") 67usize, inlateout("x0") fd as isize => result,
        in("x1") buffer, in("x2") count, in("x3") offset, options(nostack));
    result
}

#[panic_handler]
fn panic(_: &PanicInfo<'_>) -> ! {
    loop {
        unsafe {
            #[cfg(target_arch = "x86_64")]
            asm!("syscall", in("rax") 231usize, in("rdi") 125usize,
                lateout("rcx") _, lateout("r11") _, options(nostack));
            #[cfg(target_arch = "aarch64")]
            asm!("svc 0", in("x8") 94usize, in("x0") 125usize, options(nostack));
        }
    }
}

unsafe fn finish(hash: Sha256, output: *mut u8) {
    let digest = hash.finalize();
    for (index, byte) in digest.iter().enumerate() {
        ptr::write(output.add(index), *byte);
    }
}

/// Hashes a caller-validated readable region; output must hold 32 writable bytes.
/// Returns zero on success, minus one for invalid bounds/pointers.
#[no_mangle]
pub unsafe extern "C" fn pw_sha256_bytes(input: *const u8, length: usize, output: *mut u8) -> i32 {
    if output.is_null() || (input.is_null() && length != 0)
        || length > isize::MAX as usize || length as u64 > MAX_BYTES
    {
        return -1;
    }
    let mut hash = Sha256::new();
    if length != 0 {
        hash.update(slice::from_raw_parts(input, length));
    }
    finish(hash, output);
    0
}

/// Hashes exactly expected_length bytes from offset zero and requires EOF.
/// The caller owns file validation and immutability. This leaves its FD/offset intact.
/// Output must hold 32 writable bytes and is written only after successful reads.
#[no_mangle]
pub unsafe extern "C" fn pw_sha256_fd(fd: i32, expected_length: u64, output: *mut u8) -> i32 {
    if fd < 0 || output.is_null() || expected_length > MAX_BYTES {
        return -1;
    }
    let mut hash = Sha256::new();
    let mut buffer = [0u8; 4096];
    let mut offset = 0u64;
    while offset < expected_length {
        let count = core::cmp::min(expected_length - offset, buffer.len() as u64) as usize;
        let received = pread(fd, buffer.as_mut_ptr(), count, offset);
        if received == -4 {
            continue;
        }
        if received <= 0 || received as usize > count {
            return -1;
        }
        hash.update(&buffer[..received as usize]);
        offset += received as u64;
    }
    loop {
        match pread(fd, buffer.as_mut_ptr(), 1, offset) {
            -4 => continue,
            0 => break,
            _ => return -1,
        }
    }
    finish(hash, output);
    0
}

// Freestanding compiler memory intrinsics. Volatile byte accesses prevent LLVM
// from lowering these implementations into recursive calls to the same symbols.
#[no_mangle]
pub unsafe extern "C" fn memcpy(destination: *mut u8, source: *const u8, length: usize) -> *mut u8 {
    for index in 0..length {
        ptr::write_volatile(destination.add(index), ptr::read_volatile(source.add(index)));
    }
    destination
}

#[no_mangle]
pub unsafe extern "C" fn memmove(destination: *mut u8, source: *const u8, length: usize) -> *mut u8 {
    if (destination as usize) <= source as usize {
        memcpy(destination, source, length);
    } else {
        for index in (0..length).rev() {
            ptr::write_volatile(destination.add(index), ptr::read_volatile(source.add(index)));
        }
    }
    destination
}

#[no_mangle]
pub unsafe extern "C" fn memset(destination: *mut u8, value: i32, length: usize) -> *mut u8 {
    for index in 0..length {
        ptr::write_volatile(destination.add(index), value as u8);
    }
    destination
}

#[no_mangle]
pub unsafe extern "C" fn memcmp(left: *const u8, right: *const u8, length: usize) -> i32 {
    for index in 0..length {
        let difference = ptr::read_volatile(left.add(index)) as i32
            - ptr::read_volatile(right.add(index)) as i32;
        if difference != 0 {
            return difference;
        }
    }
    0
}

#[no_mangle]
pub unsafe extern "C" fn bcmp(left: *const u8, right: *const u8, length: usize) -> i32 {
    memcmp(left, right, length)
}
