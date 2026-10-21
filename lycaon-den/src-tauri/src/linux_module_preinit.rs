//! No allocation or library calls may precede the root-bound loader handshake.

/// Root-written policy frame and the auditor's response share this nonce span.
const NONCE: core::ops::Range<usize> = 16..48;

/// Whether the loader handshake proves a root-sealed policy and its auditor.
///
/// `facts` is the policy descriptor's `statx` record, `seals` its `F_GET_SEALS`
/// result, `header` the policy's first 48 bytes and `response` the auditor's
/// 40-byte witness. Volatile byte access keeps the compiler from emitting libc
/// `memcmp` calls before shared-library constructors run.
pub(crate) fn admitted(facts: &[u8; 256], seals: isize, header: &[u8; 48], response: &[u8; 40]) -> bool {
    let byte = |bytes: &[u8], index: usize| unsafe { bytes.as_ptr().add(index).read_volatile() };
    if seals < 0 || seals & 15 != 15 {
        return false;
    }
    // stx_uid is root, no write bits, and stx_mode is a regular file.
    for index in 20..24 {
        if byte(facts, index) != 0 {
            return false;
        }
    }
    if byte(facts, 28) & 0x92 != 0 || byte(facts, 29) & 0xf0 != 0x80 {
        return false;
    }
    for index in 0..8 {
        if byte(header, index) != byte(b"PWMOD02\0", index) || byte(response, index) != byte(b"PWAUD01\0", index) {
            return false;
        }
    }
    for index in NONCE {
        if byte(header, index) != byte(response, index - 8) {
            return false;
        }
    }
    true
}

#[cfg(not(test))]
mod gate {
    use super::admitted;
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
    // read, close, pread64, fcntl, exit_group, statx
    #[cfg(target_arch = "x86_64")]
    const NUMBERS: [usize; 6] = [0, 3, 17, 72, 231, 332];
    #[cfg(target_arch = "aarch64")]
    const NUMBERS: [usize; 6] = [63, 57, 67, 25, 94, 291];

    unsafe fn refuse() -> ! {
        call(NUMBERS[4], 125, 0, 0, 0, 0);
        core::hint::unreachable_unchecked()
    }

    unsafe extern "C" fn gate() {
        // Uninitialized buffers: zeroing could emit a libc memset before its constructors.
        let mut facts = MaybeUninit::<[u8; 256]>::uninit();
        let mut header = MaybeUninit::<[u8; 48]>::uninit();
        let mut response = MaybeUninit::<[u8; 40]>::uninit();
        let seals = call(NUMBERS[3], 7, 1034, 0, 0, 0);
        // Each buffer is read only after its syscall filled every byte.
        if call(NUMBERS[5], 7, c"".as_ptr() as usize, 0x1100, 0x7ff, facts.as_mut_ptr() as usize) != 0
            || call(NUMBERS[2], 7, header.as_mut_ptr() as usize, 48, 0, 0) != 48
            || call(NUMBERS[0], 9, response.as_mut_ptr() as usize, 40, 0, 0) != 40
            || !admitted(facts.assume_init_ref(), seals, header.assume_init_ref(), response.assume_init_ref())
        {
            refuse()
        }
        for fd in [7, 9, 10] {
            call(NUMBERS[1], fd, 0, 0, 0, 0);
        }
    }

    // glibc invokes executable preinit before ordinary shared-library constructors.
    #[used]
    #[link_section = ".preinit_array"]
    static PREINIT: unsafe extern "C" fn() = gate;
}

#[cfg(test)]
mod tests {
    use super::admitted;

    struct Handshake {
        facts: [u8; 256],
        seals: isize,
        header: [u8; 48],
        response: [u8; 40],
    }

    impl Handshake {
        /// A root-owned 0444 sealed regular policy and the auditor's matching witness.
        fn valid() -> Self {
            let mut facts = [0u8; 256];
            facts[28..30].copy_from_slice(&0o100444u16.to_le_bytes());
            let mut header = [0u8; 48];
            header[..8].copy_from_slice(b"PWMOD02\0");
            let mut response = [0u8; 40];
            response[..8].copy_from_slice(b"PWAUD01\0");
            for index in 0..32 {
                header[16 + index] = index as u8 + 1;
                response[8 + index] = index as u8 + 1;
            }
            Self { facts, seals: 15, header, response }
        }

        fn admitted(&self) -> bool {
            admitted(&self.facts, self.seals, &self.header, &self.response)
        }
    }

    #[test]
    fn admits_a_root_sealed_policy_with_the_auditors_matching_witness() {
        assert!(Handshake::valid().admitted());
        let mut extra = Handshake::valid();
        extra.seals = 15 | 16;
        assert!(extra.admitted(), "additional seals keep the policy immutable");
    }

    #[test]
    fn refuses_policies_that_are_not_root_owned_immutable_regular_files() {
        let cases: [(&str, fn(&mut Handshake)); 7] = [
            ("owned by a user", |h| h.facts[20] = 0xe8),
            ("owned by a high uid", |h| h.facts[23] = 1),
            ("owner writable", |h| h.facts[28..30].copy_from_slice(&0o100644u16.to_le_bytes())),
            ("group writable", |h| h.facts[28..30].copy_from_slice(&0o100464u16.to_le_bytes())),
            ("a directory", |h| h.facts[28..30].copy_from_slice(&0o040555u16.to_le_bytes())),
            ("missing write seal", |h| h.seals = 7),
            ("unreadable seals", |h| h.seals = -9),
        ];
        for (name, alter) in cases {
            let mut handshake = Handshake::valid();
            alter(&mut handshake);
            assert!(!handshake.admitted(), "{name} was admitted");
        }
    }

    #[test]
    fn refuses_frames_that_do_not_bind_this_policy_to_its_auditor() {
        let cases: [(&str, fn(&mut Handshake)); 5] = [
            ("policy format", |h| h.header[6] = b'1'),
            ("auditor format", |h| h.response[0] = b'X'),
            ("first nonce byte", |h| h.response[8] ^= 1),
            ("last nonce byte", |h| h.header[47] ^= 1),
            ("zero witness", |h| h.response = [0; 40]),
        ];
        for (name, alter) in cases {
            let mut handshake = Handshake::valid();
            alter(&mut handshake);
            assert!(!handshake.admitted(), "altered {name} was admitted");
        }
    }
}
