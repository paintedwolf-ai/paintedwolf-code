//! Private X11 and headless Wayland displays for GTK fixtures. A fixture runs
//! in a child test process so it never touches the developer's session.
//! Missing display servers fail; they never skip.

use std::io::{BufReader, ErrorKind, Read};
use std::os::unix::fs::PermissionsExt;
use std::os::unix::net::UnixStream;
use std::path::Path;
use std::process::{Child, Command, Output, Stdio};
use std::time::{Duration, Instant};

pub(crate) const FIXTURE_DEADLINE: Duration = Duration::from_secs(60);
const SERVER_POLL: Duration = Duration::from_millis(50);

pub(crate) struct OwnedChild(pub(crate) Child);

impl Drop for OwnedChild {
    fn drop(&mut self) {
        let _ = self.0.kill();
        let _ = self.0.wait();
    }
}

/// Reruns this test binary for one test, without the session's display.
pub(crate) fn fixture_command(test: &str) -> Command {
    let mut command = Command::new(std::env::current_exe().unwrap());
    command.args(["--exact", test, "--nocapture"]);
    for variable in [
        "DISPLAY",
        "WAYLAND_DISPLAY",
        "GDK_BACKEND",
        "XDG_RUNTIME_DIR",
    ] {
        command.env_remove(variable);
    }
    command.stdout(Stdio::piped()).stderr(Stdio::piped());
    command
}

pub(crate) fn run_fixture(command: &mut Command) -> Output {
    let mut child = OwnedChild(command.spawn().unwrap());
    let deadline = Instant::now() + FIXTURE_DEADLINE;
    loop {
        if child.0.try_wait().unwrap().is_some() {
            break;
        }
        assert!(Instant::now() < deadline, "display fixture timed out");
        std::thread::sleep(Duration::from_millis(10));
    }
    let stdout = child.0.stdout.take().unwrap();
    let stderr = child.0.stderr.take().unwrap();
    let status = child.0.wait().unwrap();
    let mut output = Output {
        status,
        stdout: Vec::new(),
        stderr: Vec::new(),
    };
    BufReader::new(stdout)
        .read_to_end(&mut output.stdout)
        .unwrap();
    BufReader::new(stderr)
        .read_to_end(&mut output.stderr)
        .unwrap();
    assert!(
        status.success(),
        "display fixture failed: {} {}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
    output
}

pub(crate) fn start_xvfb() -> (OwnedChild, String) {
    let (mut reader, writer) = UnixStream::pair().unwrap();
    // Short reads so a server that exits is noticed while it is awaited.
    reader.set_read_timeout(Some(SERVER_POLL)).unwrap();
    let mut server = OwnedChild(
        Command::new("Xvfb")
            .args(["-displayfd", "0", "-nolisten", "tcp", "-ac"])
            .stdin(Stdio::from(std::os::fd::OwnedFd::from(writer)))
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .spawn()
            .expect("start private X11 server"),
    );
    let deadline = Instant::now() + FIXTURE_DEADLINE;
    let mut line = Vec::new();
    let mut byte = [0u8; 1];
    while !line.ends_with(b"\n") {
        match reader.read(&mut byte) {
            Ok(0) => panic!("X11 server closed its display descriptor without naming a display"),
            Ok(_) => line.push(byte[0]),
            Err(error)
                if matches!(
                    error.kind(),
                    ErrorKind::WouldBlock | ErrorKind::TimedOut | ErrorKind::Interrupted
                ) =>
            {
                await_server(&mut server, deadline, "X11 server named no display");
            }
            Err(error) => panic!("read X11 display number: {error}"),
        }
        assert!(line.len() <= 32, "X11 display number is too long");
    }
    let display: u16 = String::from_utf8_lossy(&line)
        .trim()
        .parse()
        .expect("private display number");
    (server, format!(":{display}"))
}

/// Fails the fixture when the server exited or the deadline passed.
fn await_server(server: &mut OwnedChild, deadline: Instant, missing: &str) {
    if let Some(status) = server.0.try_wait().unwrap() {
        panic!("{missing}: server exited with {status}");
    }
    assert!(Instant::now() < deadline, "{missing}");
}

/// A headless sway with XWayland disabled, on a private runtime directory.
pub(crate) fn start_sway(runtime: &Path) -> (OwnedChild, String) {
    std::fs::set_permissions(runtime, std::fs::Permissions::from_mode(0o700)).unwrap();
    let config = runtime.join("sway.config");
    std::fs::write(&config, "xwayland disable\n").unwrap();
    let log = std::fs::File::create(runtime.join("sway.log")).unwrap();
    let mut server = OwnedChild(
        Command::new("sway")
            .arg("-c")
            .arg(&config)
            .env("XDG_RUNTIME_DIR", runtime)
            .env("WLR_BACKENDS", "headless")
            .env("WLR_LIBINPUT_NO_DEVICES", "1")
            .env("WLR_RENDERER", "pixman")
            .env_remove("DISPLAY")
            .env_remove("WAYLAND_DISPLAY")
            .stdin(Stdio::null())
            .stdout(Stdio::null())
            .stderr(log)
            .spawn()
            .expect("start private headless Wayland compositor"),
    );
    let deadline = Instant::now() + FIXTURE_DEADLINE;
    loop {
        let socket = std::fs::read_dir(runtime)
            .unwrap()
            .filter_map(Result::ok)
            .map(|entry| entry.file_name().to_string_lossy().into_owned())
            .find(|name| name.starts_with("wayland-") && !name.ends_with(".lock"));
        if let Some(socket) = socket {
            return (server, socket);
        }
        let missing = format!(
            "compositor exposed no Wayland socket: {}",
            std::fs::read_to_string(runtime.join("sway.log")).unwrap_or_default()
        );
        await_server(&mut server, deadline, &missing);
        std::thread::sleep(SERVER_POLL);
    }
}
