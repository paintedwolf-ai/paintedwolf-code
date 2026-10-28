//! Programs the desktop starts on the user's behalf: editors, default applications,
//! the file manager fallback and package queries. Under the protected Linux launch they
//! run through the launch broker, outside the protected namespace, because nothing but
//! the generation may execute inside it. Otherwise they start here and drop the
//! desktop's protected credentials first.
use std::ffi::OsString;
use std::io;
use std::process::{Command, ExitStatus, Stdio};

/// One external program and the environment it receives.
pub(crate) struct External {
    program: OsString,
    args: Vec<OsString>,
    clear_env: bool,
    env: Vec<(OsString, OsString)>,
}

/// What a finished program left: its status and, when captured, its output.
pub(crate) struct Finished {
    pub status: ExitStatus,
    pub stdout: Vec<u8>,
}

impl External {
    pub(crate) fn new(program: impl Into<OsString>) -> Self {
        Self { program: program.into(), args: Vec::new(), clear_env: false, env: Vec::new() }
    }

    pub(crate) fn args<I, S>(mut self, args: I) -> Self
    where
        I: IntoIterator<Item = S>,
        S: Into<OsString>,
    {
        self.args.extend(args.into_iter().map(Into::into));
        self
    }

    /// Start from an empty environment instead of the desktop's.
    pub(crate) fn clear_env(mut self) -> Self {
        self.clear_env = true;
        self
    }

    pub(crate) fn env(mut self, key: impl Into<OsString>, value: impl Into<OsString>) -> Self {
        self.env.push((key.into(), value.into()));
        self
    }

    /// Run to completion with no captured output.
    pub(crate) fn status(self) -> io::Result<ExitStatus> {
        self.run(false).map(|finished| finished.status)
    }

    /// Run to completion and capture standard output.
    pub(crate) fn output(self) -> io::Result<Finished> {
        self.run(true)
    }

    fn run(self, capture: bool) -> io::Result<Finished> {
        #[cfg(target_os = "linux")]
        if let Some(broker) = crate::protected_launch::broker() {
            return brokered::run(broker, &self.environment(), &self.argv(), capture);
        }
        self.run_here(capture)
    }

    fn run_here(self, capture: bool) -> io::Result<Finished> {
        let mut command = Command::new(&self.program);
        command.args(&self.args).stdin(Stdio::null());
        if self.clear_env {
            command.env_clear();
        }
        command.envs(self.env.iter().map(|(key, value)| (key, value)));
        crate::child_credentials::prepare(&mut command);
        if capture {
            let output = command.stderr(Stdio::inherit()).output()?;
            Ok(Finished { status: output.status, stdout: output.stdout })
        } else {
            Ok(Finished { status: command.status()?, stdout: Vec::new() })
        }
    }

    fn argv(&self) -> Vec<String> {
        std::iter::once(&self.program)
            .chain(&self.args)
            .map(|value| value.to_string_lossy().into_owned())
            .collect()
    }

    /// The program's environment: the desktop's, less what only protected processes
    /// may hold, unless cleared, then the explicit entries.
    fn environment(&self) -> Vec<String> {
        let mut entries: Vec<(String, String)> = if self.clear_env {
            Vec::new()
        } else {
            std::env::vars_os()
                .map(|(key, value)| (key.to_string_lossy().into_owned(), value.to_string_lossy().into_owned()))
                .filter(|(key, _)| !protected_only(key))
                .collect()
        };
        for (key, value) in &self.env {
            let key = key.to_string_lossy().into_owned();
            entries.retain(|(existing, _)| *existing != key);
            entries.push((key, value.to_string_lossy().into_owned()));
        }
        entries.into_iter().map(|(key, value)| format!("{key}={value}")).collect()
    }
}

/// Variables that describe the protected launch or its loader and never leave it.
fn protected_only(key: &str) -> bool {
    key.starts_with("LYCAON_") || key.starts_with("LD_")
}

#[cfg(target_os = "linux")]
pub(crate) mod brokered {
    use super::Finished;
    use std::io::{self, Read};
    use std::os::fd::{AsRawFd, FromRawFd, OwnedFd, RawFd};
    use std::os::unix::process::ExitStatusExt;
    use std::process::ExitStatus;

    #[derive(serde::Serialize)]
    struct Request<'a> {
        version: u32,
        argv: &'a [String],
        environment: &'a [String],
        dir: &'a str,
        #[serde(skip_serializing_if = "is_zero")]
        extra_files: usize,
    }

    fn is_zero(count: &usize) -> bool {
        *count == 0
    }

    /// A program the broker runs for as long as the desktop keeps its call channel.
    pub(crate) struct Call {
        channel: OwnedFd,
        exited: bool,
    }

    impl Call {
        /// Whether the broker reported the program's exit or closed the call; never blocks.
        pub(crate) fn exited(&mut self) -> io::Result<bool> {
            let mut buffer = [0u8; 4096];
            while !self.exited {
                // SAFETY: recv writes at most the buffer's length into it.
                let read = unsafe {
                    libc::recv(self.channel.as_raw_fd(), buffer.as_mut_ptr().cast(), buffer.len(), libc::MSG_DONTWAIT)
                };
                if read < 0 {
                    let error = io::Error::last_os_error();
                    return match error.kind() {
                        io::ErrorKind::WouldBlock => Ok(false),
                        io::ErrorKind::Interrupted => continue,
                        _ => Err(error),
                    };
                }
                let event: Option<Event> = (read > 0).then(|| serde_json::from_slice(&buffer[..read as usize]).ok()).flatten();
                self.exited = read == 0 || event.is_some_and(|event| event.exited || !event.error.is_empty());
            }
            Ok(true)
        }

        /// Closing the call channel makes the broker end the program's session.
        pub(crate) fn end(self) {}
    }

    /// Starts a long-running program with the given standard streams and extra
    /// descriptors (from 3), returning once the broker reports it started.
    pub(crate) fn start(
        broker: RawFd,
        argv: &[String],
        environment: &[String],
        streams: [RawFd; 3],
        extra: &[RawFd],
    ) -> io::Result<Call> {
        let (channel, remote) = pair()?;
        let request = Request { version: 1, argv, environment, dir: "/", extra_files: extra.len() };
        let payload = serde_json::to_vec(&request).map_err(io::Error::other)?;
        let mut fds = vec![remote.as_raw_fd()];
        fds.extend(streams);
        fds.extend(extra);
        send(broker, &payload, &fds)?;
        drop(remote);
        let mut buffer = vec![0u8; 4096];
        loop {
            // SAFETY: recv writes at most the buffer's length into it.
            let read = unsafe { libc::recv(channel.as_raw_fd(), buffer.as_mut_ptr().cast(), buffer.len(), 0) };
            if read < 0 && io::Error::last_os_error().kind() == io::ErrorKind::Interrupted {
                continue;
            }
            if read <= 0 {
                return Err(io::Error::other("the launch broker closed the call before the program started"));
            }
            let event: Event = serde_json::from_slice(&buffer[..read as usize])
                .map_err(|error| io::Error::other(format!("the launch broker sent an invalid event: {error}")))?;
            if !event.error.is_empty() {
                return Err(io::Error::other(format!("the launch broker refused: {}", event.error)));
            }
            if event.started > 0 {
                return Ok(Call { channel, exited: false });
            }
        }
    }

    #[derive(serde::Deserialize, Default)]
    #[serde(deny_unknown_fields)]
    pub(super) struct Event {
        #[serde(default)]
        pub started: i32,
        #[serde(default)]
        pub exited: bool,
        #[serde(default)]
        pub code: i32,
        #[serde(default)]
        pub signal: i32,
        #[serde(default)]
        pub error: String,
    }

    /// The finished status a call channel's events describe.
    pub(super) fn outcome(events: &[Event]) -> io::Result<ExitStatus> {
        let mut started = false;
        for event in events {
            if !event.error.is_empty() {
                return Err(io::Error::other(format!("the launch broker refused: {}", event.error)));
            }
            if event.started > 0 {
                started = true;
            } else if event.exited && started {
                return Ok(if event.signal != 0 {
                    ExitStatus::from_raw(event.signal & 0x7f)
                } else {
                    ExitStatus::from_raw((event.code & 0xff) << 8)
                });
            }
        }
        Err(io::Error::other("the launch broker closed the call before the program finished"))
    }

    fn pair() -> io::Result<(OwnedFd, OwnedFd)> {
        let mut fds = [0; 2];
        // SAFETY: socketpair writes two new descriptors into the local array.
        if unsafe { libc::socketpair(libc::AF_UNIX, libc::SOCK_SEQPACKET | libc::SOCK_CLOEXEC, 0, fds.as_mut_ptr()) } != 0 {
            return Err(io::Error::last_os_error());
        }
        // SAFETY: both descriptors were just created and are owned here.
        Ok(unsafe { (OwnedFd::from_raw_fd(fds[0]), OwnedFd::from_raw_fd(fds[1])) })
    }

    pub(crate) fn pipe() -> io::Result<(OwnedFd, OwnedFd)> {
        let mut fds = [0; 2];
        // SAFETY: pipe2 writes two new descriptors into the local array.
        if unsafe { libc::pipe2(fds.as_mut_ptr(), libc::O_CLOEXEC) } != 0 {
            return Err(io::Error::last_os_error());
        }
        // SAFETY: both descriptors were just created and are owned here.
        Ok(unsafe { (OwnedFd::from_raw_fd(fds[0]), OwnedFd::from_raw_fd(fds[1])) })
    }

    /// Send one request with its call channel and standard streams.
    pub(super) fn send(broker: RawFd, payload: &[u8], fds: &[RawFd]) -> io::Result<()> {
        let rights = std::mem::size_of_val(fds);
        // SAFETY: CMSG_SPACE only computes a size.
        let space = unsafe { libc::CMSG_SPACE(rights as u32) } as usize;
        let mut control = vec![0u8; space];
        let mut iov = libc::iovec { iov_base: payload.as_ptr() as *mut _, iov_len: payload.len() };
        // SAFETY: the message header points at locals that outlive the sendmsg call, and
        // the control buffer is sized for exactly one SCM_RIGHTS message of `fds`.
        unsafe {
            let mut message: libc::msghdr = std::mem::zeroed();
            message.msg_iov = &mut iov;
            message.msg_iovlen = 1;
            message.msg_control = control.as_mut_ptr().cast();
            message.msg_controllen = space as _;
            let header = libc::CMSG_FIRSTHDR(&message);
            (*header).cmsg_level = libc::SOL_SOCKET;
            (*header).cmsg_type = libc::SCM_RIGHTS;
            (*header).cmsg_len = libc::CMSG_LEN(rights as u32) as _;
            std::ptr::copy_nonoverlapping(fds.as_ptr(), libc::CMSG_DATA(header).cast::<RawFd>(), fds.len());
            if libc::sendmsg(broker, &message, libc::MSG_NOSIGNAL) != payload.len() as isize {
                return Err(io::Error::last_os_error());
            }
        }
        Ok(())
    }

    /// Read events until the call channel reports the end or closes.
    pub(super) fn events(channel: RawFd) -> io::Result<Vec<Event>> {
        let mut events = Vec::new();
        let mut buffer = vec![0u8; 4096];
        loop {
            // SAFETY: recv writes at most the buffer's length into it.
            let read = unsafe { libc::recv(channel, buffer.as_mut_ptr().cast(), buffer.len(), 0) };
            if read < 0 {
                let error = io::Error::last_os_error();
                if error.kind() == io::ErrorKind::Interrupted {
                    continue;
                }
                return Err(error);
            }
            if read == 0 {
                return Ok(events);
            }
            let event: Event = serde_json::from_slice(&buffer[..read as usize])
                .map_err(|error| io::Error::other(format!("the launch broker sent an invalid event: {error}")))?;
            let done = event.exited || !event.error.is_empty();
            events.push(event);
            if done {
                return Ok(events);
            }
        }
    }

    pub(super) fn run(broker: RawFd, environment: &[String], argv: &[String], capture: bool) -> io::Result<Finished> {
        let (channel, remote) = pair()?;
        let null = std::fs::File::open("/dev/null")?;
        let output = if capture { Some(pipe()?) } else { None };
        let stdout = output.as_ref().map_or(null.as_raw_fd(), |(_, write)| write.as_raw_fd());
        let request = Request { version: 1, argv, environment, dir: "/", extra_files: 0 };
        let payload = serde_json::to_vec(&request).map_err(io::Error::other)?;
        // Standard error stays the desktop's own, so diagnostics reach its log.
        send(broker, &payload, &[remote.as_raw_fd(), null.as_raw_fd(), stdout, 2])?;
        drop(remote);
        let reader = output.map(|(read, write)| {
            drop(write);
            std::thread::spawn(move || {
                let mut captured = Vec::new();
                std::fs::File::from(read).read_to_end(&mut captured).map(|_| captured)
            })
        });
        // The channel stays open until the program exits; closing it early ends its session.
        let status = outcome(&events(channel.as_raw_fd())?)?;
        let stdout = match reader {
            Some(reader) => reader.join().map_err(|_| io::Error::other("output reader failed"))??,
            None => Vec::new(),
        };
        Ok(Finished { status, stdout })
    }
}

#[cfg(all(test, target_os = "linux"))]
mod tests {
    use super::*;
    use std::os::unix::process::ExitStatusExt;

    fn event(json: &str) -> brokered::Event {
        serde_json::from_str(json).unwrap()
    }

    #[test]
    fn the_brokered_environment_keeps_protected_launch_state_inside() {
        std::env::set_var("LYCAON_LAUNCH_BROKER_FD", "6");
        let program = External::new("xdg-open").env("PATH", "/usr/bin").env("LC_ALL", "C");
        let environment = program.environment();
        assert!(!environment.iter().any(|entry| entry.starts_with("LYCAON_") || entry.starts_with("LD_")));
        assert_eq!(environment.iter().filter(|entry| entry.starts_with("PATH=")).collect::<Vec<_>>(), ["PATH=/usr/bin"]);
        let cleared = External::new("dpkg").clear_env().env("PATH", "/usr/bin").environment();
        assert_eq!(cleared, ["PATH=/usr/bin"]);
        assert_eq!(External::new("code").args(["-g", "a b:1"]).argv(), ["code", "-g", "a b:1"]);
    }

    #[test]
    fn broker_events_become_the_programs_exit_status() {
        let ok = brokered::outcome(&[event(r#"{"started": 41}"#), event(r#"{"exited": true}"#)]).unwrap();
        assert!(ok.success());
        let failed = brokered::outcome(&[event(r#"{"started": 41}"#), event(r#"{"exited": true, "code": 3}"#)]).unwrap();
        assert_eq!(failed.code(), Some(3));
        let killed = brokered::outcome(&[event(r#"{"started": 41}"#), event(r#"{"exited": true, "signal": 9}"#)]).unwrap();
        assert_eq!(killed.signal(), Some(9));
        assert!(brokered::outcome(&[event(r#"{"error": "not found"}"#)]).is_err());
        assert!(brokered::outcome(&[event(r#"{"started": 41}"#)]).is_err());
        assert!(brokered::outcome(&[event(r#"{"exited": true}"#)]).is_err(), "an exit before a start is not a run");
        assert!(serde_json::from_str::<brokered::Event>(r#"{"started": 1, "unknown": 2}"#).is_err());
    }

    /// A stand-in broker on a socket pair: it receives the request and its descriptors,
    /// writes to the program's standard output, and reports a start and an exit.
    #[test]
    fn a_request_carries_its_streams_and_the_broker_reports_on_the_call_channel() {
        let mut fds = [0; 2];
        // SAFETY: socketpair writes two descriptors into the local array.
        assert_eq!(unsafe { libc::socketpair(libc::AF_UNIX, libc::SOCK_SEQPACKET, 0, fds.as_mut_ptr()) }, 0);
        let (client, server) = (fds[0], fds[1]);
        let broker = std::thread::spawn(move || {
            let mut payload = vec![0u8; 4096];
            let mut control = vec![0u8; 256];
            let mut iov = libc::iovec { iov_base: payload.as_mut_ptr().cast(), iov_len: payload.len() };
            // SAFETY: the header points at locals for one recvmsg call; received
            // descriptors are written with and closed by this thread.
            unsafe {
                let mut message: libc::msghdr = std::mem::zeroed();
                message.msg_iov = &mut iov;
                message.msg_iovlen = 1;
                message.msg_control = control.as_mut_ptr().cast();
                message.msg_controllen = control.len() as _;
                let read = libc::recvmsg(server, &mut message, 0);
                let header = libc::CMSG_FIRSTHDR(&message);
                let count = ((*header).cmsg_len as usize - libc::CMSG_LEN(0) as usize) / 4;
                let mut received = vec![0i32; count];
                std::ptr::copy_nonoverlapping(libc::CMSG_DATA(header).cast::<i32>(), received.as_mut_ptr(), count);
                let request: serde_json::Value = serde_json::from_slice(&payload[..read as usize]).unwrap();
                libc::write(received[2], b"listed\n".as_ptr().cast(), 7);
                for json in [r#"{"started":7}"#, r#"{"exited":true,"code":0}"#] {
                    libc::send(received[0], json.as_ptr().cast(), json.len(), 0);
                }
                for fd in received {
                    libc::close(fd);
                }
                libc::close(server);
                (request, count)
            }
        });
        let finished = brokered::run(client, &["PATH=/usr/bin".into()], &["dpkg".into(), "-S".into()], true).unwrap();
        let (request, count) = broker.join().unwrap();
        assert!(finished.status.success());
        assert_eq!(finished.stdout, b"listed\n");
        assert_eq!(count, 4, "call channel and three standard streams");
        assert_eq!(request["version"], 1);
        assert_eq!(request["argv"], serde_json::json!(["dpkg", "-S"]));
        assert_eq!(request["environment"], serde_json::json!(["PATH=/usr/bin"]));
        assert_eq!(request["dir"], "/");
        // SAFETY: closes the client end this test created.
        unsafe { libc::close(client) };
    }
}
