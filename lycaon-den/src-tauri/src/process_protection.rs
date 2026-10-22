//! Protect the desktop's password and presence key before initialization.

pub(crate) fn harden_release() -> std::io::Result<()> {
    #[cfg(all(target_os = "linux", not(debug_assertions)))]
    return linux::harden();
    #[cfg(any(not(target_os = "linux"), debug_assertions))]
    Ok(())
}

#[cfg(all(target_os = "linux", any(not(debug_assertions), test)))]
mod linux {
    pub(super) fn harden() -> std::io::Result<()> {
        harden_with(|option| {
            // SAFETY: these prctl operations take integer arguments and touch no memory.
            unsafe { libc::prctl(option, 0, 0, 0, 0) }
        })
    }

    fn harden_with(mut prctl: impl FnMut(libc::c_int) -> libc::c_int) -> std::io::Result<()> {
        if prctl(libc::PR_SET_DUMPABLE) != 0 {
            return Err(std::io::Error::last_os_error());
        }
        match prctl(libc::PR_GET_DUMPABLE) {
            0 => Ok(()),
            -1 => Err(std::io::Error::last_os_error()),
            _ => Err(std::io::Error::new(
                std::io::ErrorKind::PermissionDenied,
                "process remains dumpable",
            )),
        }
    }

    #[cfg(test)]
    mod tests {
        use super::*;

        #[test]
        fn failed_transition_or_readback_refuses_initialization() {
            assert!(harden_with(|_| -1).is_err());
            assert!(harden_with(|option| if option == libc::PR_GET_DUMPABLE {
                -1
            } else {
                0
            })
            .is_err());
            assert!(harden_with(|option| if option == libc::PR_GET_DUMPABLE {
                1
            } else {
                0
            })
            .is_err());
        }

        #[test]
        fn successful_transition_requires_non_dumpable_readback() {
            let mut calls = Vec::new();
            assert!(harden_with(|option| {
                calls.push(option);
                0
            })
            .is_ok());
            assert_eq!(calls, [libc::PR_SET_DUMPABLE, libc::PR_GET_DUMPABLE]);
        }

        #[test]
        fn native_hardening_stays_in_a_disposable_child() {
            const CHILD: &str = "PW_TEST_PROCESS_PROTECTION_CHILD";
            if std::env::var_os(CHILD).is_some() {
                harden().expect("make child non-dumpable");
                return;
            }
            let status = std::process::Command::new(std::env::current_exe().unwrap())
                .args(["--exact", "process_protection::linux::tests::native_hardening_stays_in_a_disposable_child"])
                .env(CHILD, "1")
                .status()
                .expect("run protected child");
            assert!(status.success());
        }
    }
}
