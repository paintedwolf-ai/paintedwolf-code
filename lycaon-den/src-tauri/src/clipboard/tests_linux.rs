use super::ClipboardState;
use std::{
    io::{BufRead, BufReader, Read},
    os::unix::net::UnixStream,
    process::{Child, Command, Output, Stdio},
    time::{Duration, Instant},
};

struct OwnedChild(Child);

impl Drop for OwnedChild {
    fn drop(&mut self) {
        let _ = self.0.kill();
        let _ = self.0.wait();
    }
}

fn fixture_command(test: &str) -> Command {
    let mut command = Command::new(std::env::current_exe().unwrap());
    command.args(["--exact", test, "--nocapture"]);
    command.env_remove("WAYLAND_DISPLAY");
    command.stdout(Stdio::piped()).stderr(Stdio::piped());
    command
}

fn run_fixture(command: &mut Command) -> Output {
    let mut child = OwnedChild(command.spawn().unwrap());
    let deadline = Instant::now() + Duration::from_secs(20);
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
    let mut output = Output { status, stdout: Vec::new(), stderr: Vec::new() };
    BufReader::new(stdout).read_to_end(&mut output.stdout).unwrap();
    BufReader::new(stderr).read_to_end(&mut output.stderr).unwrap();
    assert!(status.success(), "clipboard fixture failed: {} {}", String::from_utf8_lossy(&output.stdout), String::from_utf8_lossy(&output.stderr));
    output
}

#[test]
fn clipboard_commands_keep_selection_without_a_clipboard_manager() {
    let (reader, writer) = UnixStream::pair().unwrap();
    reader.set_read_timeout(Some(Duration::from_secs(5))).unwrap();
    let _server = OwnedChild(Command::new("Xvfb")
        .args(["-displayfd", "0", "-nolisten", "tcp", "-ac"])
        .stdin(Stdio::from(std::os::fd::OwnedFd::from(writer)))
        .stdout(Stdio::null()).stderr(Stdio::null())
        .spawn().expect("start private X11 clipboard server"));
    let mut display = String::new();
    BufReader::new(reader.take(32)).read_line(&mut display).unwrap();
    let display: u16 = display.trim().parse().expect("private display number");
    run_fixture(fixture_command("clipboard::tests_linux::clipboard_owner_child")
        .env("DISPLAY", format!(":{display}"))
        .env("PW_CLIPBOARD_OWNER_FIXTURE", "1"));
}

#[test]
fn clipboard_owner_child() {
    if std::env::var("PW_CLIPBOARD_OWNER_FIXTURE").as_deref() != Ok("1") {
        return;
    }
    let owner = ClipboardState::default();
    for text in ["copied λ text\nsecond line", "replacement contents"] {
        owner.with_clipboard(|clipboard| clipboard.set_text(text).map_err(|e| e.to_string())).unwrap();
        run_fixture(fixture_command("clipboard::tests_linux::clipboard_consumer_child")
            .env("PW_CLIPBOARD_EXPECT_TEXT", text)
            .env_remove("PW_CLIPBOARD_EXPECT_EMPTY"));
        let read = owner.with_clipboard(|clipboard| clipboard.get_text().map_err(|e| e.to_string())).unwrap();
        assert_eq!(read, text);
    }
    owner.shutdown();
    owner.shutdown();
    run_fixture(fixture_command("clipboard::tests_linux::clipboard_consumer_child")
        .env_remove("PW_CLIPBOARD_EXPECT_TEXT")
        .env("PW_CLIPBOARD_EXPECT_EMPTY", "1"));
}

#[test]
fn clipboard_consumer_child() {
    let expected = std::env::var("PW_CLIPBOARD_EXPECT_TEXT");
    let empty = std::env::var("PW_CLIPBOARD_EXPECT_EMPTY").as_deref() == Ok("1");
    if expected.is_err() && !empty {
        return;
    }
    let mut clipboard = arboard::Clipboard::new().unwrap();
    let contents = clipboard.get_text();
    if empty {
        assert!(matches!(contents, Err(arboard::Error::ContentNotAvailable)), "released clipboard returned {contents:?}");
    } else {
        assert_eq!(contents.unwrap(), expected.unwrap());
    }
}
