//! Owned process lifetime for one fixed observer image, with no action requests.
mod image;
use crate::{read, Frame, Status};
use std::os::fd::{AsRawFd, OwnedFd};
use std::os::unix::process::CommandExt;
use std::{
    ffi::OsString,
    fs::File,
    io::{self, BufReader},
    path::{Path, PathBuf},
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
/// The only variables the observer receives, taken from the shell's own.
pub fn environment() -> Vec<(&'static str, OsString)> {
    ENVIRONMENT
        .iter()
        .filter_map(|key| std::env::var_os(key).map(|value| (*key, value)))
        .collect()
}

/// A started observer: the pipe ends the shell keeps, and the process lifetime.
/// Dropping `input` asks the observer to stop.
pub struct Started {
    pub input: File,
    pub output: File,
    pub process: Box<dyn Process + Send>,
}

/// The lifetime of one started observer.
pub trait Process {
    /// Whether the observer has exited; never blocks.
    fn exited(&mut self) -> io::Result<bool>;
    /// Ends an observer that did not stop when asked, and reaps it.
    fn end(&mut self);
}

/// How the shell starts the verified observer image with exactly `environment`.
pub trait Launch {
    fn start(&self, image: &File, environment: &[(&'static str, OsString)]) -> io::Result<Started>;
}

/// Starts the observer as the shell's own child.
pub struct Local;

impl Launch for Local {
    fn start(&self, image: &File, environment: &[(&'static str, OsString)]) -> io::Result<Started> {
        let executable = PathBuf::from(format!("/proc/self/fd/{}", image.as_raw_fd()));
        let mut child = command(&executable, environment).spawn()?;
        let input = child.stdin.take().map(OwnedFd::from);
        let output = child.stdout.take().map(OwnedFd::from);
        let (Some(input), Some(output)) = (input, output) else {
            stop_child(&mut child);
            return Err(io::Error::other("settings observer pipe unavailable"));
        };
        Ok(Started { input: input.into(), output: output.into(), process: Box::new(child) })
    }
}

impl Process for Child {
    fn exited(&mut self) -> io::Result<bool> {
        self.try_wait().map(|status| status.is_some())
    }

    fn end(&mut self) {
        stop_child(self);
    }
}

fn stop_child(child: &mut Child) {
    let _ = child.kill();
    let _ = child.wait();
}

fn command(executable: &Path, environment: &[(&'static str, OsString)]) -> Command {
    let mut command = Command::new(executable);
    command
        .env_clear()
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::null());
    for (key, value) in environment {
        command.env(key, value);
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
/// Closes the observer's input, gives it two seconds to exit, then ends it.
fn stop(input: File, process: &mut dyn Process) {
    drop(input);
    let deadline = Instant::now() + Duration::from_secs(2);
    loop {
        match process.exited() {
            Ok(true) => return,
            Err(_) => break,
            Ok(false) => {}
        }
        if Instant::now() >= deadline {
            break;
        }
        thread::sleep(Duration::from_millis(20));
    }
    process.end();
}
fn one(
    shell: &Path,
    launcher: &dyn Launch,
    stop_requested: &AtomicBool,
    mut observe: impl FnMut(Frame),
) -> io::Result<Status> {
    let image = image::Image::open(shell)?;
    let Started { input, output: stdout, mut process } = launcher.start(image.file(), &environment())?;
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
    stop(input, process.as_mut());
    let _ = reader.join();
    Ok(result)
}
pub fn watch(
    shell: &Path,
    launcher: &dyn Launch,
    stop_requested: Arc<AtomicBool>,
    mut observe: impl FnMut(Frame),
    mut status: impl FnMut(Status),
) {
    for attempt in 0..5 {
        if stop_requested.load(Ordering::Acquire) {
            return;
        }
        let outcome = one(shell, launcher, &stop_requested, &mut observe).unwrap_or_else(|error| {
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
    use std::os::fd::FromRawFd;
    #[test]
    fn fixed_child_observes_only_safe_environment_and_no_inherited_fds() {
        if std::env::var_os("PW_OBSERVER_TEST_CHILD").is_none() {
            return;
        }
        if std::env::var("PW_OBSERVER_TEST_CHILD").unwrap() == "-1" {
            assert_eq!(unsafe { libc::prctl(libc::PR_SET_DUMPABLE, 0) }, 0);
            let mut observer = command(&std::env::current_exe().unwrap(), &environment())
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
        let mut child = command(&std::env::current_exe().unwrap(), &environment())
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
        let input = File::from(OwnedFd::from(child.stdin.take().unwrap()));
        let result = child.wait().unwrap();
        assert!(result.success());
        stop(input, &mut child);
        assert!(child.try_wait().unwrap().is_some());
    }
    #[test]
    fn separate_exec_observes_without_relaxing_the_nondumpable_parent() {
        let mut hardened = command(&std::env::current_exe().unwrap(), &environment())
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
    fn a_started_observer_hardens_itself_whoever_launched_it() {
        const MARKER: &str = "PW_OBSERVER_HARDEN_CHILD";
        if let Some(descriptor) = std::env::var_os(MARKER) {
            let descriptor: i32 = descriptor.to_str().unwrap().parse().unwrap();
            assert!(unsafe { libc::fcntl(descriptor, libc::F_GETFD) } >= 0, "the launcher's descriptor arrived");
            crate::harden().unwrap();
            assert_eq!(unsafe { libc::fcntl(descriptor, libc::F_GETFD) }, -1);
            assert_eq!(unsafe { libc::prctl(libc::PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0) }, 1);
            println!("\nhardened");
            return;
        }
        // A launcher that, like the broker, sets no no-new-privs and leaves a descriptor open.
        let image = std::fs::File::open("/dev/null").unwrap();
        let descriptor = unsafe { libc::fcntl(image.as_raw_fd(), libc::F_DUPFD, 200) };
        assert!(descriptor >= 200);
        let output = Command::new(std::env::current_exe().unwrap())
            .args(["--exact", "process::tests::a_started_observer_hardens_itself_whoever_launched_it", "--nocapture"])
            .env(MARKER, descriptor.to_string())
            .output()
            .unwrap();
        unsafe { libc::close(descriptor) };
        let stdout = String::from_utf8_lossy(&output.stdout);
        assert!(output.status.success() && stdout.lines().any(|line| line == "hardened"),
            "{}\n{stdout}\n{}", output.status, String::from_utf8_lossy(&output.stderr));
    }

    struct Scripted {
        frames: &'static [u8],
        started: std::sync::Mutex<u32>,
    }

    struct Ended(Arc<AtomicBool>);

    impl Process for Ended {
        fn exited(&mut self) -> io::Result<bool> {
            Ok(self.0.load(Ordering::Acquire))
        }
        fn end(&mut self) {
            self.0.store(true, Ordering::Release);
        }
    }

    impl Launch for Scripted {
        fn start(&self, _: &File, environment: &[(&'static str, OsString)]) -> io::Result<Started> {
            assert!(environment.iter().all(|(key, _)| ENVIRONMENT.contains(key)));
            *self.started.lock().unwrap() += 1;
            let (output, mut writer) = pipe();
            let (_, input) = pipe();
            use std::io::Write;
            writer.write_all(self.frames).unwrap();
            Ok(Started { input, output, process: Box::new(Ended(Arc::new(AtomicBool::new(false)))) })
        }
    }

    fn pipe() -> (File, File) {
        let mut fds = [0; 2];
        assert_eq!(unsafe { libc::pipe2(fds.as_mut_ptr(), libc::O_CLOEXEC) }, 0);
        unsafe { (File::from_raw_fd(fds[0]), File::from_raw_fd(fds[1])) }
    }

    #[test]
    fn stop_ends_a_launched_observer_that_does_not_exit_when_asked() {
        let launcher = Scripted { frames: b"", started: std::sync::Mutex::new(0) };
        let image = File::open("/dev/null").unwrap();
        let Started { input, output, mut process } = launcher.start(&image, &environment()).unwrap();
        drop(output);
        stop(input, process.as_mut());
        assert!(process.exited().unwrap());
        assert_eq!(*launcher.started.lock().unwrap(), 1);
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
