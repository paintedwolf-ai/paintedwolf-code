//! Fixed OS read interfaces share one deadline, output cap and cancellation scope.
use super::QueryReason;
use std::{io::Read, process::{Child, Command, Stdio}, sync::{Arc, atomic::{AtomicBool, Ordering}}, time::{Duration, Instant}};

pub(super) struct Output {
    pub code: i32,
    pub stdout: String,
}

pub(super) trait Reader {
    fn read(&mut self, program: &str, args: &[&str]) -> Result<Output, QueryReason>;
}

pub(super) struct OsReader {
    deadline: Instant,
    cancelled: Arc<AtomicBool>,
}

impl OsReader {
    pub fn new(timeout: Duration, cancelled: Arc<AtomicBool>) -> Self {
        Self { deadline: Instant::now() + timeout, cancelled }
    }
    fn check(&self) -> Result<(), QueryReason> {
        if self.cancelled.load(Ordering::Acquire) { return Err(QueryReason::Cancelled); }
        if Instant::now() >= self.deadline { return Err(QueryReason::TimedOut); }
        Ok(())
    }
}

struct OwnedChild(Child, bool);
impl Drop for OwnedChild {
    fn drop(&mut self) {
        // Only this newly created process group belongs to the query.
        if !self.1 {
            #[cfg(target_os = "linux")]
            unsafe { libc::kill(-(self.0.id() as i32), libc::SIGKILL); }
            let _ = self.0.kill();
            let _ = self.0.wait();
        }
    }
}

impl Reader for OsReader {
    fn read(&mut self, program: &str, args: &[&str]) -> Result<Output, QueryReason> {
        self.check()?;
        #[cfg(not(target_os = "linux"))]
        { let _ = (program, args); Err(QueryReason::AdapterUnavailable) }
        #[cfg(target_os = "linux")]
        {
            use std::os::{fd::AsRawFd, unix::process::CommandExt};
            let mut command = Command::new(program);
            command.args(args).env_clear()
                .env("PATH", "/usr/bin:/bin:/usr/sbin:/sbin").env("LC_ALL", "C")
                .current_dir("/")
                .stdin(Stdio::null()).stdout(Stdio::piped()).stderr(Stdio::null())
                .process_group(0);
            crate::child_credentials::prepare(&mut command);
            let child = command.spawn().map_err(|e| if e.kind() == std::io::ErrorKind::NotFound {
                    QueryReason::AdapterUnavailable
                } else { QueryReason::QueryFailed })?;
            let mut child = OwnedChild(child, false);
            let mut stdout = child.0.stdout.take().ok_or(QueryReason::QueryFailed)?;
            let fd = stdout.as_raw_fd();
            if unsafe { libc::fcntl(fd, libc::F_SETFL, libc::O_NONBLOCK) } < 0 {
                return Err(QueryReason::QueryFailed);
            }
            let mut bytes = Vec::new();
            let mut eof = false;
            loop {
                self.check()?;
                let mut buffer = [0u8; 4096];
                match stdout.read(&mut buffer) {
                    Ok(0) => eof = true,
                    Ok(n) => {
                        if bytes.len() + n > 64 * 1024 { return Err(QueryReason::OutputLimit); }
                        bytes.extend_from_slice(&buffer[..n]);
                    }
                    Err(e) if e.kind() == std::io::ErrorKind::WouldBlock => {},
                    Err(e) if e.kind() == std::io::ErrorKind::Interrupted => continue,
                    Err(_) => return Err(QueryReason::QueryFailed),
                }
                if eof {
                    // Keep the leader unreaped until its process group is cleaned up,
                    // so a recycled PID cannot identify an unrelated process group.
                    let mut info: libc::siginfo_t = unsafe { std::mem::zeroed() };
                    if unsafe { libc::waitid(libc::P_PID, child.0.id(), &mut info, libc::WEXITED | libc::WNOHANG | libc::WNOWAIT) } < 0 {
                        return Err(QueryReason::QueryFailed);
                    }
                    if unsafe { info.si_pid() } != 0 {
                        unsafe { libc::kill(-(child.0.id() as i32), libc::SIGKILL); }
                        let status = child.0.wait().map_err(|_| QueryReason::QueryFailed)?;
                        child.1 = true;
                        return Ok(Output { code: status.code().unwrap_or(-1), stdout: String::from_utf8(bytes).map_err(|_| QueryReason::InvalidResponse)? });
                    }
                }
                std::thread::sleep(Duration::from_millis(5));
            }
        }
    }
}

pub(super) fn success(reader: &mut impl Reader, program: &str, args: &[&str]) -> Result<String, QueryReason> {
    let output = reader.read(program, args)?;
    if output.code != 0 { return Err(QueryReason::QueryFailed); }
    Ok(output.stdout)
}

pub(super) fn field(value: &str) -> bool {
    !value.is_empty() && value.len() <= 4096 && !value.chars().any(char::is_control)
}

pub(super) fn package_name(value: &str) -> bool {
    !value.is_empty() && value.len() <= 255 && value.bytes().all(|c| c.is_ascii_alphanumeric() || b"+._-".contains(&c)) && !value.starts_with('-')
}
