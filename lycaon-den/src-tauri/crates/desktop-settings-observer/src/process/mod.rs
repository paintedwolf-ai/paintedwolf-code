//! Owned process lifetime for one fixed observer image, with no action requests.
mod image;
use crate::{read, Frame, Status};
use std::os::unix::process::CommandExt;
use std::{
    io::{self, BufReader},
    path::Path,
    process::{Child, Command, Stdio},
    sync::{
        atomic::{AtomicBool, Ordering},
        mpsc, Arc,
    },
    thread,
    time::{Duration, Instant},
};
const ENVIRONMENT: [&str; 6] = [
    "DBUS_SESSION_BUS_ADDRESS",
    "XDG_RUNTIME_DIR",
    "LANG",
    "LC_ALL",
    "LC_CTYPE",
    "TZ",
];
fn command(executable: &Path) -> Command {
    let mut command = Command::new(executable);
    command
        .env_clear()
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::null());
    for key in ENVIRONMENT {
        if let Some(value) = std::env::var_os(key) {
            command.env(key, value);
        }
    }
    // Only standard streams survive exec. This also closes unrelated inherited
    // engine/control descriptors without exposing secret-bearing parent memory.
    unsafe {
        command.pre_exec(|| {
            if libc::prctl(libc::PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0) != 0 {
                return Err(io::Error::last_os_error());
            }
            if libc::syscall(
                libc::SYS_close_range,
                3u32,
                u32::MAX,
                libc::CLOSE_RANGE_CLOEXEC,
            ) != 0
            {
                return Err(io::Error::last_os_error());
            }
            Ok(())
        });
    }
    command
}
fn stop(child: &mut Child) {
    drop(child.stdin.take());
    let deadline = Instant::now() + Duration::from_secs(2);
    loop {
        match child.try_wait() {
            Ok(Some(_)) => return,
            Err(_) => break,
            Ok(None) => {}
        }
        if Instant::now() >= deadline {
            break;
        }
        thread::sleep(Duration::from_millis(20));
    }
    let _ = child.kill();
    let _ = child.wait();
}
fn one(
    shell: &Path,
    stop_requested: &AtomicBool,
    mut observe: impl FnMut(Frame),
) -> io::Result<Status> {
    let image = image::Image::open(shell)?;
    let mut child = command(&image.executable()).spawn()?;
    let stdout = child
        .stdout
        .take()
        .ok_or_else(|| io::Error::other("settings observer pipe unavailable"))?;
    let (sender, receiver) = mpsc::sync_channel(1);
    let reader = thread::spawn(move || {
        let mut input = BufReader::new(stdout);
        let mut sequence = 0;
        loop {
            let result = read(&mut input, sequence);
            if let Ok(Some(frame)) = &result {
                sequence = frame.sequence;
            }
            let done = !matches!(&result, Ok(Some(_)));
            if sender.send(result).is_err() || done {
                break;
            }
        }
    });
    let mut result = Status::Disconnected;
    let mut last = Instant::now();
    let mut started = false;
    loop {
        if stop_requested.load(Ordering::Acquire) {
            break;
        }
        match receiver.recv_timeout(Duration::from_millis(100)) {
            Ok(Ok(Some(frame))) => {
                last = Instant::now();
                result = frame.status;
                started = true;
                observe(frame);
                if result != Status::Ready {
                    break;
                }
            }
            Ok(Err(_)) => {
                result = Status::Failed;
                break;
            }
            Ok(Ok(None)) | Err(mpsc::RecvTimeoutError::Disconnected) => {
                result = Status::Disconnected;
                break;
            }
            Err(mpsc::RecvTimeoutError::Timeout) => {}
        }
        if last.elapsed() > Duration::from_secs(if started { 30 } else { 5 }) {
            result = Status::TimedOut;
            break;
        }
    }
    // Drop the bounded receiver before joining a reader blocked in send.
    drop(receiver);
    stop(&mut child);
    let _ = reader.join();
    Ok(result)
}
pub fn watch(
    shell: &Path,
    stop_requested: Arc<AtomicBool>,
    mut observe: impl FnMut(Frame),
    mut status: impl FnMut(Status),
) {
    for attempt in 0..5 {
        if stop_requested.load(Ordering::Acquire) {
            return;
        }
        let outcome = one(shell, &stop_requested, &mut observe).unwrap_or_else(|error| {
            if error.kind() == io::ErrorKind::NotFound {
                Status::Missing
            } else {
                Status::Failed
            }
        });
        if stop_requested.load(Ordering::Acquire) {
            return;
        }
        status(outcome);
        if matches!(outcome, Status::Denied | Status::Failed) {
            return;
        }
        let deadline = Instant::now() + Duration::from_secs(1 << attempt);
        while !stop_requested.load(Ordering::Acquire) && Instant::now() < deadline {
            thread::sleep(Duration::from_millis(100));
        }
    }
    status(Status::Failed);
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn fixed_child_observes_only_safe_environment_and_no_inherited_fds() {
        if std::env::var_os("PW_OBSERVER_TEST_CHILD").is_none() {
            return;
        }
        if std::env::var("PW_OBSERVER_TEST_CHILD").unwrap() == "-1" {
            assert_eq!(unsafe { libc::prctl(libc::PR_SET_DUMPABLE, 0) }, 0);
            let mut observer = command(&std::env::current_exe().unwrap())
                .args(["--exact", "process::tests::fixed_child_observes_only_safe_environment_and_no_inherited_fds"])
                .env("PW_OBSERVER_TEST_CHILD", "200")
                .spawn().unwrap();
            assert!(observer.wait().unwrap().success());
            assert_eq!(unsafe { libc::prctl(libc::PR_GET_DUMPABLE) }, 0);
            return;
        }
        assert_eq!(unsafe { libc::prctl(libc::PR_GET_DUMPABLE) }, 1);
        assert_eq!(
            unsafe { libc::prctl(libc::PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0) },
            1
        );
        for (name, _) in std::env::vars_os() {
            assert!(ENVIRONMENT.iter().any(|key| name == *key) || name == "PW_OBSERVER_TEST_CHILD");
        }
        let descriptor: i32 = std::env::var("PW_OBSERVER_TEST_CHILD")
            .unwrap()
            .parse()
            .unwrap();
        assert_eq!(unsafe { libc::fcntl(descriptor, libc::F_GETFD) }, -1);
    }
    #[test]
    fn actual_exec_clears_environment_descriptors_and_reaps_owned_child() {
        use std::os::fd::AsRawFd;
        let secret = std::fs::File::open("/dev/null").unwrap();
        let descriptor = unsafe { libc::fcntl(secret.as_raw_fd(), libc::F_DUPFD, 200) };
        assert!(descriptor >= 200);
        let mut child = command(&std::env::current_exe().unwrap())
            .args([
                "--exact",
                "process::tests::fixed_child_observes_only_safe_environment_and_no_inherited_fds",
                "--nocapture",
            ])
            .env("PW_OBSERVER_TEST_CHILD", descriptor.to_string())
            .spawn()
            .unwrap();
        unsafe {
            libc::close(descriptor);
        }
        let result = child.wait().unwrap();
        assert!(result.success());
        stop(&mut child);
        assert!(child.try_wait().unwrap().is_some());
    }
    #[test]
    fn separate_exec_observes_without_relaxing_the_nondumpable_parent() {
        let mut hardened = command(&std::env::current_exe().unwrap())
            .args([
                "--exact",
                "process::tests::fixed_child_observes_only_safe_environment_and_no_inherited_fds",
            ])
            .env("PW_OBSERVER_TEST_CHILD", "-1")
            .spawn()
            .unwrap();
        assert!(hardened.wait().unwrap().success());
    }
    #[test]
    fn environment_never_inherits_parent_credentials() {
        assert!(!ENVIRONMENT.iter().any(|name| name.contains("TOKEN")
            || name.contains("KEY")
            || name.contains("PASSWORD")));
        assert_eq!(ENVIRONMENT.len(), 6);
    }
    #[test]
    fn foreign_or_missing_image_cannot_select_a_command() {
        assert!(image::Image::open(Path::new("/nonexistent/bin/painted-wolf-code")).is_err());
        assert!(image::Image::open(Path::new("/bin/sh")).is_err());
    }
}
