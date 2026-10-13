//! Private display fixtures that drive the GTK adapter against independent
//! peers: Xvfb with `xclip`, and a headless sway compositor with
//! `wl-clipboard`, XWayland disabled.
//!
//! The owner runs in a child process so a private display never touches the
//! developer's session clipboard. Missing fixtures fail; they never skip.

use super::linux::{GlibMainThread, NativeClipboard};
use super::service::{ClipboardError, ClipboardService, DisplayProtocol};
use crate::test_support::TempDir;
use gtk::prelude::*;
use std::io::{BufRead, BufReader, Read, Write};
use std::os::unix::fs::PermissionsExt;
use std::os::unix::net::UnixStream;
use std::path::Path;
use std::process::{Child, Command, Output, Stdio};
use std::time::{Duration, Instant};

const FIXTURE_DEADLINE: Duration = Duration::from_secs(60);
const PEER_DEADLINE: Duration = Duration::from_secs(10);

struct OwnedChild(Child);

impl Drop for OwnedChild {
    fn drop(&mut self) {
        let _ = self.0.kill();
        let _ = self.0.wait();
    }
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
enum Peer {
    X11,
    Wayland,
}

impl Peer {
    fn from_env() -> Self {
        match std::env::var("PW_CLIPBOARD_PEER").as_deref() {
            Ok("x11") => Self::X11,
            Ok("wayland") => Self::Wayland,
            other => panic!("unknown clipboard peer {other:?}"),
        }
    }

    fn protocol(self) -> DisplayProtocol {
        match self {
            Self::X11 => DisplayProtocol::X11,
            Self::Wayland => DisplayProtocol::Wayland,
        }
    }

    fn read_command(self) -> Command {
        let mut command = match self {
            Self::X11 => {
                let mut command = Command::new("xclip");
                command.args(["-selection", "clipboard", "-o"]);
                command
            }
            Self::Wayland => {
                let mut command = Command::new("wl-paste");
                command.arg("--no-newline");
                command
            }
        };
        command.stdin(Stdio::null());
        command
    }

    /// The peer's view of the clipboard: text, or `None` without an owner.
    fn read(self) -> Option<String> {
        let output = self
            .read_command()
            .output()
            .unwrap_or_else(|e| panic!("start clipboard peer reader for {self:?}: {e}"));
        let text = output
            .status
            .success()
            .then(|| String::from_utf8(output.stdout).expect("peer text is UTF-8"))?;
        // GTK serves the `text/plain` MIME types in their CRLF canonical form,
        // which GTK readers fold back; `wl-paste` shows the bytes as served.
        Some(match self {
            Self::Wayland => text.replace("\r\n", "\n"),
            Self::X11 => text,
        })
    }

    fn read_until(self, expected: Option<&str>) -> Option<String> {
        let deadline = Instant::now() + PEER_DEADLINE;
        loop {
            let read = self.read();
            if read.as_deref() == expected || Instant::now() >= deadline {
                return read;
            }
            std::thread::sleep(Duration::from_millis(50));
        }
    }

    /// Takes ownership away from the adapter, as another application would.
    fn write(self, text: &str) {
        let mut command = match self {
            Self::X11 => {
                let mut command = Command::new("xclip");
                command.args(["-selection", "clipboard", "-i"]);
                command
            }
            Self::Wayland => {
                let mut command = Command::new("wl-copy");
                command
            }
        };
        // The peer forks a server for the selection; only the parent is awaited.
        let mut child = command
            .stdin(Stdio::piped())
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .spawn()
            .unwrap_or_else(|e| panic!("start clipboard peer writer for {self:?}: {e}"));
        child
            .stdin
            .take()
            .unwrap()
            .write_all(text.as_bytes())
            .unwrap();
        assert!(child.wait().unwrap().success(), "peer write failed");
    }

    /// Only Wayland peers can end a selection without owning it.
    fn clear(self) -> bool {
        if self != Self::Wayland {
            return false;
        }
        let status = Command::new("wl-copy")
            .arg("--clear")
            .stdin(Stdio::null())
            .status()
            .expect("start wl-copy --clear");
        assert!(status.success(), "peer clear failed");
        true
    }

    /// Wayland accepts a selection only from a client that received input,
    /// so the owner's window gets one key through the compositor's virtual
    /// keyboard, as a person's keyboard would provide.
    fn send_key_to_owner_window(self) {
        let status = Command::new("wtype")
            .args(["-k", "Shift_L"])
            .stdin(Stdio::null())
            .status()
            .expect("start wtype to focus the owner window");
        assert!(status.success(), "wtype could not reach the compositor");
    }
}

fn fixture_command(test: &str) -> Command {
    let mut command = Command::new(std::env::current_exe().unwrap());
    command.args(["--exact", test, "--nocapture"]);
    for variable in ["DISPLAY", "WAYLAND_DISPLAY", "GDK_BACKEND", "XDG_RUNTIME_DIR"] {
        command.env_remove(variable);
    }
    command.stdout(Stdio::piped()).stderr(Stdio::piped());
    command
}

fn run_fixture(command: &mut Command) -> Output {
    let mut child = OwnedChild(command.spawn().unwrap());
    let deadline = Instant::now() + FIXTURE_DEADLINE;
    loop {
        if child.0.try_wait().unwrap().is_some() {
            break;
        }
        assert!(Instant::now() < deadline, "clipboard fixture timed out");
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
        "clipboard fixture failed: {} {}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
    output
}

fn owner_child(peer: &str) -> Command {
    let mut command = fixture_command("clipboard::tests_linux::clipboard_owner_child");
    command
        .env("PW_CLIPBOARD_OWNER_FIXTURE", "1")
        .env("PW_CLIPBOARD_PEER", peer);
    command
}

fn start_xvfb() -> (OwnedChild, String) {
    let (reader, writer) = UnixStream::pair().unwrap();
    reader
        .set_read_timeout(Some(Duration::from_secs(5)))
        .unwrap();
    let server = OwnedChild(
        Command::new("Xvfb")
            .args(["-displayfd", "0", "-nolisten", "tcp", "-ac"])
            .stdin(Stdio::from(std::os::fd::OwnedFd::from(writer)))
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .spawn()
            .expect("start private X11 clipboard server"),
    );
    let mut display = String::new();
    BufReader::new(reader.take(32))
        .read_line(&mut display)
        .unwrap();
    let display: u16 = display.trim().parse().expect("private display number");
    (server, format!(":{display}"))
}

/// A headless sway with XWayland disabled, on a private runtime directory.
fn start_sway(runtime: &Path) -> (OwnedChild, String) {
    std::fs::set_permissions(runtime, std::fs::Permissions::from_mode(0o700)).unwrap();
    let config = runtime.join("sway.config");
    std::fs::write(&config, "xwayland disable\n").unwrap();
    let log = std::fs::File::create(runtime.join("sway.log")).unwrap();
    let server = OwnedChild(
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
    let deadline = Instant::now() + Duration::from_secs(15);
    loop {
        let socket = std::fs::read_dir(runtime)
            .unwrap()
            .filter_map(Result::ok)
            .map(|entry| entry.file_name().to_string_lossy().into_owned())
            .find(|name| name.starts_with("wayland-") && !name.ends_with(".lock"));
        if let Some(socket) = socket {
            return (server, socket);
        }
        assert!(
            Instant::now() < deadline,
            "compositor exposed no Wayland socket: {}",
            std::fs::read_to_string(runtime.join("sway.log")).unwrap_or_default()
        );
        std::thread::sleep(Duration::from_millis(50));
    }
}

#[test]
fn x11_display_fixture_exercises_the_gtk_adapter() {
    let (_server, display) = start_xvfb();
    run_fixture(
        owner_child("x11")
            .env("DISPLAY", &display)
            .env("GDK_BACKEND", "x11"),
    );
    // Ownership ended with the owner process.
    let after = Peer::X11
        .read_command()
        .env("DISPLAY", &display)
        .env_remove("WAYLAND_DISPLAY")
        .output()
        .unwrap();
    assert!(
        !after.status.success() || after.stdout.is_empty(),
        "released clipboard still served {:?}",
        String::from_utf8_lossy(&after.stdout)
    );
}

#[test]
fn wayland_display_fixture_exercises_the_gtk_adapter() {
    let runtime = TempDir::new("wayland-clipboard");
    let (_server, socket) = start_sway(&runtime);
    run_fixture(
        owner_child("wayland")
            .env("WAYLAND_DISPLAY", &socket)
            .env("XDG_RUNTIME_DIR", runtime.path())
            .env("GDK_BACKEND", "wayland"),
    );
    let after = Peer::Wayland
        .read_command()
        .env("WAYLAND_DISPLAY", &socket)
        .env("XDG_RUNTIME_DIR", runtime.path())
        .env_remove("DISPLAY")
        .output()
        .unwrap();
    assert!(
        !after.status.success(),
        "released clipboard still served {:?}",
        String::from_utf8_lossy(&after.stdout)
    );
}

#[test]
fn clipboard_owner_child() {
    if std::env::var("PW_CLIPBOARD_OWNER_FIXTURE").as_deref() != Ok("1") {
        return;
    }
    let peer = Peer::from_env();
    gtk::init().expect("initialize GTK on the private display");
    // A mapped window gives the owner focus, as the application's windows do.
    let window = gtk::Window::new(gtk::WindowType::Toplevel);
    window.set_title("Painted Wolf Code clipboard fixture");
    window.set_default_size(320, 200);
    window.show_all();
    let worker = std::thread::spawn(move || {
        let outcome = std::panic::catch_unwind(|| exercise_adapter(peer));
        gtk::glib::MainContext::default().invoke(gtk::main_quit);
        outcome
    });
    gtk::main();
    if let Err(panic) = worker.join().unwrap() {
        std::panic::resume_unwind(panic);
    }
}

fn exercise_adapter(peer: Peer) {
    let main = GlibMainThread;
    let service = ClipboardService::<NativeClipboard>::default();
    tauri::async_runtime::block_on(async {
        let facts = service.facts(&main).await.unwrap();
        assert_eq!(facts.protocol, peer.protocol());
        assert!(!facts.persistence, "private displays have no clipboard manager");
        // A fresh private display has no selection to read.
        assert_eq!(service.read(&main).await.unwrap(), "");
        if peer == Peer::Wayland {
            establish_focus(&service, &main).await;
        }

        for text in ["copied λ text\nsecond line", "replacement contents"] {
            service.write(&main, text.into()).await.unwrap();
            assert_eq!(peer.read_until(Some(text)).as_deref(), Some(text));
            assert_eq!(service.read(&main).await.unwrap(), text);
        }

        // A peer that asks later is still served by the owner.
        service.write(&main, "served later ✓".into()).await.unwrap();
        std::thread::sleep(Duration::from_millis(500));
        assert_eq!(peer.read().as_deref(), Some("served later ✓"));
        assert_eq!(peer.read().as_deref(), Some("served later ✓"));

        // Another application replaces the owner; the adapter reads its text.
        peer.write("peer owned text");
        let _ = peer.read_until(Some("peer owned text"));
        let deadline = Instant::now() + PEER_DEADLINE;
        loop {
            let read = service.read(&main).await.unwrap();
            if read == "peer owned text" {
                break;
            }
            assert!(Instant::now() < deadline, "adapter still reads {read:?}");
            std::thread::sleep(Duration::from_millis(50));
        }
        if peer.clear() {
            let _ = peer.read_until(None);
            let deadline = Instant::now() + PEER_DEADLINE;
            loop {
                let read = service.read(&main).await.unwrap();
                if read.is_empty() {
                    break;
                }
                assert!(Instant::now() < deadline, "adapter still reads {read:?}");
                std::thread::sleep(Duration::from_millis(50));
            }
        }

        // Ownership returns to the adapter.
        service.write(&main, "owner again".into()).await.unwrap();
        assert_eq!(
            peer.read_until(Some("owner again")).as_deref(),
            Some("owner again")
        );

        service.shutdown(&main);
        service.shutdown(&main);
        assert_eq!(
            service.write(&main, "after exit".into()).await,
            Err(ClipboardError::Stopped)
        );
        assert_eq!(service.read(&main).await, Err(ClipboardError::Stopped));
        // Shutdown only hands text to a manager; this process still serves it.
        assert_eq!(peer.read().as_deref(), Some("owner again"));
    });
}

/// Repeats the key until the compositor accepts a selection from the window,
/// which can only happen once the window is mapped and focused.
async fn establish_focus(service: &ClipboardService<NativeClipboard>, main: &GlibMainThread) {
    let deadline = Instant::now() + PEER_DEADLINE;
    loop {
        Peer::Wayland.send_key_to_owner_window();
        service.write(main, "focus probe".into()).await.unwrap();
        std::thread::sleep(Duration::from_millis(200));
        if Peer::Wayland.read().as_deref() == Some("focus probe") {
            return;
        }
        assert!(Instant::now() < deadline, "owner window never gained focus");
    }
}
