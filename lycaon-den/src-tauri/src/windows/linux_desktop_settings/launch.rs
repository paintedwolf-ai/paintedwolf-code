//! Starts the Settings observer where the portal can identify it.
//!
//! The portal names its caller from `/proc/<pid>/root`, which the protected
//! namespace hides, so under the protected launch the observer runs through
//! the launch broker, outside that namespace. The broker executes the image
//! the shell verified, passed as descriptor 3, so nothing re-resolves its path.
//! The observer hardens itself on start, since the broker sets no
//! `no_new_privs`. Elsewhere it remains the shell's own child.
use crate::external_launch::brokered::{self, Call};
use desktop_settings_observer::process::{Launch, Local, Process, Started};
use std::ffi::OsString;
use std::fs::File;
use std::io;
use std::os::fd::{AsRawFd, RawFd};

pub(super) struct ShellLaunch;

impl Launch for ShellLaunch {
    fn start(&self, image: &File, environment: &[(&'static str, OsString)]) -> io::Result<Started> {
        match crate::protected_launch::broker() {
            Some(broker) => start_brokered(broker, image, environment),
            None => Local.start(image, environment),
        }
    }
}

struct Brokered(Option<Call>);

impl Process for Brokered {
    fn exited(&mut self) -> io::Result<bool> {
        self.0.as_mut().map_or(Ok(true), Call::exited)
    }

    fn end(&mut self) {
        if let Some(call) = self.0.take() {
            call.end();
        }
    }
}

fn start_brokered(broker: RawFd, image: &File, environment: &[(&'static str, OsString)]) -> io::Result<Started> {
    let (input_read, input) = brokered::pipe()?;
    let (output, output_write) = brokered::pipe()?;
    let null = File::open("/dev/null")?;
    let entries: Vec<String> = environment
        .iter()
        .map(|(key, value)| format!("{key}={}", value.to_string_lossy()))
        .collect();
    let call = brokered::start(
        broker,
        &["/proc/self/fd/3".into()],
        &entries,
        [input_read.as_raw_fd(), output_write.as_raw_fd(), null.as_raw_fd()],
        &[image.as_raw_fd()],
    )?;
    Ok(Started { input: input.into(), output: output.into(), process: Box::new(Brokered(Some(call))) })
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::{Read, Write};
    use std::os::fd::FromRawFd;
    use std::os::unix::fs::MetadataExt;

    /// What the stand-in broker received for one request.
    struct Received {
        request: serde_json::Value,
        image: (u64, u64),
        read_input_to_end: bool,
        channel_closed: bool,
    }

    /// A stand-in broker: it takes one request, checks its descriptors, writes a
    /// frame to the program's output, reports a start, and then either reports an
    /// exit once the program's input closes or waits for the desktop to close the call.
    fn broker(reply_exit_on_input_close: bool, refuse: bool) -> (RawFd, std::thread::JoinHandle<Received>) {
        let mut fds = [0; 2];
        // SAFETY: socketpair writes two descriptors into the local array.
        assert_eq!(unsafe { libc::socketpair(libc::AF_UNIX, libc::SOCK_SEQPACKET, 0, fds.as_mut_ptr()) }, 0);
        let (client, server) = (fds[0], fds[1]);
        let thread = std::thread::spawn(move || {
            let mut payload = vec![0u8; 4096];
            let mut control = vec![0u8; 512];
            let mut iov = libc::iovec { iov_base: payload.as_mut_ptr().cast(), iov_len: payload.len() };
            // SAFETY: the header points at locals for one recvmsg call; the received
            // descriptors become Files owned by this thread.
            let (read, received) = unsafe {
                let mut message: libc::msghdr = std::mem::zeroed();
                message.msg_iov = &mut iov;
                message.msg_iovlen = 1;
                message.msg_control = control.as_mut_ptr().cast();
                message.msg_controllen = control.len() as _;
                let read = libc::recvmsg(server, &mut message, 0);
                let header = libc::CMSG_FIRSTHDR(&message);
                let count = ((*header).cmsg_len as usize - libc::CMSG_LEN(0) as usize) / 4;
                let mut raw = vec![0i32; count];
                std::ptr::copy_nonoverlapping(libc::CMSG_DATA(header).cast::<i32>(), raw.as_mut_ptr(), count);
                libc::close(server);
                (read, raw.into_iter().map(|fd| File::from_raw_fd(fd)).collect::<Vec<_>>())
            };
            let request: serde_json::Value = serde_json::from_slice(&payload[..read as usize]).unwrap();
            assert_eq!(received.len(), 5, "call channel, three streams and the image");
            let mut received = received.into_iter();
            let mut channel = received.next().unwrap();
            let mut input = received.next().unwrap();
            let mut output = received.next().unwrap();
            let _error = received.next().unwrap();
            let image = received.next().unwrap().metadata().unwrap();
            if refuse {
                channel.write_all(br#"{"error":"not permitted"}"#).unwrap();
                return Received { request, image: (image.dev(), image.ino()), read_input_to_end: false, channel_closed: false };
            }
            output.write_all(b"frame").unwrap();
            drop(output);
            channel.write_all(br#"{"started":4242}"#).unwrap();
            let (mut read_input_to_end, mut channel_closed) = (false, false);
            if reply_exit_on_input_close {
                read_input_to_end = input.read_to_end(&mut Vec::new()).is_ok();
                channel.write_all(br#"{"exited":true}"#).unwrap();
            } else {
                channel_closed = channel.read(&mut [0u8; 16]).unwrap() == 0;
            }
            Received { request, image: (image.dev(), image.ino()), read_input_to_end, channel_closed }
        });
        (client, thread)
    }

    fn image() -> File {
        File::open(std::env::current_exe().unwrap()).unwrap()
    }

    #[test]
    fn the_broker_executes_the_verified_image_with_only_the_observer_environment() {
        let (client, stand_in) = broker(true, false);
        let image = image();
        let identity = image.metadata().unwrap();
        let environment = [("XDG_RUNTIME_DIR", OsString::from("/run/user/1000")), ("LANG", OsString::from("C.UTF-8"))];
        let Started { input, mut output, mut process } = start_brokered(client, &image, &environment).unwrap();
        let mut frame = Vec::new();
        output.read_to_end(&mut frame).unwrap();
        assert_eq!(frame, b"frame");
        assert!(!process.exited().unwrap(), "running until its input closes");
        drop(input);
        let deadline = std::time::Instant::now() + std::time::Duration::from_secs(5);
        while !process.exited().unwrap() {
            assert!(std::time::Instant::now() < deadline, "the broker's exit report arrives");
            std::thread::sleep(std::time::Duration::from_millis(10));
        }
        let received = stand_in.join().unwrap();
        assert!(received.read_input_to_end);
        assert_eq!(received.image, (identity.dev(), identity.ino()), "descriptor 3 is the verified image");
        assert_eq!(received.request["argv"], serde_json::json!(["/proc/self/fd/3"]));
        assert_eq!(received.request["extra_files"], 1);
        assert_eq!(received.request["environment"], serde_json::json!(["XDG_RUNTIME_DIR=/run/user/1000", "LANG=C.UTF-8"]));
        // SAFETY: closes the client end this test created.
        unsafe { libc::close(client) };
    }

    #[test]
    fn ending_an_observer_closes_the_call_so_the_broker_ends_its_session() {
        let (client, stand_in) = broker(false, false);
        let Started { input, output, mut process } = start_brokered(client, &image(), &[]).unwrap();
        drop((input, output));
        process.end();
        assert!(process.exited().unwrap());
        assert!(stand_in.join().unwrap().channel_closed);
        // SAFETY: closes the client end this test created.
        unsafe { libc::close(client) };
    }

    #[test]
    fn a_broker_refusal_starts_nothing() {
        let (client, stand_in) = broker(false, true);
        let error = start_brokered(client, &image(), &[]).err().expect("refused");
        assert!(error.to_string().contains("not permitted"), "{error}");
        stand_in.join().unwrap();
        // SAFETY: closes the client end this test created.
        unsafe { libc::close(client) };
    }
}
