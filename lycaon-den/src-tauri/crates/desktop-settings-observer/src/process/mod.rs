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
mod tests;
