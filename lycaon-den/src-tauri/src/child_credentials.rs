//! External applications leave the desktop's protected credential envelope.
use std::process::Command;

/// Install the credential boundary before any untrusted executable runs.
pub(crate) fn prepare(command: &mut Command) {
    #[cfg(target_os = "linux")]
    {
        use std::os::unix::process::CommandExt;
        unsafe {
            command.pre_exec(|| {
                if drop_groups() == 0 { Ok(()) } else { Err(std::io::Error::last_os_error()) }
            });
        }
    }
    #[cfg(not(target_os = "linux"))]
    let _ = command;
}

// These raw syscalls run only in the single-threaded child between fork and exec.
// No allocator, locks, environment lookup or arbitrary callback runs there.
#[cfg(target_os = "linux")]
unsafe fn drop_groups() -> libc::c_int {
    let gid = libc::getgid();
    if libc::syscall(libc::SYS_setresgid, gid, gid, gid) != 0 {
        return -1;
    }
    let (mut real, mut effective, mut saved): (libc::gid_t, libc::gid_t, libc::gid_t) = (0, 0, 0);
    if libc::syscall(libc::SYS_getresgid, &mut real, &mut effective, &mut saved) != 0 {
        return -1;
    }
    if (real, effective, saved) != (gid, gid, gid) {
        *libc::__errno_location() = libc::EPERM;
        return -1;
    }
    0
}

/// GIO's raw child-setup callback avoids a boxed Rust closure after fork.
#[cfg(target_os = "linux")]
pub(crate) unsafe extern "C" fn desktop_setup(_: *mut libc::c_void) {
    if drop_groups() != 0 {
        libc::_exit(126);
    }
}

#[cfg(all(test, target_os = "linux"))]
mod tests {
    use super::*;
    #[test]
    fn external_exec_keeps_only_the_real_group() {
        const MARKER: &str = "PW_CHILD_CREDENTIALS_TEST";
        if std::env::var_os(MARKER).is_some() {
            let (mut real, mut effective, mut saved): (libc::gid_t, libc::gid_t, libc::gid_t) = (0, 0, 0);
            assert_eq!(unsafe { libc::getresgid(&mut real, &mut effective, &mut saved) }, 0);
            println!("observed-groups={real},{effective},{saved}");
            return;
        }
        let gid = unsafe { libc::getgid() };
        let mut command = Command::new(std::env::current_exe().unwrap());
        command.args(["--exact", "child_credentials::tests::external_exec_keeps_only_the_real_group", "--nocapture"])
            .env(MARKER, "1");
        prepare(&mut command);
        let result = command.output().unwrap();
        assert!(result.status.success());
        assert!(String::from_utf8(result.stdout).unwrap().lines()
            .any(|line| line == format!("observed-groups={gid},{gid},{gid}")));
    }

    #[test]
    fn generic_shell_permission_is_unavailable_on_linux() {
        let root = std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("capabilities");
        let read = |name| serde_json::from_slice::<serde_json::Value>(
            &std::fs::read(root.join(name)).unwrap()).unwrap();
        let default = read("default.json");
        assert!(!default["permissions"].as_array().unwrap().iter().any(|entry|
            entry["identifier"] == "shell:allow-open" || entry == "shell:allow-open"));
        let scoped = read("platform-shell-open.json");
        assert_eq!(scoped["platforms"], serde_json::json!(["macOS", "windows"]));
        assert_eq!(scoped["permissions"][0]["identifier"], "shell:allow-open");
    }
}
